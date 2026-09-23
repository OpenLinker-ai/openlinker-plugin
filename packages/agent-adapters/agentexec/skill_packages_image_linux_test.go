//go:build linux

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
	"syscall"
	"testing"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/skillpackages"
	openlinker "github.com/OpenLinker-ai/openlinker-go"
)

// Run only in the disposable skill-packages-test image. This exercises the
// production loader, both provider protocols and the real UID-switch launcher,
// with synthetic provider programs and no network/model credentials.
func TestSkillPackagesProviderImage(t *testing.T) {
	if os.Getenv("OPENLINKER_TEST_PROVIDER_IMAGE") != "1" {
		t.Skip("requires disposable Provider image fixture")
	}
	if os.Geteuid() == 0 {
		command := exec.Command(os.Args[0], "-test.run=^TestSkillPackagesProviderImage$", "-test.v")
		command.Env = os.Environ()
		command.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 10001, Gid: 10001, Groups: []uint32{10003}}, AmbientCaps: []uintptr{6, 7}}
		out, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("Runtime child: %v: %s", err, out)
		}
		t.Log(string(out))
		return
	}
	syscall.Umask(0077)
	if os.Geteuid() != 10001 {
		t.Fatal("not running as Runtime UID")
	}
	if os.Getenv("OPENLINKER_TEST_READONLY_SKILL_CACHE") == "1" {
		for _, name := range []string{"codex", "claude"} {
			handler, err := NewHandler(ProviderConfig{Provider: name, Bin: "/usr/local/bin/openlinker-provider-launcher-" + name, SkillPackageCache: skillpackages.Cache{Directory: "/skills", GroupID: 10003}})
			if err != nil {
				t.Fatal(err)
			}
			if len(handler.SkillPackageFeatures()) != 0 {
				t.Fatal("read-only image layer advertised writable skill cache")
			}
			if _, err := handler.Provider.Run(context.Background(), RunContext{PackageSnapshot: testPackageSnapshot(name, "test")}); !errors.Is(err, ErrSkillPackagesUnsupported) {
				t.Fatal("read-only cache did not reject before launching Provider", err)
			}
		}
		return
	}
	info, err := os.Stat("/workspace")
	if err != nil || info.Mode().Perm() != 0555 {
		t.Fatal("fixture workspace must be read-only")
	}
	if err := os.WriteFile("/runtime/skill-test-secret", []byte("PRIVATE-RUNTIME-STATE"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("/runtime/skill-host-only", []byte("not executed"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENLINKER_CODEX_RPC_LAUNCHER", "1")
	for _, name := range []string{"codex", "claude"} {
		t.Run(name, func(t *testing.T) {
			config := ProviderConfig{Provider: name, Bin: "/usr/local/bin/openlinker-provider-launcher-" + name, Workspace: "/workspace", SkillPackageCache: skillpackages.Cache{Directory: "/skills", GroupID: 10003}, Env: []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=/provider", "CODEX_HOME=/provider", "CLAUDE_CONFIG_DIR=/provider", "OPENLINKER_CODEX_RPC_LAUNCHER=1"}}
			handler, err := NewHandler(config)
			if err != nil {
				t.Fatal(err)
			}
			if len(handler.SkillPackageFeatures()) != 2 {
				t.Fatal("properly provisioned image failed to advertise skills")
			}
			loaded := false
			run := RunContext{AgentID: "55555555-5555-4555-8555-555555555555", Authority: &openlinker.RuntimeAuthorityContext{PrincipalScopeID: "test-owner"}, PackageSnapshot: testPackageSnapshot(name, "IMAGE-PRIVATE-INSTRUCTION"), Emit: func(kind string, _ any) error {
				if kind == "run.skill_packages.loaded" {
					loaded = true
				}
				return nil
			}}
			result, err := handler.Provider.Run(context.Background(), run)
			if err != nil {
				t.Fatal(err)
			}
			out, ok := result.Output.(map[string]any)
			if !ok || !loaded || !strings.Contains(out["summary"].(string), "skill-files-readable-state-private") {
				t.Fatalf("image provider did not read files: %#v", result)
			}
			checkSkillFilesServerAsProvider(t, name, run.AgentID)
			// The fixture prepared an executable symlink in the Provider's
			// private HOME. Runtime cannot traverse it, while Provider can.
			if _, err := os.Stat("/provider/skill-provider-only"); !errors.Is(err, os.ErrPermission) {
				t.Fatal("Runtime unexpectedly sees Provider-only command", err)
			}
			config.Env = append(config.Env, "PATH=/runtime:/provider:/usr/local/bin:/usr/bin:/bin")
			handler, err = NewHandler(config)
			if err != nil {
				t.Fatal(err)
			}
			for _, command := range []string{"skill-provider-only", "skill-host-only"} {
				snapshot := testPackageSnapshot(name, "IMAGE-PRIVATE-INSTRUCTION")
				var contents packageContents
				if err := json.Unmarshal([]byte(snapshot.Bundles[0].Payload), &contents); err != nil {
					t.Fatal(err)
				}
				contents.RequiredCommands = []string{command}
				raw, _ := json.Marshal(contents)
				digest := sha256.Sum256(raw)
				snapshot.Bundles[0].Payload = string(raw)
				snapshot.Bundles[0].Digest = hex.EncodeToString(digest[:])
				run.PackageSnapshot = snapshot
				_, err := handler.Provider.Run(context.Background(), run)
				if command == "skill-provider-only" && err != nil {
					t.Fatal("Provider-visible command rejected", err)
				}
				if command == "skill-host-only" && err == nil {
					t.Fatal("Runtime-only command accepted for Provider")
				}
			}
		})
	}
	if _, err := os.Stat("/workspace/.openlinker-skills"); !os.IsNotExist(err) {
		t.Fatal("image polluted user workspace")
	}
}

// Browser entries have no shell, so Codex/Claude spawn the Host's read-only
// package server as the Provider identity. Exercise that exact identity.
func checkSkillFilesServerAsProvider(t *testing.T, provider, agentID string) {
	t.Helper()
	roots, err := filepath.Glob(filepath.Join("/skills", agentID, "*"))
	if err != nil || len(roots) == 0 {
		t.Fatalf("no materialized package directory: %v %v", roots, err)
	}
	root := roots[0]
	request := func(id int, path string) string {
		raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/call", "params": map[string]any{"name": "read_skill_file", "arguments": map[string]any{"path": path}}})
		return string(raw)
	}
	args := []string{"plugin", "skill-files", "--host", provider, "--root", root}
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err == nil && entry.Type().IsRegular() {
			args = append(args, "--file", path)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("/usr/local/bin/openlinker-plugin-host", args...)
	command.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=/provider"}
	command.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 10002, Gid: 10002, Groups: []uint32{10003}}}
	command.Stdin = strings.NewReader(strings.Join([]string{
		request(1, filepath.Join(root, "references", "example.txt")),
		request(2, "/runtime/skill-test-secret"),
		request(3, filepath.Join(root, "..", "..", "..", "runtime", "skill-test-secret")),
	}, "\n") + "\n")
	out, err := command.Output()
	if err != nil {
		t.Fatalf("package file server as Provider: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 3 || !strings.Contains(lines[0], "versioned reference") || strings.Contains(lines[0], `"isError":true`) {
		t.Fatalf("Provider could not read the pinned supporting file: %s", out)
	}
	for _, line := range lines[1:] {
		if strings.Contains(line, "PRIVATE-RUNTIME-STATE") || !strings.Contains(line, `"isError":true`) {
			t.Fatalf("package file server exposed Runtime state: %s", line)
		}
	}
}
