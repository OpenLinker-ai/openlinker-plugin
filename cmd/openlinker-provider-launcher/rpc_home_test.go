//go:build linux

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/OpenLinker-ai/openlinker-plugin/packages/agent-adapters/codexhome"
)

func TestRPCHomePreparedUnderProviderIdentity(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires disposable root Linux test container")
	}
	root, err := os.MkdirTemp("/tmp", "openlinker-rpc-launcher-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "provider")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(source, providerUID, providerGID); err != nil {
		t.Fatal(err)
	}
	auth := filepath.Join(source, "auth.json")
	if err := os.WriteFile(auth, []byte("test native authentication"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(auth, providerUID, providerGID); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=TestRPCHomeLauncherHelper")
	command.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: providerUID, Gid: providerGID}}
	command.Env = []string{"TEST_RPC_LAUNCHER=1", "HOME=" + source, "CODEX_HOME=" + source, codexhome.PrepareEnvironment + "=1"}
	raw, err := command.CombinedOutput()
	if err != nil || !strings.Contains(string(raw), "provider-private-home-ok") {
		t.Fatalf("Provider UID home preparation failed: %s %v", raw, err)
	}
	entries, err := os.ReadDir(filepath.Join(source, "openlinker-rpc-v1"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "attempt-") {
			t.Fatal("normal launcher exit leaked private home")
		}
	}
}
func TestRPCHomeLauncherHelper(t *testing.T) {
	if os.Getenv("TEST_RPC_LAUNCHER") != "1" {
		return
	}
	if os.Geteuid() != providerUID {
		os.Exit(2)
	}
	code := runIsolatedCodex(os.Args[0], []string{"-test.run=TestRPCHomeNativeHelper"}, os.Environ())
	os.Exit(code)
}
func TestRPCHomeNativeHelper(t *testing.T) {
	if os.Getenv("TEST_RPC_LAUNCHER") != "1" {
		return
	}
	home := os.Getenv("CODEX_HOME")
	if os.Geteuid() != providerUID || !strings.Contains(home, "/attempt-") || os.Getenv(codexhome.PrepareEnvironment) != "" {
		os.Exit(2)
	}
	info, err := os.Stat(home)
	if err != nil || info.Mode().Perm() != 0o700 || info.Sys().(*syscall.Stat_t).Uid != providerUID {
		os.Exit(2)
	}
	if raw, err := os.ReadFile(filepath.Join(home, "auth.json")); err != nil || string(raw) != "test native authentication" {
		os.Exit(2)
	}
	_, _ = os.Stdout.WriteString("provider-private-home-ok\n")
	os.Exit(0)
}
