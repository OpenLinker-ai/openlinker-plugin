package agent

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestSharedAppFileProductionConsumers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "private", "config.json")
	value := struct {
		Value int `json:"value"`
	}{Value: 7}
	if err := writePrivateJSON(path, value); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != "{\n  \"value\": 7\n}\n" {
		t.Fatalf("app writer changed file format: %q, %v", raw, err)
	}
	var decoded struct {
		Value int `json:"value"`
	}
	if err := decodeStrictJSON(raw, &decoded); err != nil || decoded.Value != value.Value {
		t.Fatalf("app strict decoder failed: %v", err)
	}
	if err := decodeStrictJSON([]byte(`{"unknown":1}`), &decoded); err == nil {
		t.Fatal("app decoder silently accepted an unknown field")
	}
	if err := writePrivateJSON(path, func() {}); err == nil {
		t.Fatal("invalid JSON write unexpectedly succeeded")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(raw) {
		t.Fatal("failed write changed the existing app file")
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("private app-file permission changed: %v", err)
		}
		link := filepath.Join(dir, "link")
		if err := os.Symlink(path, link); err != nil {
			t.Fatal(err)
		}
		if err := writePrivateFile(link, []byte("replacement")); err == nil || err.Error() != "destination is not a regular file" {
			t.Fatalf("app symlink write was not rejected: %v", err)
		}
	}
	id := "12345678-1234-4234-8234-123456789abc"
	state := filepath.Join(dir, "identity")
	first, err := loadOrCreateNodeID(state, id)
	if err != nil || first != id {
		t.Fatalf("first app Node identity: %q, %v", first, err)
	}
	second, err := loadOrCreateNodeID(state, "")
	if err != nil || second != id {
		t.Fatal("app did not preserve its own Node identity path")
	}
	stored, err := os.ReadFile(filepath.Join(state, "node-id"))
	if err != nil || string(stored) != id+"\n" {
		t.Fatal("app Node identity filename or format changed")
	}
}

func TestSharedAppNodeIdentityProductionConsumer(t *testing.T) {
	state := filepath.Join(t.TempDir(), "identity")
	first, err := loadOrCreateNodeID(state, "")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(first) {
		t.Fatal("generated app Node identity is not a canonical lowercase UUID v4")
	}
	path := filepath.Join(state, "node-id")
	stored, err := os.ReadFile(path)
	if err != nil || string(stored) != first+"\n" {
		t.Fatalf("generated app Node identity was not persisted at its product path: %v", err)
	}
	for _, explicit := range []string{"", first} {
		second, err := loadOrCreateNodeID(state, explicit)
		if err != nil || second != first {
			t.Fatalf("persisted app Node identity was not reused: %v", err)
		}
	}
	conflict := "0" + first[1:]
	if first[0] == '0' {
		conflict = "1" + first[1:]
	}
	value, err := loadOrCreateNodeID(state, conflict)
	if err == nil || err.Error() != "OPENLINKER_NODE_ID does not match the persisted Node ID" || value != "" {
		t.Fatalf("conflicting app Node identity was not rejected: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(stored) {
		t.Fatal("reuse or a rejected identity conflict changed the persisted node-id bytes")
	}
	if recovered, err := loadOrCreateNodeID(state, ""); err != nil || recovered != first {
		t.Fatalf("app Node identity did not remain usable after a rejected conflict: %v", err)
	}
}

func TestSharedAppSecretProductionConsumer(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix private-mode semantics do not assert a new Windows DACL guarantee")
	}
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte(" \nfixture-only-secret\t"), 0o600); err != nil {
		t.Fatal(err)
	}
	getenv := func(key string) string {
		if key == "FIXTURE_SECRET_FILE" {
			return path
		}
		return ""
	}
	value, source, err := resolveSecret(getenv, "FIXTURE_SECRET", "FIXTURE_SECRET_FILE", true)
	if err != nil || value != "fixture-only-secret" || source != "file" {
		t.Fatalf("app secret resolver did not use the preserved file behavior: source=%q error=%v", source, err)
	}
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	value, _, err = resolveSecret(getenv, "FIXTURE_SECRET", "FIXTURE_SECRET_FILE", true)
	if err == nil || err.Error() != "FIXTURE_SECRET_FILE: secret file must not be accessible by group or other users" || value != "" {
		t.Fatal("app secret resolver changed the permission rejection or exposed a value")
	}
}

func TestSharedAppLockProductionConsumer(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	lock, err := acquireAgentModeLock(state)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.release()
	if info, err := os.Stat(filepath.Join(state, ".agent-mode.lock")); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("product lock path was not preserved: %v", err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, executable, "-test.run=^TestSharedAppLockConsumerChild$")
	child.Env = []string{"OPENLINKER_APPFILES_CONSUMER_STATE=" + state}
	if err := child.Run(); err == nil {
		t.Fatal("another app process acquired the held state lock")
	} else {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 23 {
			t.Fatalf("unexpected competing-app error: %v", err)
		}
	}
	if err := lock.release(); err != nil {
		t.Fatal(err)
	}
	child = exec.CommandContext(ctx, executable, "-test.run=^TestSharedAppLockConsumerChild$")
	child.Env = []string{"OPENLINKER_APPFILES_CONSUMER_STATE=" + state}
	if output, err := child.CombinedOutput(); err != nil || !strings.Contains(string(output), "PASS") {
		t.Fatalf("released app state lock was not reusable: %v", err)
	}
	if err := lock.release(); err != nil {
		t.Fatal(err)
	}
	if err := (*agentModeLock)(nil).release(); err != nil {
		t.Fatal(err)
	}
	if _, err := acquireAgentModeLock(""); err == nil {
		t.Fatal("empty stateDir must retain the old MkdirAll error")
	}
}

func TestSharedAppLockConsumerChild(t *testing.T) {
	state := os.Getenv("OPENLINKER_APPFILES_CONSUMER_STATE")
	if state == "" {
		return
	}
	lock, err := acquireAgentModeLock(state)
	if err != nil {
		if err.Error() == "Agent state is already serving another Runtime Worker" {
			os.Exit(23)
		}
		t.Fatal(err)
	}
	if err := lock.release(); err != nil {
		t.Fatal(err)
	}
}
