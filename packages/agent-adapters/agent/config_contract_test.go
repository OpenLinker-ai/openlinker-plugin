package agent

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Frozen product contract: sharing a bad new default between consumers is not
// compatibility. Changes to this oracle require a reviewed v1 migration.
const agentConfigContractID = "openlinker.agent-config.v1"
const agentConfigBaselineCommit = "610bb66d082dc14eb379feb70271540eb3b16d78"

type agentConfigFieldContract struct {
	Name    string "json:\"name\""
	Type    string "json:\"type\""
	JSONTag string "json:\"json_tag\""
}
type agentConfigContractFixture struct {
	ContractID     string                     "json:\"contract_id\""
	BaselineCommit string                     "json:\"baseline_commit\""
	SourceSHA256   map[string]string          "json:\"source_sha256\""
	Schema         []agentConfigFieldContract "json:\"schema\""
	Defaults       map[string]any             "json:\"defaults\""
	PortableConfig map[string]any             "json:\"portable_config\""
}

func agentConfigJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func agentConfigEqual(t *testing.T, got, want any) {
	t.Helper()
	var left, right any
	if err := json.Unmarshal(agentConfigJSON(t, got), &left); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(agentConfigJSON(t, want), &right); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(left, right) {
		t.Fatalf("config contract mismatch\ngot: %s\nwant: %s", agentConfigJSON(t, got), agentConfigJSON(t, want))
	}
}
func agentConfigHash(t *testing.T, value any) string {
	t.Helper()
	sum := sha256.Sum256(agentConfigJSON(t, value))
	return hex.EncodeToString(sum[:])
}
func agentConfigFixture(t *testing.T) agentConfigContractFixture {
	t.Helper()
	raw, err := os.ReadFile("testdata/config-contract-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture agentConfigContractFixture
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&fixture); err != nil {
		t.Fatal(err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		t.Fatal("contract fixture has trailing data")
	}
	if fixture.ContractID != agentConfigContractID || fixture.BaselineCommit != agentConfigBaselineCommit {
		t.Fatal("wrong immutable v1 baseline")
	}
	agentConfigEqual(t, fixture.SourceSHA256, map[string]string{
		"packages/agent-adapters/agent/config.go":      "7870596cb4fc8e7919e4e9eaac52e299538455bbf91e7ab8292219e504939c15",
		"packages/agent-adapters/agent/configure.go":   "7cec6ca2c9c92e80baa6bbdbb1904dca1486023a70a5c31fc8b1163d88f9a152",
		"packages/agent-adapters/agent/application.go": "c729f3b1469c2f5926683c26748893c3762f2ced2e6ae37e64c209a5a09295a7",
		"packages/agent-adapters/agent/files.go":       "e6f0d7c5c02f6639b6d00b54590e67000fec6d233ed1a121ef92b75e9b539d55",
	})
	return fixture
}
func agentConfigSchema() []agentConfigFieldContract {
	typ := reflect.TypeOf(Config{})
	var fields []agentConfigFieldContract
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if field.IsExported() {
			fields = append(fields, agentConfigFieldContract{field.Name, field.Type.String(), field.Tag.Get("json")})
		}
	}
	return fields
}

// Unlike marshaling Config, reflection includes every exported nil/zero field,
// including those hidden by omitempty. No source regex stands in for execution.
func agentConfigValues(config Config) map[string]any {
	typ, value := reflect.TypeOf(config), reflect.ValueOf(config)
	result := map[string]any{}
	for i := 0; i < typ.NumField(); i++ {
		if typ.Field(i).IsExported() {
			result[typ.Field(i).Name] = value.Field(i).Interface()
		}
	}
	return result
}
func agentConfigCopy(values map[string]any) map[string]any {
	result := make(map[string]any, len(values))
	for name, value := range values {
		result[name] = value
	}
	return result
}
func agentConfigEnvironment(path string, overrides map[string]string) func(string) string {
	return func(name string) string {
		if name == "OPENLINKER_AGENT_CONFIG" {
			return path
		}
		return overrides[name]
	}
}
func agentConfigWrite(t *testing.T, path string, raw []byte) {
	t.Helper()
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}
func agentConfigRead(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func agentConfigPortable(t *testing.T, fixture agentConfigContractFixture, root string) map[string]any {
	t.Helper()
	values := agentConfigCopy(fixture.PortableConfig)
	for name, value := range values {
		if path, ok := value.(string); ok && strings.HasPrefix(path, "${TEST_ROOT}") {
			values[name] = root
			if path != "${TEST_ROOT}" {
				values[name] = filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(path, "${TEST_ROOT}/")))
			}
		}
	}
	return values
}
func agentConfigPortableRoundTrip(t *testing.T, fixture agentConfigContractFixture) map[string]any {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "agent.json")
	want := agentConfigPortable(t, fixture, root)
	agentConfigWrite(t, path, agentConfigJSON(t, want))
	getenv := agentConfigEnvironment(path, nil)
	config, gotPath, err := loadConfig(getenv)
	if err != nil || gotPath != path {
		t.Fatalf("portable load: %v", err)
	}
	agentConfigEqual(t, config, want)
	// The actual public writer, not a test-side persistence implementation.
	config, _, err = ConfigureNonSecret(getenv, ConfigureOptions{ChangedFields: []string{}})
	if err != nil {
		t.Fatalf("portable public write: %v", err)
	}
	agentConfigEqual(t, config, want)
	var persisted map[string]any
	if err := json.Unmarshal(agentConfigRead(t, path), &persisted); err != nil {
		t.Fatal(err)
	}
	agentConfigEqual(t, persisted, want)
	config, _, err = loadConfig(getenv)
	if err != nil {
		t.Fatal(err)
	}
	agentConfigEqual(t, config, want)
	for name, value := range persisted {
		if pathValue, ok := value.(string); ok && (pathValue == root || strings.HasPrefix(pathValue, root+string(filepath.Separator))) {
			suffix, err := filepath.Rel(root, pathValue)
			if err != nil {
				t.Fatal(err)
			}
			persisted[name] = "${TEST_ROOT}"
			if suffix != "." {
				persisted[name] = "${TEST_ROOT}/" + filepath.ToSlash(suffix)
			}
		}
	}
	agentConfigEqual(t, persisted, fixture.PortableConfig)
	return persisted
}
func TestAgentConfigContractSchemaAndDefaults(t *testing.T) {
	fixture := agentConfigFixture(t)
	agentConfigEqual(t, agentConfigSchema(), fixture.Schema)
	agentConfigEqual(t, agentConfigValues(defaultConfig()), fixture.Defaults)
	path := filepath.Join(t.TempDir(), "does-not-exist.json")
	config, gotPath, err := loadConfig(agentConfigEnvironment(path, nil))
	if err != nil || gotPath != path {
		t.Fatalf("missing-file defaults: %v", err)
	}
	agentConfigEqual(t, agentConfigValues(config), fixture.Defaults)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("reading defaults wrote a config file")
	}
}
func TestAgentConfigContractLegacyLoadSemantics(t *testing.T) {
	fixture := agentConfigFixture(t)
	cases := []struct {
		name, raw string
		overrides map[string]any
	}{
		{"missing-fields", "{}", nil},
		{"omitted-version", "{\"model\":\"synthetic\"}", map[string]any{"Model": "synthetic"}},
		{"root-null-is-tolerated", "null", nil},
		{"field-null-is-tolerated", "{\"session_reuse\":null,\"capacity\":null,\"transport\":null}", nil},
		{"duplicate-key-last-wins", "{\"capacity\":9,\"capacity\":2}", map[string]any{"Capacity": 2}},
		{"explicit-false", "{\"session_reuse\":false,\"web_search\":false}", map[string]any{"SessionReuse": false}},
		{"explicit-zero-load-is-not-configure", "{\"capacity\":0,\"timeout_seconds\":0}", map[string]any{"Capacity": 0, "TimeoutSeconds": 0}},
		{"explicit-empty", "{\"transport\":\"\",\"codex_sandbox\":\"\",\"allowed_tools\":[]}", map[string]any{"Transport": "", "CodexSandbox": "", "AllowedTools": []string{}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "agent.json")
			before := []byte(tc.raw)
			agentConfigWrite(t, path, before)
			config, _, err := loadConfig(agentConfigEnvironment(path, nil))
			if err != nil {
				t.Fatal(err)
			}
			want := agentConfigCopy(fixture.Defaults)
			for name, value := range tc.overrides {
				want[name] = value
			}
			agentConfigEqual(t, agentConfigValues(config), want)
			if !bytes.Equal(before, agentConfigRead(t, path)) {
				t.Fatal("load modified legacy bytes")
			}
		})
	}
}
func TestAgentConfigContractPortableRoundTrip(t *testing.T) {
	agentConfigPortableRoundTrip(t, agentConfigFixture(t))
}
func TestAgentConfigContractConfigurePatchSemantics(t *testing.T) {
	fixture := agentConfigFixture(t)
	falseValue := false
	cases := []struct {
		name      string
		options   ConfigureOptions
		overrides map[string]any
	}{
		{"nil-changed-empty-is-omitted", ConfigureOptions{}, nil},
		{"empty-changed-ignores-all-options", ConfigureOptions{ChangedFields: []string{}, Model: "ignored", Capacity: 99, Enabled: &falseValue}, nil},
		{"nil-changed-explicit-bool-pointers", ConfigureOptions{Enabled: &falseValue, WebSearch: &falseValue}, map[string]any{"enabled": false, "web_search": false}},
		{"selected-nil-bool-pointer-is-omitted", ConfigureOptions{ChangedFields: []string{"enabled", "web-search"}}, nil},
		{"selected-zero-empty-values", ConfigureOptions{ChangedFields: []string{"model", "url", "allowed-tool", "delegation-target", "enabled", "session-reuse", "web-search"}, Enabled: &falseValue, SessionReuse: &falseValue, WebSearch: &falseValue}, map[string]any{"model": "", "openlinker_url": "", "allowed_tools": nil, "delegation_targets": nil, "enabled": false, "session_reuse": false, "web_search": false}},
		{"nil-changed-empty-slice-clears", ConfigureOptions{AllowedTools: []string{}, DelegationTargets: []string{}}, map[string]any{"allowed_tools": nil, "delegation_targets": nil}},
		{"nil-changed-trims-policy", ConfigureOptions{CodexApproval: "  never  ", ClaudePermission: "  dontAsk  "}, map[string]any{"codex_approval": "never", "claude_permission": "dontAsk"}},
		{"unknown-changed-field-is-ignored", ConfigureOptions{ChangedFields: []string{"unknown-field"}, Model: "ignored"}, nil},
		{"explicit-path-empty-means-cwd", ConfigureOptions{ChangedFields: []string{"workspace", "state-dir", "browser-native-plugin", "browser-socket", "browser-credential-file", "browser-lease-root", "browser-broker-root"}}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "agent.json")
			want := agentConfigPortable(t, fixture, root)
			if tc.name == "selected-zero-empty-values" {
				// Prove an actual true -> false patch, not false -> false.
				want["session_reuse"] = true
			}
			agentConfigWrite(t, path, agentConfigJSON(t, want))
			if tc.name == "selected-zero-empty-values" {
				before, _, err := loadConfig(agentConfigEnvironment(path, nil))
				if err != nil || !before.SessionReuse {
					t.Fatal("explicit false patch lacks a true persisted precondition")
				}
			}
			for name, value := range tc.overrides {
				want[name] = value
			}
			if tc.name == "explicit-path-empty-means-cwd" {
				cwd, err := filepath.Abs("")
				if err != nil {
					t.Fatal(err)
				}
				for _, name := range []string{"workspace", "state_dir", "browser_native_plugin", "browser_socket", "browser_credential_file", "browser_lease_root", "browser_broker_root"} {
					want[name] = cwd
				}
			}
			config, _, err := ConfigureNonSecret(agentConfigEnvironment(path, nil), tc.options)
			if err != nil {
				t.Fatal(err)
			}
			var expected Config
			if err := json.Unmarshal(agentConfigJSON(t, want), &expected); err != nil {
				t.Fatal(err)
			}
			agentConfigEqual(t, agentConfigValues(config), agentConfigValues(expected))
			loaded, _, err := loadConfig(agentConfigEnvironment(path, nil))
			if err != nil {
				t.Fatal(err)
			}
			agentConfigEqual(t, agentConfigValues(loaded), agentConfigValues(expected))
		})
	}
}
func TestAgentConfigContractConfigureCompleteFile(t *testing.T) {
	fixture := agentConfigFixture(t)
	root := t.TempDir()
	path := filepath.Join(root, "agent.json")
	want := agentConfigPortable(t, fixture, root)
	var expected Config
	if err := json.Unmarshal(agentConfigJSON(t, want), &expected); err != nil {
		t.Fatal(err)
	}
	options := ConfigureOptions{
		DelegationTargets: expected.DelegationTargets, DelegationProxyBin: expected.DelegationProxyBin, DelegationBrokerRoot: expected.DelegationBrokerRoot,
		Enabled: &expected.Enabled, Provider: expected.Provider, AgentID: expected.AgentID, Workspace: expected.Workspace, OpenLinkerURL: expected.OpenLinkerURL,
		StateDir: expected.StateDir, ProviderBin: expected.ProviderBin, Model: expected.Model, Transport: expected.Transport, Capacity: expected.Capacity, TimeoutSeconds: expected.TimeoutSeconds,
		SessionReuse: &expected.SessionReuse, WebSearch: &expected.WebSearch, CodexBaseURL: expected.CodexBaseURL, CodexSandbox: expected.CodexSandbox, CodexApproval: expected.CodexApproval,
		ClaudePermission: expected.ClaudePermission, AllowedTools: expected.AllowedTools, ExecutionProfile: expected.ExecutionProfile, BrowserInteractionPolicy: expected.BrowserInteractionPolicy,
		BrowserClientMode: expected.BrowserClientMode, BrowserPluginBin: expected.BrowserPluginBin, BrowserNativePlugin: expected.BrowserNativePlugin,
		BrowserSocket: expected.BrowserSocket, BrowserCredentialFile: expected.BrowserCredentialFile, BrowserLeaseRoot: expected.BrowserLeaseRoot, BrowserBrokerRoot: expected.BrowserBrokerRoot,
	}
	// Exercise the real writer with available synthetic credentials. Their
	// absence from a credential-free fixture alone would prove nothing.
	secrets := map[string]string{
		"OPENLINKER_AGENT_TOKEN": "SYNTHETIC_AGENT_TOKEN_MUST_NOT_PERSIST",
		"OPENLINKER_USER_TOKEN":  "SYNTHETIC_USER_TOKEN_MUST_NOT_PERSIST",
		"CODEX_API_KEY":          "SYNTHETIC_CODEX_API_KEY_MUST_NOT_PERSIST",
		"ANTHROPIC_API_KEY":      "SYNTHETIC_ANTHROPIC_API_KEY_MUST_NOT_PERSIST",
	}
	config, _, err := ConfigureNonSecret(agentConfigEnvironment(path, secrets), options)
	if err != nil {
		t.Fatal(err)
	}
	persisted := agentConfigRead(t, path)
	for _, value := range secrets {
		if bytes.Contains(persisted, []byte(value)) {
			t.Fatal("ConfigureNonSecret persisted a synthetic credential")
		}
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(persisted, &fields); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"token", "agent_token", "user_token", "api_key", "provider_api_key", "codex_api_key", "anthropic_api_key", "credentials", "secrets"} {
		if _, exists := fields[name]; exists {
			t.Fatal("ConfigureNonSecret persisted a credential field")
		}
	}
	agentConfigEqual(t, config, want)
	loaded, _, err := loadConfig(agentConfigEnvironment(path, nil))
	if err != nil {
		t.Fatal(err)
	}
	agentConfigEqual(t, loaded, want)
}
func TestAgentConfigContractRejectsWithoutWrite(t *testing.T) {
	fixture := agentConfigFixture(t)
	for name, raw := range map[string]string{
		"unknown-field": "{\"unknown_field\":true}", "wrong-type": "{\"capacity\":\"one\"}", "unsupported-version": "{\"version\":2}",
		"explicit-version-zero": "{\"version\":0}", "trailing-json": "{} {}", "malformed-json": "{", "array-instead-of-object": "[]",
	} {
		t.Run("decode/"+name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "agent.json")
			before := []byte(raw)
			agentConfigWrite(t, path, before)
			getenv := agentConfigEnvironment(path, nil)
			if _, _, err := loadConfig(getenv); err == nil {
				t.Fatal("invalid legacy file accepted")
			}
			if _, _, err := ConfigureNonSecret(getenv, ConfigureOptions{Model: "replacement"}); err == nil {
				t.Fatal("invalid legacy file overwritten")
			}
			if !bytes.Equal(before, agentConfigRead(t, path)) {
				t.Fatal("failed decode changed bytes")
			}
		})
	}
	for name, options := range map[string]ConfigureOptions{
		"capacity-zero": {ChangedFields: []string{"capacity"}, Capacity: 0}, "capacity-too-large": {Capacity: 1025},
		"timeout-zero": {ChangedFields: []string{"timeout"}, TimeoutSeconds: 0}, "transport-empty": {ChangedFields: []string{"transport"}},
		"provider-empty": {ChangedFields: []string{"provider"}}, "agent-id-empty": {ChangedFields: []string{"agent-id"}},
		"raw-selected-policy-space": {ChangedFields: []string{"codex-approval"}, CodexApproval: " never "},
		"invalid-sandbox":           {CodexSandbox: "invalid"}, "invalid-permission": {ClaudePermission: "invalid"},
		"credential-bearing-router-url": {CodexBaseURL: "https://synthetic:synthetic@router.example.invalid"},
	} {
		t.Run("configure/"+name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "agent.json")
			before := append(agentConfigJSON(t, agentConfigPortable(t, fixture, root)), '\n')
			agentConfigWrite(t, path, before)
			if _, _, err := ConfigureNonSecret(agentConfigEnvironment(path, nil), options); err == nil {
				t.Fatal("invalid explicit patch accepted")
			}
			if !bytes.Equal(before, agentConfigRead(t, path)) {
				t.Fatal("failed validation changed bytes")
			}
			files, err := os.ReadDir(root)
			if err != nil || len(files) != 1 {
				t.Fatal("failed validation left a temporary file")
			}
		})
	}
}
func TestAgentConfigContractEnvironmentPrecedence(t *testing.T) {
	fixture := agentConfigFixture(t)
	root := t.TempDir()
	path := filepath.Join(root, "agent.json")
	before := agentConfigJSON(t, agentConfigPortable(t, fixture, root))
	agentConfigWrite(t, path, before)
	config, _, err := loadConfig(agentConfigEnvironment(path, nil))
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]string{
		"OPENLINKER_AGENT_TRANSPORT": "ws", "OPENLINKER_CLAUDE_MODEL": "runtime-model", "OPENLINKER_CODEX_MODEL": "wrong-provider",
		"OPENLINKER_AGENT_CAPACITY": "7", "OPENLINKER_AGENT_TIMEOUT_SECONDS": "99", "OPENLINKER_AGENT_SESSION_REUSE": "on",
		"OPENLINKER_AGENT_WEB_SEARCH": "true", "OPENLINKER_CLAUDE_WEB_SEARCH": "false",
		"OPENLINKER_AGENT_DELEGATION_TARGETS":   " 33333333-3333-4333-8333-333333333333, ,44444444-4444-4444-8444-444444444444 ",
		"OPENLINKER_AGENT_DELEGATION_PROXY_BIN": "env-proxy", "OPENLINKER_AGENT_DELEGATION_BROKER_ROOT": "env-delegation",
		"OPENLINKER_CODEX_BASE_URL": "https://environment.example.invalid/v1", "OPENLINKER_CODEX_SANDBOX": "read-only", "OPENLINKER_CODEX_APPROVAL": "never",
		"OPENLINKER_CLAUDE_PERMISSION": "dontAsk", "OPENLINKER_CLAUDE_ALLOWED_TOOLS": " Read, ,WebFetch ",
		"OPENLINKER_AGENT_EXECUTION_PROFILE": "browser", "OPENLINKER_BROWSER_INTERACTION_POLICY": "full", "OPENLINKER_BROWSER_CLIENT_MODE": "isolated-native",
		"OPENLINKER_BROWSER_PLUGIN_BIN": "env-browser-proxy", "OPENLINKER_BROWSER_NATIVE_PLUGIN_PATH": "env-native",
		"OPENLINKER_BROWSER_SOCKET": "env-socket", "OPENLINKER_BROWSER_CHANNEL_CREDENTIAL_FILE": "env-channel",
		"OPENLINKER_BROWSER_LEASE_ROOT": "env-leases", "OPENLINKER_BROWSER_BROKER_ROOT": "env-broker",
		"OPENLINKER_BROWSER_CLIENT_MODE_EFFECTIVE": " native ", "OPENLINKER_BROWSER_BACKEND_MODE": " official_chrome_extension ", "OPENLINKER_BROWSER_CLIENT_FALLBACK_REASON": " synthetic-fallback ",
	}
	want := agentConfigValues(config)
	for name, value := range map[string]any{
		"Transport": "ws", "Model": "runtime-model", "Capacity": int64(7), "TimeoutSeconds": 99, "SessionReuse": true, "WebSearch": false,
		"DelegationTargets":  []string{"33333333-3333-4333-8333-333333333333", "44444444-4444-4444-8444-444444444444"},
		"DelegationProxyBin": "env-proxy", "DelegationBrokerRoot": "env-delegation", "CodexBaseURL": "https://environment.example.invalid/v1",
		"CodexSandbox": "read-only", "CodexApproval": "never", "ClaudePermission": "dontAsk", "AllowedTools": []string{"Read", "WebFetch"},
		"ExecutionProfile": "browser", "BrowserInteractionPolicy": "full", "BrowserClientMode": "isolated-native", "BrowserPluginBin": "env-browser-proxy",
		"BrowserNativePlugin": "env-native", "BrowserSocket": "env-socket", "BrowserCredentialFile": "env-channel", "BrowserLeaseRoot": "env-leases", "BrowserBrokerRoot": "env-broker",
	} {
		want[name] = value
	}
	if err := applyRuntimeEnvironment(&config, agentConfigEnvironment(path, env)); err != nil {
		t.Fatal(err)
	}
	agentConfigEqual(t, agentConfigValues(config), want)
	if config.browserSelectedMode != "native" || config.browserBackendMode != "official_chrome_extension" || config.browserFallbackReason != "synthetic-fallback" {
		t.Fatal("runtime Browser environment mapping drifted")
	}
	if !bytes.Equal(before, agentConfigRead(t, path)) {
		t.Fatal("runtime environment persisted into config")
	}
	got, err := stateDir(config, agentConfigEnvironment(path, map[string]string{"OPENLINKER_AGENT_STATE_DIR": root, "XDG_STATE_HOME": "ignored"}))
	if err != nil || got != root {
		t.Fatalf("state env precedence failed: %v", err)
	}
	got, err = stateDir(config, agentConfigEnvironment(path, map[string]string{"XDG_STATE_HOME": "ignored"}))
	if err != nil || got != config.StateDir {
		t.Fatalf("stored state precedence failed: %v", err)
	}
	unchanged := defaultConfig()
	if err := applyRuntimeEnvironment(&unchanged, agentConfigEnvironment(path, nil)); err != nil {
		t.Fatal(err)
	}
	agentConfigEqual(t, agentConfigValues(unchanged), fixture.Defaults)
}
func TestAgentConfigContractEnvironmentBooleansAndErrors(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		for raw, want := range map[string]bool{"1": true, "true": true, "yes": true, "on": true, "enabled": true, "0": false, "false": false, "no": false, "off": false, "disabled": false, " TRUE ": true, " OFF ": false} {
			t.Run(provider+"/"+strings.TrimSpace(raw), func(t *testing.T) {
				config := defaultConfig()
				config.Provider = provider
				config.WebSearch = !want
				config.SessionReuse = !want
				env := map[string]string{"OPENLINKER_AGENT_SESSION_REUSE": raw, "OPENLINKER_AGENT_WEB_SEARCH": "invalid-but-shadowed", "OPENLINKER_" + strings.ToUpper(provider) + "_WEB_SEARCH": raw}
				if err := applyRuntimeEnvironment(&config, agentConfigEnvironment("", env)); err != nil {
					t.Fatal(err)
				}
				if config.WebSearch != want || config.SessionReuse != want {
					t.Fatal("boolean spelling or provider precedence drifted")
				}
				env["OPENLINKER_"+strings.ToUpper(provider)+"_WEB_SEARCH"] = "  "
				env["OPENLINKER_AGENT_WEB_SEARCH"] = raw
				config.WebSearch = !want
				if err := applyRuntimeEnvironment(&config, agentConfigEnvironment("", env)); err != nil || config.WebSearch != want {
					t.Fatalf("generic fallback failed: %v", err)
				}
			})
		}
	}
	for _, name := range []string{"OPENLINKER_AGENT_CAPACITY", "OPENLINKER_AGENT_TIMEOUT_SECONDS"} {
		for _, raw := range []string{"0", "-1", "1.5", "invalid", "9223372036854775808"} {
			t.Run(name+"/"+raw, func(t *testing.T) {
				config := defaultConfig()
				if err := applyRuntimeEnvironment(&config, agentConfigEnvironment("", map[string]string{name: raw})); err == nil {
					t.Fatal("invalid positive integer accepted")
				}
			})
		}
	}
	for _, name := range []string{"OPENLINKER_AGENT_SESSION_REUSE", "OPENLINKER_AGENT_WEB_SEARCH", "OPENLINKER_CLAUDE_WEB_SEARCH"} {
		t.Run(name+"/invalid", func(t *testing.T) {
			config := defaultConfig()
			config.Provider = "claude"
			if err := applyRuntimeEnvironment(&config, agentConfigEnvironment("", map[string]string{name: "invalid"})); err == nil {
				t.Fatal("invalid boolean accepted")
			}
		})
	}
}
func TestAgentConfigContractEvidence(t *testing.T) {
	fixture := agentConfigFixture(t)
	schema, defaults := agentConfigSchema(), agentConfigValues(defaultConfig())
	agentConfigEqual(t, schema, fixture.Schema)
	agentConfigEqual(t, defaults, fixture.Defaults)
	portable := agentConfigPortableRoundTrip(t, fixture)
	pkg := reflect.TypeOf(Config{}).PkgPath()
	module, _, ok := strings.Cut(pkg, "/pkg/")
	if !ok {
		module, _, ok = strings.Cut(pkg, "/packages/")
	}
	if !ok || module == "" {
		t.Fatal("could not resolve actual consumer identity")
	}
	evidence := map[string]any{
		"contract_id": fixture.ContractID, "module": module, "package": pkg, "baseline_commit": fixture.BaselineCommit,
		"source_sha256": fixture.SourceSHA256, "schema_sha256": agentConfigHash(t, schema), "defaults_sha256": agentConfigHash(t, defaults),
		"portable_fixture": portable, "portable_fixture_sha256": agentConfigHash(t, portable),
	}
	t.Logf("OPENLINKER_CONFIG_CONTRACT=%s", agentConfigJSON(t, evidence))
}
