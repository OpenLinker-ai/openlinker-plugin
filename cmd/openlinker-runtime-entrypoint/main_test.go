package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveNodeIDGeneratesPersistsAndRejectsMismatch(t *testing.T) {
	runtimeDir := t.TempDir()
	if err := os.Chmod(runtimeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	generated, err := resolveNodeID(runtimeDir, "")
	if err != nil {
		t.Fatal(err)
	}
	if !canonicalUUID.MatchString(generated) || generated[14] != '4' {
		t.Fatalf("generated Node ID = %q", generated)
	}
	persisted, err := resolveNodeID(runtimeDir, generated)
	if err != nil || persisted != generated {
		t.Fatalf("persisted Node ID = %q, %v", persisted, err)
	}
	info, err := os.Stat(filepath.Join(runtimeDir, "node-id"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("node-id mode = %v, %v", info, err)
	}
	if _, err := resolveNodeID(runtimeDir, "11111111-1111-4111-8111-111111111111"); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("mismatch error = %v", err)
	}
}

func TestResolveRequiredSecretSupportsDirectAndFile(t *testing.T) {
	t.Setenv("CODEX_API_KEY", "codex-direct")
	value, err := resolveRequiredSecret("CODEX_API_KEY")
	if err != nil || value != "codex-direct" {
		t.Fatalf("direct secret = %q, %v", value, err)
	}

	t.Setenv("CODEX_API_KEY", "")
	path := filepath.Join(t.TempDir(), "codex-key")
	if err := os.WriteFile(path, []byte("codex-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_API_KEY_FILE", path)
	value, err = resolveRequiredSecret("CODEX_API_KEY")
	if err != nil || value != "codex-file" {
		t.Fatalf("file secret = %q, %v", value, err)
	}

	t.Setenv("CODEX_API_KEY", "codex-direct")
	if _, err := resolveRequiredSecret("CODEX_API_KEY"); err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("dual source error = %v", err)
	}
}

func TestAgentStageEnvironmentResolvesOnlySelectedProviderSecrets(t *testing.T) {
	for _, name := range []string{
		"OPENLINKER_AGENT_TOKEN", "OPENLINKER_AGENT_TOKEN_FILE",
		"CODEX_API_KEY", "CODEX_API_KEY_FILE",
		"ANTHROPIC_API_KEY", "ANTHROPIC_API_KEY_FILE",
		entrypointStageEnv,
	} {
		t.Setenv(name, "")
	}
	agentTokenFile := filepath.Join(t.TempDir(), "agent-token")
	if err := os.WriteFile(agentTokenFile, []byte("ol_agent_file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENLINKER_AGENT_TOKEN_FILE", agentTokenFile)
	t.Setenv("CODEX_API_KEY", "codex-direct")
	t.Setenv("ANTHROPIC_API_KEY", "must-not-pass")

	environment, err := agentStageEnvironment("codex")
	if err != nil {
		t.Fatal(err)
	}
	values := make(map[string]string, len(environment))
	for _, item := range environment {
		key, value, _ := strings.Cut(item, "=")
		values[key] = value
	}
	if values["OPENLINKER_AGENT_TOKEN"] != "ol_agent_file" || values["CODEX_API_KEY"] != "codex-direct" {
		t.Fatalf("resolved stage secrets = %#v", values)
	}
	for _, forbidden := range []string{"OPENLINKER_AGENT_TOKEN_FILE", "CODEX_API_KEY_FILE", "ANTHROPIC_API_KEY", "ANTHROPIC_API_KEY_FILE"} {
		if _, ok := values[forbidden]; ok {
			t.Fatalf("stage environment retained %s", forbidden)
		}
	}
	if values[entrypointStageEnv] != "1" {
		t.Fatalf("stage marker = %q", values[entrypointStageEnv])
	}
}

func TestBrowserPluginHostPreparationReceivesNoCredential(t *testing.T) {
	environment := browserClientHostEnvironment([]string{
		"HOME=/provider",
		"CODEX_HOME=/provider",
		"OPENLINKER_AGENT_TOKEN=agent-secret",
		"OPENLINKER_USER_TOKEN=user-secret",
		"CODEX_API_KEY=codex-secret",
		"ANTHROPIC_API_KEY=claude-secret",
	})
	joined := strings.Join(environment, "\n")
	for _, expected := range []string{"HOME=/provider", "CODEX_HOME=/provider"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("Plugin preparation environment omitted %s: %s", expected, joined)
		}
	}
	for _, forbidden := range []string{
		"agent-secret",
		"user-secret",
		"codex-secret",
		"claude-secret",
	} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf(
				"Plugin preparation environment retained a credential: %s",
				joined,
			)
		}
	}
}

func TestConfigureCodexUsesFixedRuntimeBoundaries(t *testing.T) {
	runtimeDir := t.TempDir()
	workspace := t.TempDir()
	if err := os.Chmod(runtimeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENLINKER_URL", "https://openlinker.example")
	t.Setenv("OPENLINKER_AGENT_ID", "11111111-1111-4111-8111-111111111111")
	t.Setenv("OPENLINKER_AGENT_TOKEN", "ol_agent_test")
	t.Setenv("CODEX_API_KEY", "codex-test")
	t.Setenv("ANTHROPIC_API_KEY", "must-not-be-selected")
	t.Setenv("OPENLINKER_NODE_ID", "")
	t.Setenv("OPENLINKER_BLOCK_PRIVATE_NETWORK", "true")
	t.Setenv("OPENLINKER_EGRESS_PROXY_URL", "http://gateway:3128")
	t.Setenv("OPENLINKER_CODEX_BASE_URL", "https://router.example/v1")
	if err := configure("codex", runtimeDir, workspace, false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if runtimeLock != nil {
			_ = runtimeLock.Close()
			runtimeLock = nil
		}
	})
	for key, want := range map[string]string{
		"OPENLINKER_PROVIDER":            "codex",
		"OPENLINKER_AGENT_STATE_DIR":     runtimeDir,
		"OPENLINKER_AGENT_CONFIG":        filepath.Join(runtimeDir, "agent.json"),
		"OPENLINKER_WORKSPACE":           workspace,
		"OPENLINKER_AGENT_SESSION_REUSE": "true",
		"OPENLINKER_CODEX_BIN":           "/usr/local/bin/openlinker-provider-launcher",
		"OPENLINKER_CODEX_BASE_URL":      "https://router.example/v1",
		"CODEX_API_KEY":                  "codex-test",
		"HTTP_PROXY":                     "http://gateway:3128",
		"NO_PROXY":                       "",
	} {
		if got := os.Getenv(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	if os.Getenv("OPENLINKER_NODE_ID") == "" {
		t.Fatal("entrypoint did not persist and export Node ID")
	}
}

func TestRuntimeAndWorkspaceValidationFailClosed(t *testing.T) {
	runtimeDir := t.TempDir()
	if err := os.Chmod(runtimeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := validateRuntimeDir(runtimeDir, false); err == nil || !strings.Contains(err.Error(), "owner-only") {
		t.Fatalf("runtime mode error = %v", err)
	}
	workspace := t.TempDir()
	if err := os.Chmod(workspace, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := validateWorkspace(workspace, false); err == nil || !strings.Contains(err.Error(), "group/world writable") {
		t.Fatalf("workspace mode error = %v", err)
	}
}

func TestURLValidationRejectsCredentialsAndNonHTTPS(t *testing.T) {
	for _, raw := range []string{"", "http://openlinker.example", "https://user:pass@openlinker.example", "https://openlinker.example?token=x"} {
		if _, err := validatePlatformURL(raw); err == nil {
			t.Errorf("expected platform URL rejection: %q", raw)
		}
	}
	if got, err := validatePlatformURL("https://openlinker.example/"); err != nil || got != "https://openlinker.example" {
		t.Fatalf("platform URL = %q, %v", got, err)
	}
}

func TestGatewayProxyValidationRejectsCredentialsAndPaths(t *testing.T) {
	for _, raw := range []string{"https://gateway:3128", "http://user:pass@gateway:3128", "http://gateway:3128/proxy", "http://gateway:3128?token=x"} {
		if _, err := validateGatewayProxy(raw); err == nil {
			t.Errorf("expected gateway proxy rejection: %q", raw)
		}
	}
	if got, err := validateGatewayProxy("http://gateway:3128"); err != nil || got != "http://gateway:3128" {
		t.Fatalf("gateway proxy = %q, %v", got, err)
	}
}

func TestBrowserClientModeSelectionIsStrictAndBounded(t *testing.T) {
	mcp, err := selectBrowserClientMode("codex", "mcp", "/missing", false)
	if err != nil || mcp.Selected != "mcp" || mcp.PluginPath != "" {
		t.Fatalf("mcp selection = %#v, %v", mcp, err)
	}
	auto, err := selectBrowserClientMode("codex", "auto", "/missing", false)
	if err != nil ||
		auto.Selected != "mcp" ||
		auto.FallbackReason != "native_bundle_unavailable" {
		t.Fatalf("auto fallback = %#v, %v", auto, err)
	}
	if _, err := selectBrowserClientMode("codex", "native", "/missing", false); err == nil ||
		!strings.Contains(err.Error(), "native_bundle_unavailable") {
		t.Fatalf("strict native error = %v", err)
	}
	if _, err := selectBrowserClientMode("codex", "AUTO", "/missing", false); err == nil {
		t.Fatal("case-confused Browser client mode was accepted")
	}
}

func TestLinuxCodexDefaultsToAutoWithoutChangingOtherProviders(t *testing.T) {
	if got := defaultBrowserClientMode("codex", "linux"); got != "auto" {
		t.Fatalf("Linux Codex default = %q", got)
	}
	for _, input := range [][2]string{
		{"claude", "linux"},
		{"codex", "darwin"},
		{"codex", "windows"},
	} {
		if got := defaultBrowserClientMode(input[0], input[1]); got != "mcp" {
			t.Fatalf("default for %s/%s = %q", input[0], input[1], got)
		}
	}
}

func TestCodexNativeBrowserPluginActivationRequiresExactHostList(t *testing.T) {
	root := writeTestAgentRuntimePlugin(t, "codex")
	original := runBrowserClientHostCommand
	t.Cleanup(func() { runBrowserClientHostCommand = original })
	var calls [][]string
	runBrowserClientHostCommand = func(args ...string) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		if len(args) >= 2 && args[0] == "plugin" && args[1] == "list" {
			return json.Marshal(map[string]any{
				"installed": []any{map[string]any{
					"pluginId":        "openlinker@openlinker-agent-runtime",
					"name":            "openlinker",
					"marketplaceName": "openlinker-agent-runtime",
					"version":         "0.1.0+agent-runtime.codex",
					"installed":       true,
					"enabled":         true,
					"source": map[string]any{
						"source": "local",
						"path":   filepath.Join(root, "plugins", "openlinker"),
					},
					"marketplaceSource": map[string]any{
						"sourceType": "local",
						"source":     root,
					},
					"installPolicy": "AVAILABLE",
					"authPolicy":    "ON_INSTALL",
				}},
				"available": []any{},
			})
		}
		return []byte(`{}`), nil
	}
	selection, err := selectBrowserClientMode("codex", "native", root, false)
	if err != nil || selection.Selected != "native" || len(calls) != 3 {
		t.Fatalf("Codex native selection = %#v, calls=%#v, %v", selection, calls, err)
	}

	runBrowserClientHostCommand = func(args ...string) ([]byte, error) {
		if len(args) >= 2 && args[0] == "plugin" && args[1] == "list" {
			return []byte(`{"installed":[],"available":[]}`), nil
		}
		return []byte(`{}`), nil
	}
	fallback, err := selectBrowserClientMode("codex", "auto", root, false)
	if err != nil ||
		fallback.Selected != "mcp" ||
		fallback.FallbackReason != "native_tool_handshake_failed" {
		t.Fatalf("Codex host-list fallback = %#v, %v", fallback, err)
	}
}

func TestClaudeNativeBrowserPluginUsesStrictHostValidation(t *testing.T) {
	root := writeTestAgentRuntimePlugin(t, "claude")
	original := runBrowserClientHostCommand
	t.Cleanup(func() { runBrowserClientHostCommand = original })
	var call []string
	runBrowserClientHostCommand = func(args ...string) ([]byte, error) {
		call = append([]string(nil), args...)
		return []byte("validation passed"), nil
	}
	selection, err := selectBrowserClientMode("claude", "native", root, false)
	if err != nil || selection.Selected != "native" {
		t.Fatalf("Claude native selection = %#v, %v", selection, err)
	}
	want := []string{"plugin", "validate", "--strict", root}
	if strings.Join(call, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("Claude validation call = %#v, want %#v", call, want)
	}
}

func TestConfigureBrowserAgentRejectsUserTokenAndDefaultsToMCP(t *testing.T) {
	runtimeDir := t.TempDir()
	workspace := t.TempDir()
	if err := os.Chmod(runtimeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{
		"OPENLINKER_URL":                     "https://openlinker.example",
		"OPENLINKER_AGENT_ID":                "11111111-1111-4111-8111-111111111111",
		"OPENLINKER_AGENT_TOKEN":             "ol_agent_test",
		"CODEX_API_KEY":                      "codex-test",
		"OPENLINKER_NODE_ID":                 "",
		"OPENLINKER_BLOCK_PRIVATE_NETWORK":   "true",
		"OPENLINKER_EGRESS_PROXY_URL":        "http://gateway:3128",
		"OPENLINKER_AGENT_EXECUTION_PROFILE": "browser",
	} {
		t.Setenv(key, value)
	}
	if err := configure("codex", runtimeDir, workspace, false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if runtimeLock != nil {
			_ = runtimeLock.Close()
			runtimeLock = nil
		}
	})
	if got := os.Getenv("OPENLINKER_BROWSER_CLIENT_MODE_EFFECTIVE"); got != "mcp" {
		t.Fatalf("default effective Browser client mode = %q", got)
	}

	_ = runtimeLock.Close()
	runtimeLock = nil
	t.Setenv("OPENLINKER_USER_TOKEN", "ol_user_forbidden")
	if err := configure("codex", runtimeDir, workspace, false); err == nil ||
		!strings.Contains(err.Error(), "not supported") {
		t.Fatalf("User Token rejection = %v", err)
	}
}

func writeTestAgentRuntimePlugin(t *testing.T, provider string) string {
	t.Helper()
	root := t.TempDir()
	var files map[string]string
	if provider == "codex" {
		files = map[string]string{
			".agents/plugins/marketplace.json": `{"name":"openlinker-agent-runtime"}`,
			"plugins/openlinker/.codex-plugin/plugin.json": `{
					"name":"openlinker",
					"skills":"./skills/",
					"mcpServers":"./.mcp.json",
					"interface":{
						"displayName":"OpenLinker Isolated Browser",
						"shortDescription":"Use the isolated Browser.",
						"longDescription":"Use the Runtime-authorized isolated Browser.",
						"developerName":"OpenLinker",
						"category":"Productivity",
						"capabilities":["Browser automation"],
						"defaultPrompt":["Use the isolated Browser."]
					}
				}`,
			"plugins/openlinker/.mcp.json": `{
				"mcpServers":{
					"openlinker_browser":{
						"command":"/usr/local/bin/openlinker",
						"args":["plugin","browser-proxy","--host","codex"],
						"cwd":"/workspace",
						"env_vars":["OPENLINKER_BROWSER_TOOL_SOCKET"]
					}
				}
			}`,
			"plugins/openlinker/LICENSE":                                        "Apache-2.0",
			"plugins/openlinker/skills/use-isolated-browser/SKILL.md":           "Browser skill",
			"plugins/openlinker/skills/use-isolated-browser/agents/openai.yaml": "interface: {}",
		}
	} else {
		files = map[string]string{
			".claude-plugin/plugin.json": `{
				"name":"openlinker",
				"skills":"./skills/",
				"commands":"./commands/"
			}`,
			".mcp.json": `{
				"mcpServers":{
					"openlinker_browser":{
						"command":"/usr/local/bin/openlinker",
						"args":["plugin","browser-proxy","--host","claude"],
						"env":{"OPENLINKER_BROWSER_TOOL_SOCKET":"${OPENLINKER_BROWSER_TOOL_SOCKET}"}
					}
				}
			}`,
			"LICENSE":                              "Apache-2.0",
			"commands/use-isolated-browser.md":     "Browser command",
			"skills/use-isolated-browser/SKILL.md": "Browser skill",
		}
	}
	for relative, content := range files {
		path := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}
