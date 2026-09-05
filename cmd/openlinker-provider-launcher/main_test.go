//go:build linux

package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
)

func TestFixedProviderBinary(t *testing.T) {
	for provider, want := range map[string]string{
		"codex":  "/usr/local/bin/codex",
		"CLAUDE": "/usr/local/bin/claude",
	} {
		got, err := fixedProviderBinary(provider)
		if err != nil || got != want {
			t.Fatalf("fixedProviderBinary(%q) = %q, %v", provider, got, err)
		}
	}
	if _, err := fixedProviderBinary("shell"); err == nil {
		t.Fatal("expected unfixed Provider rejection")
	}
}

func TestCodexPluginPreparationNeverStartsProviderAuthProxy(t *testing.T) {
	for _, args := range [][]string{
		{"plugin", "list", "--json"},
		{"plugin", "marketplace", "add", "/opt/plugin"},
		{"mcp", "list", "--json"},
		{"doctor"},
		{"--version"},
	} {
		if codexCommandNeedsAuthProxy(args) {
			t.Fatalf("management command requires auth proxy: %#v", args)
		}
	}
	for _, args := range [][]string{
		nil,
		{"exec", "-"},
		{"-c", `model_provider="openlinker_proxy"`, "exec", "-"},
	} {
		if !codexCommandNeedsAuthProxy(args) {
			t.Fatalf("model command skipped auth proxy: %#v", args)
		}
	}
}

func TestProviderPrivilegeDropBlocksAgentState(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("container-build root is required for the privilege boundary integration")
	}
	agentState := t.TempDir()
	secretPath := filepath.Join(agentState, "agent-secret")
	if err := os.WriteFile(secretPath, []byte("must-not-be-readable"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(agentState, runtimeUID, runtimeGID); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(secretPath, runtimeUID, runtimeGID); err != nil {
		t.Fatal(err)
	}
	helperBinary := copyTestExecutable(t)
	command := exec.Command(helperBinary, "-test.run=^TestProviderPrivilegeDropHelper$")
	command.Env = append(os.Environ(), "OPENLINKER_PROVIDER_DROP_HELPER=1", "OPENLINKER_PROVIDER_DROP_SECRET="+secretPath)
	command.SysProcAttr = &syscall.SysProcAttr{
		Credential:  &syscall.Credential{Uid: runtimeUID, Gid: runtimeGID, NoSetGroups: true},
		AmbientCaps: []uintptr{6, 7},
	}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("privilege helper failed: %v: %s", err, output)
	}
	text := string(output)
	for _, want := range []string{"uid=10002", "gid=10002", "caps=0000000000000000", "secret=blocked"} {
		if !strings.Contains(text, want) {
			t.Fatalf("privilege helper missing %q: %s", want, text)
		}
	}
}

func copyTestExecutable(t *testing.T) string {
	t.Helper()
	source, err := os.Open(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	destination, err := os.CreateTemp("/tmp", "openlinker-provider-drop-test-*")
	if err != nil {
		t.Fatal(err)
	}
	path := destination.Name()
	t.Cleanup(func() { _ = os.Remove(path) })
	if _, err := io.Copy(destination, source); err != nil {
		_ = destination.Close()
		t.Fatal(err)
	}
	if err := destination.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestProviderPrivilegeDropHelper(t *testing.T) {
	if os.Getenv("OPENLINKER_PROVIDER_DROP_HELPER") != "1" {
		t.Skip("helper subprocess only")
	}
	runtime.LockOSThread()
	if err := dropProviderPrivileges(); err != nil {
		t.Fatal(err)
	}
	caps := "missing"
	// Capabilities are per-thread. The launcher locks this goroutine to the OS
	// thread that will call exec, so inspect that thread instead of the thread
	// group leader exposed by /proc/self/status.
	if status, err := os.ReadFile("/proc/thread-self/status"); err == nil {
		for _, line := range strings.Split(string(status), "\n") {
			if strings.HasPrefix(line, "CapEff:") {
				caps = strings.TrimSpace(strings.TrimPrefix(line, "CapEff:"))
			}
		}
	}
	secretState := "blocked"
	if raw, err := os.ReadFile(os.Getenv("OPENLINKER_PROVIDER_DROP_SECRET")); err == nil {
		secretState = "leaked:" + string(raw)
	}
	fmt.Printf("uid=%d gid=%d caps=%s secret=%s\n", os.Geteuid(), os.Getegid(), caps, secretState)
}
