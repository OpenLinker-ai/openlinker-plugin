package agent

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSearchPolicyMatchesDoctorAndRuntimeConfiguration(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX provider probe fixture")
	}
	for _, provider := range []string{"codex", "claude"} {
		for _, test := range []struct {
			name, specific, generic, source string
			configured, effective           bool
		}{
			{"default-off", "", "", "configuration", false, false},
			{"config-on", "", "", "configuration", true, true},
			{"generic-on", "", "true", "OPENLINKER_AGENT_WEB_SEARCH", false, true},
			{"generic-off", "", "false", "OPENLINKER_AGENT_WEB_SEARCH", true, false},
			{"specific-off-wins", "false", "true", "specific", true, false},
			{"specific-on-wins", "true", "false", "specific", false, true},
			{"blank-falls-back", " \t", "true", "OPENLINKER_AGENT_WEB_SEARCH", false, true},
		} {
			t.Run(provider+"/"+test.name, func(t *testing.T) {
				get, env := authPreflightFixture(t, provider, "", false)
				name := "OPENLINKER_" + strings.ToUpper(provider) + "_WEB_SEARCH"
				env[name], env["OPENLINKER_AGENT_WEB_SEARCH"] = test.specific, test.generic
				config, _, err := ConfigureNonSecret(get, ConfigureOptions{WebSearch: &test.configured})
				if err != nil {
					t.Fatal(err)
				}
				wantSource := test.source
				if wantSource == "specific" {
					wantSource = name
				}
				want := WebSearchPolicy{Provider: provider, Configured: test.configured, Effective: test.effective, Source: wantSource, Overridden: test.configured != test.effective}
				if got := config.SearchPolicy(); got == nil || *got != want {
					t.Fatalf("configure policy=%+v, want %+v", got, want)
				}
				stored, _, err := loadConfig(get)
				if err != nil || stored.WebSearch != test.configured || stored.webSearchPolicy != nil {
					t.Fatal("runtime override leaked into persistent config", err)
				}
				diagnostic := Diagnose(get, "")
				if !diagnostic.OK || diagnostic.WebSearch == nil || *diagnostic.WebSearch != want {
					t.Fatalf("doctor policy mismatch: %+v", diagnostic)
				}
				resolved, err := resolveRuntime(get, "", "test")
				if err != nil {
					t.Fatal(err)
				}
				defer resolved.workerLock.release()
				if resolved.config.WebSearch != test.effective || *resolved.config.SearchPolicy() != want {
					t.Fatal("worker resolution disagrees with doctor/configure")
				}
			})
		}
	}
}

func TestSearchPolicyInvalidOverrideDoesNotWriteOrEchoValue(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		dir := t.TempDir()
		path := filepath.Join(dir, "agent.json")
		env := map[string]string{"OPENLINKER_AGENT_CONFIG": path}
		get := func(k string) string { return env[k] }
		_, _, err := ConfigureNonSecret(get, ConfigureOptions{Provider: provider, Workspace: dir, AgentID: "11111111-1111-4111-8111-111111111111"})
		if err != nil {
			t.Fatal(err)
		}
		before, _ := os.ReadFile(path)
		key := "OPENLINKER_" + strings.ToUpper(provider) + "_WEB_SEARCH"
		env[key] = "synthetic-private-invalid-value"
		enabled := true
		_, _, err = ConfigureNonSecret(get, ConfigureOptions{WebSearch: &enabled})
		if err == nil || !strings.Contains(err.Error(), key) || strings.Contains(err.Error(), env[key]) {
			t.Fatalf("invalid override diagnostic: %v", err)
		}
		after, _ := os.ReadFile(path)
		if !bytes.Equal(before, after) {
			t.Fatal("invalid search override changed config")
		}
	}
}

func TestStoredSearchPolicyIsLastWorkerSnapshot(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{"OPENLINKER_AGENT_CONFIG": filepath.Join(dir, "agent.json"), "OPENLINKER_AGENT_STATE_DIR": filepath.Join(dir, "state")}
	get := func(k string) string { return env[k] }
	config, _, err := ConfigureNonSecret(get, ConfigureOptions{Provider: "codex", Workspace: dir, AgentID: "11111111-1111-4111-8111-111111111111"})
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(get, nil, "test")
	service.setStatus(Status{State: "ready", StateDir: env["OPENLINKER_AGENT_STATE_DIR"], WebSearch: config.SearchPolicy()})
	copy := service.Status()
	copy.WebSearch.Effective = true
	if service.Status().WebSearch.Effective {
		t.Fatal("caller mutated live policy snapshot")
	}
	env["OPENLINKER_CODEX_WEB_SEARCH"] = "true"
	status, err := ReadStatus(get, NewService(get, nil, "test"))
	if err != nil || status.WebSearch == nil || status.WebSearch.Effective {
		t.Fatal("status relabeled old Worker with new environment", err)
	}
	// Old status files have no snapshot; absence must not claim disabled/enabled.
	if err := writePrivateJSON(filepath.Join(env["OPENLINKER_AGENT_STATE_DIR"], "status.json"), map[string]any{"state": "ready", "enabled": false, "updated_at": ""}); err != nil {
		t.Fatal(err)
	}
	status, err = ReadStatus(get, NewService(get, nil, "test"))
	if err != nil || status.WebSearch != nil {
		t.Fatal("old status must leave search unknown", err)
	}
	raw, _ := json.Marshal(status)
	if bytes.Contains(raw, []byte(`"web_search"`)) {
		t.Fatal("unknown snapshot serialized as known policy")
	}
}
