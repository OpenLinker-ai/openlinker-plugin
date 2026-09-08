package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDoctorAndWorkerStartupRejectMissingCodexAppServer(t *testing.T) {
	directory := t.TempDir()
	bin := filepath.Join(directory, "codex")
	script := "#!/bin/sh\ncase \"$*\" in\n--version) echo 'codex-cli 0.153.0';;\n'app-server --help') exit 2;;\n*) exit 99;;\nesac\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	environment := map[string]string{
		"OPENLINKER_AGENT_CONFIG":    filepath.Join(directory, "agent.json"),
		"OPENLINKER_PROVIDER":        "codex",
		"OPENLINKER_CODEX_BIN":       bin,
		"OPENLINKER_WORKSPACE":       directory,
		"OPENLINKER_AGENT_STATE_DIR": filepath.Join(directory, "state"),
		"OPENLINKER_URL":             "https://runtime.example.test",
		"OPENLINKER_AGENT_ID":        "11111111-1111-4111-8111-111111111111",
		"OPENLINKER_AGENT_TOKEN":     "test-worker-token",
	}
	getenv := func(key string) string { return environment[key] }
	diagnostic := Diagnose(getenv, "codex")
	if diagnostic.OK || diagnostic.Checks["provider_cli"] != "incompatible_or_missing" || !strings.Contains(diagnostic.Checks["provider_cli_detail"], "app-server") {
		t.Fatalf("doctor accepted unavailable app-server: %#v", diagnostic)
	}
	_, err := resolveRuntime(getenv, "codex", "test")
	if err == nil || !strings.Contains(err.Error(), "app-server") {
		t.Fatalf("Worker startup bypassed app-server probe: %v", err)
	}
}
