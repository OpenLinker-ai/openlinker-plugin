package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/agenthost"
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

func TestDoctorAndWorkerStartupRequireClaudeBareAPIKey(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX CLI/host fixtures")
	}
	for _, profile := range []struct {
		name, provider, browserMode string
		delegation, required        bool
	}{
		{"claude-standard", "claude", "", false, false},
		{"claude-delegation", "claude", "", true, true},
		{"claude-browser-mcp", "claude", "mcp", false, true},
		{"claude-browser-native", "claude", "native", false, true},
		{"claude-browser-mcp-delegation", "claude", "mcp", true, true},
		{"codex-standard", "codex", "", false, false},
		{"codex-delegation", "codex", "", true, false},
		{"codex-browser-mcp", "codex", "mcp", false, false},
		{"codex-browser-native", "codex", "native", false, false},
	} {
		for _, source := range []string{"absent", "whitespace", "environment", "file", "invalid-file", "conflict"} {
			t.Run(profile.name+"/"+source, func(t *testing.T) {
				getenv, environment := authPreflightFixture(t, profile.provider, profile.browserMode, profile.delegation)
				key, keyFile := "CODEX_API_KEY", "CODEX_API_KEY_FILE"
				if profile.provider == "claude" {
					key, keyFile = "ANTHROPIC_API_KEY", "ANTHROPIC_API_KEY_FILE"
				}
				const secret = "test-provider-key-must-not-appear-in-diagnostics"
				switch source {
				case "whitespace":
					environment[key] = " \t\n"
				case "environment":
					environment[key] = secret
				case "file", "invalid-file", "conflict":
					path := filepath.Join(t.TempDir(), "provider-key")
					mode := os.FileMode(0o600)
					if source == "invalid-file" {
						mode = 0o644
					}
					if err := os.WriteFile(path, []byte(secret+"\n"), mode); err != nil {
						t.Fatal(err)
					}
					if err := os.Chmod(path, mode); err != nil {
						t.Fatal(err)
					}
					environment[keyFile] = path
					if source == "conflict" {
						environment[key] = secret
					}
				}
				missing := source == "absent" || source == "whitespace"
				wantOK := !(profile.required && missing) && source != "invalid-file" && source != "conflict"
				diagnostic := Diagnose(getenv, "")
				if diagnostic.OK != wantOK {
					t.Errorf("doctor OK=%v, want %v; checks=%v", diagnostic.OK, wantOK, diagnostic.Checks)
				}
				for name, status := range diagnostic.Checks {
					if name != "provider_auth" && (strings.Contains(status, "invalid") || strings.Contains(status, "incompatible")) {
						t.Fatalf("unrelated preflight failure %s=%s", name, status)
					}
				}
				if profile.required && missing {
					if diagnostic.Checks["provider_auth"] != "missing" || !strings.Contains(diagnostic.Checks["provider_auth_detail"], "ANTHROPIC_API_KEY") {
						t.Errorf("missing actionable --bare credential diagnostic: %#v", diagnostic)
					}
				} else if wantOK {
					wantSource := source
					if missing {
						wantSource = "absent"
					}
					if diagnostic.Checks["provider_auth"] != wantSource {
						t.Errorf("provider_auth=%q, want %q", diagnostic.Checks["provider_auth"], wantSource)
					}
				}
				resolved, err := resolveRuntime(getenv, "", "test")
				if err == nil {
					defer resolved.workerLock.release()
				}
				if (err == nil) != wantOK {
					t.Errorf("Worker startup error=%v, wantOK=%v", err, wantOK)
				}
				if profile.required && missing && (err == nil || !strings.Contains(err.Error(), "ANTHROPIC_API_KEY")) {
					t.Errorf("Worker did not reject missing --bare authentication: %v", err)
				}
				raw, _ := json.Marshal(diagnostic)
				if strings.Contains(string(raw), secret) || err != nil && strings.Contains(err.Error(), secret) {
					t.Fatal("preflight leaked the credential")
				}
			})
		}
	}
}

// All non-authentication checks pass, so a missing key is the sole cause of
// rejection. The fixture runs only version/help/capability probes, never a model.
func authPreflightFixture(t *testing.T, provider, browserMode string, delegation bool) (func(string) string, map[string]string) {
	t.Helper()
	directory := t.TempDir()
	bin := filepath.Join(directory, provider)
	version := "codex-cli 0.153.0"
	if provider == "claude" {
		version = "2.1.259 (Claude Code)"
	}
	help := "app-server generate-json-schema --listen --config --disable --safe-mode --bare --no-chrome --disable-slash-commands --permission-mode --resume stream-json --verbose --include-partial-messages --strict-mcp-config"
	capabilities, err := json.Marshal(agenthost.SupportedCapabilities())
	if err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf("#!/bin/sh\ncase \"$*\" in\n--version) echo '%s';;\n'--help'|'app-server --help') echo '%s';;\n'plugin capabilities') echo '%s';;\n*) exit 99;;\nesac\n", version, help, capabilities)
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	config := defaultConfig()
	config.Provider, config.ProviderBin = provider, bin
	config.AgentID = "11111111-1111-4111-8111-111111111111"
	config.OpenLinkerURL, config.Workspace = "https://runtime.example.test", directory
	config.StateDir = filepath.Join(directory, "state")
	if browserMode != "" {
		config.ExecutionProfile, config.BrowserClientMode = "browser", browserMode
		config.BrowserPluginBin, config.BrowserNativePlugin = bin, directory
		config.BrowserSocket = filepath.Join(directory, "browser.sock")
		config.BrowserCredentialFile = filepath.Join(directory, "browser-credential")
		config.BrowserLeaseRoot, config.BrowserBrokerRoot = directory, directory
		if err := os.WriteFile(config.BrowserCredentialFile, []byte("test-browser-credential"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(directory, "agent.json")
	if err := saveConfig(path, config); err != nil {
		t.Fatal(err)
	}
	environment := map[string]string{
		"OPENLINKER_AGENT_CONFIG": path,
		"OPENLINKER_AGENT_TOKEN":  "test-worker-token",
		// Exercise the launcher-supplied effective selection without running a
		// separate native plugin activation during the auth-only startup check.
		"OPENLINKER_BROWSER_CLIENT_MODE_EFFECTIVE": browserMode,
	}
	if delegation {
		// Exercise environment overrides as well as the persisted Browser profile.
		environment["OPENLINKER_AGENT_DELEGATION_TARGETS"] = "22222222-2222-4222-8222-222222222222"
		environment["OPENLINKER_AGENT_DELEGATION_PROXY_BIN"] = bin
	}
	return func(key string) string { return environment[key] }, environment
}
