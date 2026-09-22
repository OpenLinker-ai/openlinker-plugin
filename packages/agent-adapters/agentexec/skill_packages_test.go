package agentexec

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	openlinker "github.com/OpenLinker-ai/openlinker-go"
)

func testPackageSnapshot(provider, marker string) packageSnapshot {
	payload, _ := json.Marshal(packageContents{Name: "report", Description: "Report", Providers: []string{provider}, Files: map[string]string{"SKILL.md": "---\nname: report\ndescription: Report\n---\n" + marker, "references/example.txt": "versioned reference"}})
	digest := sha256.Sum256(payload)
	return packageSnapshot{Schema: 1, Bundles: []packageVersion{{BindingID: "11111111-1111-4111-8111-111111111111", PackageID: "22222222-2222-4222-8222-222222222222", VersionID: "33333333-3333-4333-8333-333333333333", Version: "1.0.0", Digest: hex.EncodeToString(digest[:]), Payload: string(payload)}}}
}

func TestSkillPackagesReachBothProviderProcesses(t *testing.T) {
	for _, name := range []string{"codex", "claude"} {
		t.Run(name, func(t *testing.T) {
			var bin, dir, log string
			if name == "codex" {
				dir = t.TempDir()
				bin = filepath.Join(dir, "codex")
				log = filepath.Join(dir, "calls")
				writeCodexRPCFixture(t, bin, "ephemeral")
			} else {
				bin, dir = reviewFakeCLI(t, `cat >> prompts
printf '%s\n' "$*" >> arguments
printf '%s\n' '{"type":"result","subtype":"success","result":"answer","session_id":"11111111-aaaa-4111-8111-111111111111"}'
`)
			}
			config := ProviderConfig{Provider: name, Bin: bin, Workspace: dir, SessionStore: filepath.Join(dir, "sessions.json"), SessionReuse: true, Env: append(os.Environ(), "TEST_LOG="+log), EnvAllowlist: []string{"TEST_LOG"}}
			provider, err := NewProvider(config)
			if err != nil {
				t.Fatal(err)
			}
			loaded := 0
			run := RunContext{RunID: "44444444-4444-4444-8444-444444444444", AgentID: "55555555-5555-4555-8555-555555555555", Authority: &openlinker.RuntimeAuthorityContext{PrincipalScopeID: "scope"}, Input: "write report", Conversation: &ConversationContext{SessionKey: "same-conversation"}, Emit: func(kind string, _ any) error {
				if kind == "run.skill_packages.loaded" {
					loaded++
				}
				return nil
			}}
			run.PackageSnapshot = testPackageSnapshot(name, "FIRST-PRIVATE-INSTRUCTION")
			if _, err := provider.Run(context.Background(), run); err != nil {
				t.Fatal(err)
			}
			run.PackageSnapshot = testPackageSnapshot(name, "SECOND-PRIVATE-INSTRUCTION")
			if _, err := provider.Run(context.Background(), run); err != nil {
				t.Fatal(err)
			}
			if _, err := provider.Run(context.Background(), run); err != nil {
				t.Fatal(err)
			}
			if loaded != 3 {
				t.Fatalf("loaded receipts=%d", loaded)
			}
			promptFile := filepath.Join(dir, "prompts")
			if name == "codex" {
				promptFile = log + ".requests"
			}
			prompts, err := os.ReadFile(promptFile)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"FIRST-PRIVATE-INSTRUCTION", "SECOND-PRIVATE-INSTRUCTION", ".openlinker-skills", "references/example.txt"} {
				if !strings.Contains(string(prompts), want) {
					t.Fatalf("provider process did not receive %q", want)
				}
			}
			for _, marker := range []string{"FIRST-PRIVATE-INSTRUCTION", "SECOND-PRIVATE-INSTRUCTION"} {
				if strings.Count(string(prompts), marker) != 1 {
					t.Fatalf("resumed session repeated package instructions: %s", marker)
				}
			}

			if name == "codex" {
				calls := prompts
				if strings.Count(string(calls), " thread/start\n") != 2 || strings.Count(string(calls), " thread/resume\n") != 1 {
					t.Fatalf("version change reused an incompatible session: %s", calls)
				}
			} else {
				arguments, _ := os.ReadFile(filepath.Join(dir, "arguments"))
				if strings.Count(string(arguments), "--resume") != 1 {
					t.Fatalf("version change reused an incompatible session: %s", arguments)
				}
			}
		})
	}
}

func TestSkillPackageMaterializationIsConcurrentAndConfined(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	files := map[string]string{"SKILL.md": "instructions", "refs/a.txt": "reference"}
	var wg sync.WaitGroup
	failures := make(chan error, 16)
	for range 16 {
		wg.Add(1)
		go func() { defer wg.Done(); failures <- materializeSkillPackage(root, "packages/version", files) }()
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(filepath.Join(dir, "packages/version/SKILL.md"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "packages/version/SKILL.md"), []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := materializeSkillPackage(root, "packages/version", files); err == nil {
		t.Fatal("silently overwrote tampered package")
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "escape")); err != nil {
		t.Skip(err)
	}
	if err := materializeSkillPackage(root, "escape/version", files); err == nil {
		t.Fatal("followed a symlink out of the workspace")
	}
	entries, _ := os.ReadDir(outside)
	if len(entries) != 0 {
		t.Fatal("wrote outside workspace")
	}
}

func TestSkillPackageValidationAndMissingDependency(t *testing.T) {
	snapshot := testPackageSnapshot("codex", "test")
	snapshot.Bundles[0].Payload += " "
	if _, _, err := decodePackageSnapshot(snapshot, "codex"); err == nil {
		t.Fatal("accepted changed contents")
	}
	snapshot = testPackageSnapshot("codex", "test")
	if _, _, err := decodePackageSnapshot(snapshot, "claude"); err == nil {
		t.Fatal("accepted incompatible provider")
	}
	var contents packageContents
	_ = json.Unmarshal([]byte(snapshot.Bundles[0].Payload), &contents)
	contents.RequiredCommands = []string{"openlinker-nonexistent-command-for-test"}
	payload, _ := json.Marshal(contents)
	digest := sha256.Sum256(payload)
	snapshot.Bundles[0].Payload = string(payload)
	snapshot.Bundles[0].Digest = hex.EncodeToString(digest[:])
	receipt := ""
	provider := skillPackageProvider{config: ProviderConfig{Provider: "codex", Workspace: t.TempDir()}}
	_, err := provider.Run(context.Background(), RunContext{AgentID: "55555555-5555-4555-8555-555555555555", Authority: &openlinker.RuntimeAuthorityContext{}, PackageSnapshot: snapshot, Emit: func(event string, payload any) error {
		raw, _ := json.Marshal(payload)
		receipt = event + string(raw)
		return nil
	}})
	if err == nil || !strings.Contains(receipt, "dependency_missing") {
		t.Fatalf("missing dependency did not fail before provider launch: %v %s", err, receipt)
	}
}

func TestSkillPackageCacheStaysOutOfGit(t *testing.T) {
	for _, nested := range []bool{false, true} {
		repo := t.TempDir()
		git := func(args ...string) string {
			t.Helper()
			out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput()
			if err != nil {
				t.Fatalf("git failed: %s %v", out, err)
			}
			return string(out)
		}
		git("init", "--quiet")
		workspace := repo
		if nested {
			workspace = filepath.Join(repo, "nested")
			if err := os.MkdirAll(workspace, 0700); err != nil {
				t.Fatal(err)
			}
		}
		if err := protectSkillPackageCache(context.Background(), workspace); err != nil {
			t.Fatal(err)
		}
		root, err := os.OpenRoot(workspace)
		if err != nil {
			t.Fatal(err)
		}
		err = materializeSkillPackage(root, ".openlinker-skills/agent/digest", map[string]string{"SKILL.md": "PRIVATE"})
		root.Close()
		if err != nil {
			t.Fatal(err)
		}
		git("add", "-A")
		if got := git("ls-files"); got != "" {
			t.Fatalf("private package entered index: %s", got)
		}
		if err := os.WriteFile(filepath.Join(repo, "user.txt"), []byte("user"), 0600); err != nil {
			t.Fatal(err)
		}
		git("add", "-A")
		if got := git("ls-files"); strings.TrimSpace(got) != "user.txt" {
			t.Fatalf("unrelated files affected: %s", got)
		}
	}
}

func TestSkillPackagesUnsupportedEnvironmentRejectsBeforeWritingOrProviderExecution(t *testing.T) {
	for _, providerName := range []string{"codex", "claude"} {
		for _, mode := range []string{"disabled", "launcher"} {
			t.Run(providerName+"/"+mode, func(t *testing.T) {
				workspace := t.TempDir()
				config := ProviderConfig{Provider: providerName, Workspace: workspace, DisableSkillPackages: mode == "disabled"}
				if mode == "launcher" {
					config.Bin = "/usr/local/bin/openlinker-provider-launcher"
				}
				handler, err := NewHandler(config)
				if err != nil {
					t.Fatal(err)
				}
				if features := handler.SkillPackageFeatures(); len(features) != 0 {
					t.Fatalf("advertised %v", features)
				}
				emitted := false
				_, err = handler.Provider.Run(context.Background(), RunContext{PackageSnapshot: testPackageSnapshot(providerName, "private"), Emit: func(string, any) error { emitted = true; return nil }})
				if !errors.Is(err, ErrSkillPackagesUnsupported) {
					t.Fatalf("unexpected result: %v", err)
				}
				if emitted {
					t.Fatal("unsupported host emitted load evidence")
				}
				entries, err := os.ReadDir(workspace)
				if err != nil || len(entries) != 0 {
					t.Fatalf("unsupported host wrote cache: %v %v", entries, err)
				}
			})
		}
	}
}
