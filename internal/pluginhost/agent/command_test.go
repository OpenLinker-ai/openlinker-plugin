package agent

import (
	"bytes"
	"encoding/json"
	"github.com/OpenLinker-ai/openlinker-plugin/internal/pluginhost/shared"
	agentapp "github.com/OpenLinker-ai/openlinker-plugin/packages/agent-adapters/agent"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestConfigureCommandStoresCodexBaseURL(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config", "agent.json")
	environment := map[string]string{"OPENLINKER_AGENT_CONFIG": configPath}
	var stdout bytes.Buffer
	command := newConfigureCommand(shared.IO{
		Getenv: func(key string) string { return environment[key] },
		Stdout: &stdout,
	})
	command.SetArgs([]string{
		"--provider", "codex",
		"--agent-id", "11111111-1111-4111-8111-111111111111",
		"--workspace", dir,
		"--codex-base-url", "https://router.example/v1",
	})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"codex_base_url": "https://router.example/v1"`) {
		t.Fatalf("stored config = %s", raw)
	}
}

func TestConfigureCommandPreservesOmittedFieldsAndExplicitFalseOrEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.json")
	configure := func(args ...string) {
		t.Helper()
		var stdout bytes.Buffer
		command := newConfigureCommand(shared.IO{
			Getenv: func(key string) string {
				if key == "OPENLINKER_AGENT_CONFIG" {
					return path
				}
				return ""
			},
			Stdout: &stdout,
		})
		command.SetArgs(args)
		if err := command.Execute(); err != nil {
			t.Fatal(err)
		}
		var response map[string]any
		if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if response["configured"] != true || response["secrets_written"] != false || response["config_path"] != path {
			t.Fatalf("configuration output changed: %s", stdout.Bytes())
		}
	}
	configure("--provider", "codex", "--agent-id", "11111111-1111-4111-8111-111111111111", "--workspace", dir,
		"--model", "old-model", "--session-reuse=false", "--web-search=true", "--enabled=true",
		"--capacity", "4", "--timeout", "42", "--transport", "pull", "--codex-sandbox", "workspace-write",
		"--claude-permission", "acceptEdits", "--codex-base-url", "https://router.example/v1")
	// No flags must not apply Cobra's defaults over an existing configuration.
	configure()
	configure("--web-search=false", "--enabled=false", "--model=", "--codex-base-url=")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var config agentapp.Config
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	if config.SessionReuse || config.WebSearch || config.Enabled || config.Model != "" || config.CodexBaseURL != "" ||
		config.Capacity != 4 || config.TimeoutSeconds != 42 || config.Transport != "pull" ||
		config.CodexSandbox != "workspace-write" || config.ClaudePermission != "acceptEdits" {
		t.Fatalf("explicit configuration patch lost values: %#v", config)
	}
}

func TestConfigureCommandRejectsExplicitInvalidFieldsWithoutWriting(t *testing.T) {
	for _, args := range [][]string{
		{"--capacity=0"}, {"--timeout=0"}, {"--provider="},
		{"--codex-sandbox", " read-only "}, {"--codex-approval", " never "},
		{"--claude-permission", " dontAsk "},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "agent.json")
			getenv := func(key string) string {
				if key == "OPENLINKER_AGENT_CONFIG" {
					return path
				}
				return ""
			}
			if _, _, err := agentapp.ConfigureNonSecret(getenv, agentapp.ConfigureOptions{
				Provider: "codex", AgentID: "11111111-1111-4111-8111-111111111111", Workspace: dir,
			}); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var stdout bytes.Buffer
			command := newConfigureCommand(shared.IO{Getenv: getenv, Stdout: &stdout})
			command.SetArgs(args)
			if err := command.Execute(); err == nil {
				t.Fatal("invalid explicit field was ignored")
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("invalid configuration changed persisted state")
			}
		})
	}
}

func TestConfigureCommandPersistsCompleteBrowserProfile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.json")
	var stdout bytes.Buffer
	command := newConfigureCommand(shared.IO{
		Getenv: func(key string) string {
			if key == "OPENLINKER_AGENT_CONFIG" {
				return path
			}
			return ""
		},
		Stdout: &stdout,
	})
	command.SetArgs([]string{
		"--provider", "CLAUDE", "--agent-id", "11111111-1111-4111-8111-111111111111",
		"--workspace", dir, "--url", "https://core.example", "--state-dir", filepath.Join(dir, "state"),
		"--provider-bin", "custom-provider", "--model", "test-model", "--transport", "PULL",
		"--capacity", "1", "--timeout", "90", "--session-reuse=true", "--web-search=true",
		"--codex-base-url", "https://router.example/v1", "--codex-sandbox", "workspace-write",
		"--codex-approval", "on-request", "--claude-permission", "acceptEdits",
		"--allowed-tool", "Read", "--allowed-tool", "Bash", "--execution-profile", "BROWSER",
		"--browser-interaction-policy", "FULL", "--browser-client-mode", "ISOLATED-NATIVE",
		"--browser-plugin-bin", "custom-openlinker", "--browser-native-plugin", filepath.Join(dir, "native"),
		"--browser-socket", filepath.Join(dir, "socket"), "--browser-credential-file", filepath.Join(dir, "credential"),
		"--browser-lease-root", filepath.Join(dir, "leases"), "--browser-broker-root", filepath.Join(dir, "broker"),
		"--enabled=true",
	})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var config agentapp.Config
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	want := agentapp.Config{
		Version: 1, Enabled: true, Provider: "claude", AgentID: "11111111-1111-4111-8111-111111111111",
		Workspace: dir, OpenLinkerURL: "https://core.example", StateDir: filepath.Join(dir, "state"),
		ProviderBin: "custom-provider", Model: "test-model", Transport: "pull", Capacity: 1, TimeoutSeconds: 90,
		SessionReuse: true, WebSearch: true, CodexBaseURL: "https://router.example/v1", CodexSandbox: "workspace-write",
		CodexApproval: "on-request", ClaudePermission: "acceptEdits", AllowedTools: []string{"Read", "Bash"},
		ExecutionProfile: "browser", BrowserInteractionPolicy: "full", BrowserClientMode: "isolated-native",
		BrowserPluginBin: "custom-openlinker", BrowserNativePlugin: filepath.Join(dir, "native"),
		BrowserSocket: filepath.Join(dir, "socket"), BrowserCredentialFile: filepath.Join(dir, "credential"),
		BrowserLeaseRoot: filepath.Join(dir, "leases"), BrowserBrokerRoot: filepath.Join(dir, "broker"),
	}
	if !reflect.DeepEqual(config, want) {
		t.Fatalf("Browser configuration = %#v, want %#v", config, want)
	}
}

func TestConfigureDelegationTargetsPreserveAndClear(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "agent.json")
	configure := func(args ...string) error {
		command := newConfigureCommand(shared.IO{Getenv: func(key string) string {
			if key == "OPENLINKER_AGENT_CONFIG" {
				return path
			}
			return ""
		}, Stdout: &bytes.Buffer{}})
		command.SetArgs(args)
		return command.Execute()
	}
	if err := configure("--provider", "codex", "--agent-id", "11111111-1111-4111-8111-111111111111", "--workspace", directory, "--delegation-target", "22222222-2222-4222-8222-222222222222"); err != nil {
		t.Fatal(err)
	}
	if err := configure("--model", "test-model"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	var config agentapp.Config
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	if len(config.DelegationTargets) != 1 {
		t.Fatal("omitted flag cleared targets")
	}
	before := string(raw)
	if err := configure("--delegation-target", "11111111-1111-4111-8111-111111111111"); err == nil {
		t.Fatal("self delegation accepted")
	}
	raw, _ = os.ReadFile(path)
	if string(raw) != before {
		t.Fatal("invalid targets changed stored config")
	}
	if err := configure("--delegation-target="); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(path)
	if strings.Contains(string(raw), "delegation_targets") {
		t.Fatal("empty flag did not disable delegation")
	}
}
