package agentexec

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/skillpackages"
	openlinker "github.com/OpenLinker-ai/openlinker-go"
)

type packageSnapshot = skillpackages.Snapshot
type packageVersion = skillpackages.Version
type packageContents = skillpackages.Contents

var decodePackageSnapshot = skillpackages.Decode

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
			run.PackageSnapshot = nil
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
				if strings.Count(string(calls), " thread/start\n") != 3 || strings.Count(string(calls), " thread/resume\n") != 1 {
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

func TestProviderSkillRecoveryStartsFreshSession(t *testing.T) {
	for _, name := range []string{"codex", "claude"} {
		t.Run(name, func(t *testing.T) {
			workspace := t.TempDir()
			var bin string
			if name == "codex" {
				bin = filepath.Join(t.TempDir(), "codex")
				writeCodexRPCFixture(t, bin, "ephemeral")
			} else {
				bin, _ = reviewFakeCLI(t, `cat >/dev/null
printf '%s\n' '{"type":"result","subtype":"success","result":"ok","session_id":"11111111-aaaa-4111-8111-111111111111"}'
`)
			}
			config := ProviderConfig{Provider: name, Bin: bin, Workspace: workspace, SessionStore: filepath.Join(workspace, "sessions.json"), SessionReuse: true}
			provider, err := NewProvider(config)
			if err != nil {
				t.Fatal(err)
			}
			snapshot := testPackageSnapshot(name, "PINNED-INSTRUCTION")
			run := RunContext{AgentID: "55555555-5555-4555-8555-555555555555", Authority: &openlinker.RuntimeAuthorityContext{PrincipalScopeID: "scope"}, Conversation: &ConversationContext{SessionKey: "conversation"}, PackageSnapshot: snapshot, Emit: func(string, any) error { return nil }}
			if _, err := provider.Run(context.Background(), run); err != nil {
				t.Fatal(err)
			}
			original := filepath.Join(workspace, ".openlinker-skills", run.AgentID, snapshot.Bundles[0].Digest, "SKILL.md")
			if err := os.Chmod(original, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(original, []byte("TAMPERED"), 0600); err != nil {
				t.Fatal(err)
			}
			for i, wantResume := range []bool{false, true} {
				result, err := provider.Run(context.Background(), run)
				if err != nil {
					t.Fatal(err)
				}
				out := result.Output.(map[string]any)
				if got := out[name+"_session_resumed"]; got != wantResume {
					t.Fatalf("attempt %d resumed=%v want=%v", i, got, wantResume)
				}
			}
		})
	}
}
