package agent

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/OpenLinker-ai/openlinker-plugin/packages/agent-adapters/browserextension"
)

func TestNodeIDIsGeneratedPersistedAndConflictsFail(t *testing.T) {
	dir := t.TempDir()
	first, err := loadOrCreateNodeID(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := loadOrCreateNodeID(dir, "")
	if err != nil || second != first {
		t.Fatalf("persisted Node ID = %q, %v; want %q", second, err, first)
	}
	info, err := os.Stat(filepath.Join(dir, "node-id"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("Node ID file = %#v, %v", info, err)
	}
	if _, err := loadOrCreateNodeID(dir, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"); err == nil {
		t.Fatal("expected explicit Node ID conflict")
	}
}

func TestSecretSourcesAreExclusiveAndPrivate(t *testing.T) {
	dir := t.TempDir()
	secretPath := filepath.Join(dir, "token")
	if err := os.WriteFile(secretPath, []byte("secret-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	environment := map[string]string{"TOKEN_FILE": secretPath}
	getenv := func(key string) string { return environment[key] }
	value, source, err := resolveSecret(getenv, "TOKEN", "TOKEN_FILE", true)
	if err != nil || value != "secret-value" || source != "file" {
		t.Fatalf("secret = %q/%q, %v", value, source, err)
	}
	environment["TOKEN"] = "direct"
	if _, _, err := resolveSecret(getenv, "TOKEN", "TOKEN_FILE", true); err == nil {
		t.Fatal("expected mutually exclusive secret error")
	}
	if err := os.Chmod(secretPath, 0o644); err != nil {
		t.Fatal(err)
	}
	delete(environment, "TOKEN")
	if _, _, err := resolveSecret(getenv, "TOKEN", "TOKEN_FILE", true); err == nil {
		t.Fatal("expected insecure secret mode error")
	}
}

func TestConfigureNonSecretNeverWritesCredentials(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config", "agent.json")
	environment := map[string]string{"OPENLINKER_AGENT_CONFIG": configPath, "OPENLINKER_AGENT_TOKEN": "must-not-leak"}
	getenv := func(key string) string { return environment[key] }
	config, _, err := ConfigureNonSecret(getenv, ConfigureOptions{
		Provider: "codex", AgentID: "11111111-1111-4111-8111-111111111111", Workspace: dir,
		CodexBaseURL: "https://router.example/v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if config.Provider != "codex" || config.CodexBaseURL != "https://router.example/v1" {
		t.Fatalf("config = %#v", config)
	}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "must-not-leak") || strings.Contains(strings.ToLower(string(raw)), "agent_token") {
		t.Fatalf("config leaked a credential: %s", raw)
	}
}

func TestApplyRuntimeEnvironmentUsesProviderSpecificSettings(t *testing.T) {
	config := defaultConfig()
	config.Provider = "codex"
	environment := map[string]string{
		"OPENLINKER_AGENT_TRANSPORT":                 "pull",
		"OPENLINKER_AGENT_CAPACITY":                  "3",
		"OPENLINKER_AGENT_TIMEOUT_SECONDS":           "90",
		"OPENLINKER_AGENT_SESSION_REUSE":             "false",
		"OPENLINKER_CODEX_MODEL":                     "gpt-test",
		"OPENLINKER_CODEX_BASE_URL":                  "https://router.example/v1",
		"OPENLINKER_CODEX_WEB_SEARCH":                "enabled",
		"OPENLINKER_CODEX_SANDBOX":                   "workspace-write",
		"OPENLINKER_CODEX_APPROVAL":                  "never",
		"OPENLINKER_AGENT_EXECUTION_PROFILE":         "browser",
		"OPENLINKER_BROWSER_INTERACTION_POLICY":      "full",
		"OPENLINKER_BROWSER_CLIENT_MODE":             "auto",
		"OPENLINKER_BROWSER_CLIENT_MODE_EFFECTIVE":   "native",
		"OPENLINKER_BROWSER_BACKEND_MODE":            "auto",
		"OPENLINKER_BROWSER_NATIVE_PLUGIN_PATH":      "/opt/openlinker/agent-runtime-plugin/codex",
		"OPENLINKER_BROWSER_SOCKET":                  "/browser/control.sock",
		"OPENLINKER_BROWSER_CHANNEL_CREDENTIAL_FILE": "/browser/channel",
		"OPENLINKER_BROWSER_LEASE_ROOT":              "/browser/leases",
		"OPENLINKER_BROWSER_BROKER_ROOT":             "/browser/broker",
	}
	if err := applyRuntimeEnvironment(&config, func(key string) string { return environment[key] }); err != nil {
		t.Fatal(err)
	}
	if config.Transport != "pull" || config.Capacity != 3 || config.TimeoutSeconds != 90 || config.SessionReuse ||
		config.Model != "gpt-test" || config.CodexBaseURL != "https://router.example/v1" || !config.WebSearch || config.CodexSandbox != "workspace-write" {
		t.Fatalf("environment overrides = %#v", config)
	}
	if config.ExecutionProfile != "browser" || config.BrowserInteractionPolicy != "full" ||
		config.BrowserSocket != "/browser/control.sock" ||
		config.BrowserCredentialFile != "/browser/channel" || config.BrowserLeaseRoot != "/browser/leases" ||
		config.BrowserBrokerRoot != "/browser/broker" ||
		config.BrowserClientMode != "auto" ||
		config.browserSelectedMode != "native" ||
		config.browserBackendMode != "auto" ||
		config.BrowserNativePlugin != "/opt/openlinker/agent-runtime-plugin/codex" {
		t.Fatalf("Browser environment overrides = %#v", config)
	}
}

func TestBrowserExecutionProfileIsExplicitAndSingleCapacity(t *testing.T) {
	config := defaultConfig()
	config.Provider = "codex"
	config.AgentID = "11111111-1111-4111-8111-111111111111"
	config.Workspace = t.TempDir()
	config.ExecutionProfile = "browser"
	config.BrowserSocket = "/browser/control.sock"
	config.BrowserCredentialFile = "/browser/channel"
	config.BrowserLeaseRoot = "/browser/leases"
	config.BrowserBrokerRoot = "/browser/broker"
	if err := validateNonSecretConfig(config); err != nil {
		t.Fatal(err)
	}
	config.BrowserInteractionPolicy = "full"
	if err := validateNonSecretConfig(config); err != nil {
		t.Fatalf("full Browser interaction policy was rejected: %v", err)
	}
	config.BrowserClientMode = "auto"
	if err := validateNonSecretConfig(config); err != nil {
		t.Fatalf("persisted Browser auto mode was rejected: %v", err)
	}
	config.browserSelectedMode = "native"
	config.BrowserNativePlugin = "/opt/openlinker/agent-runtime-plugin/codex"
	if err := validateNonSecretConfig(config); err != nil {
		t.Fatalf("resolved Browser auto mode was rejected: %v", err)
	}
	config.Capacity = 2
	if err := validateNonSecretConfig(config); err == nil ||
		!strings.Contains(err.Error(), "capacity 1") {
		t.Fatalf("multi-capacity Browser profile error = %v", err)
	}
	config = defaultConfig()
	config.Provider = "codex"
	config.AgentID = "11111111-1111-4111-8111-111111111111"
	config.Workspace = t.TempDir()
	config.BrowserSocket = "/browser/control.sock"
	if err := validateNonSecretConfig(config); err != nil {
		t.Fatalf("ordinary Agent rejected unused Browser config: %v", err)
	}
	config.BrowserInteractionPolicy = "full"
	if err := validateNonSecretConfig(config); err == nil ||
		!strings.Contains(err.Error(), "standard execution profile") {
		t.Fatalf("ordinary Agent accepted full Browser policy: %v", err)
	}
}

func TestDefaultBrowserClientModePrefersNativeChromeOnlyForLinuxCodex(t *testing.T) {
	if got := defaultBrowserClientMode("codex", "linux"); got != "auto" {
		t.Fatalf("Linux Codex Browser default = %q, want auto", got)
	}
	for _, input := range [][2]string{
		{"codex", "darwin"},
		{"codex", "windows"},
		{"claude", "linux"},
	} {
		if got := defaultBrowserClientMode(input[0], input[1]); got != "mcp" {
			t.Fatalf("defaultBrowserClientMode(%q, %q) = %q, want mcp", input[0], input[1], got)
		}
	}
}

func TestValidateCodexBaseURL(t *testing.T) {
	workspace := t.TempDir()
	base := defaultConfig()
	base.Provider = "codex"
	base.AgentID = "11111111-1111-4111-8111-111111111111"
	base.Workspace = workspace

	for _, value := range []string{"https://router.example/v1", "http://127.0.0.1:8080/v1"} {
		config := base
		config.CodexBaseURL = value
		if err := validateNonSecretConfig(config); err != nil {
			t.Fatalf("valid Codex Base URL %q: %v", value, err)
		}
	}
	for _, value := range []string{
		"router.example/v1", "ftp://router.example/v1", "https://user:pass@router.example/v1",
		"https://router.example/v1?debug=true", "https://router.example/v1#fragment",
	} {
		config := base
		config.CodexBaseURL = value
		if err := validateNonSecretConfig(config); err == nil {
			t.Fatalf("invalid Codex Base URL %q was accepted", value)
		}
	}
}

func TestApplyRuntimeEnvironmentRejectsInvalidValues(t *testing.T) {
	for name, value := range map[string]string{
		"OPENLINKER_AGENT_CAPACITY":      "zero",
		"OPENLINKER_AGENT_SESSION_REUSE": "sometimes",
		"OPENLINKER_CLAUDE_WEB_SEARCH":   "perhaps",
	} {
		config := defaultConfig()
		config.Provider = "claude"
		if err := applyRuntimeEnvironment(&config, func(key string) string {
			if key == name {
				return value
			}
			return ""
		}); err == nil {
			t.Fatalf("expected %s=%q to fail", name, value)
		}
	}
}

func TestWebSearchBooleanSpellingsWorkForBothProviders(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		for value, want := range map[string]bool{"true": true, "false": false, "enabled": true, "disabled": false} {
			t.Run(provider+"/"+value, func(t *testing.T) {
				config := defaultConfig()
				config.Provider = provider
				config.WebSearch = !want
				name := "OPENLINKER_" + strings.ToUpper(provider) + "_WEB_SEARCH"
				err := applyRuntimeEnvironment(&config, func(key string) string {
					if key == name {
						return value
					}
					return ""
				})
				if err != nil || config.WebSearch != want {
					t.Fatalf("%s=%s: web_search=%t, error=%v", name, value, config.WebSearch, err)
				}
			})
		}
	}
}

func TestAgentModeLockIsExclusiveAndReusable(t *testing.T) {
	dir := t.TempDir()
	first, err := acquireAgentModeLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acquireAgentModeLock(dir); err == nil || !strings.Contains(err.Error(), "already serving") {
		t.Fatalf("second lock error = %v", err)
	}
	if err := first.release(); err != nil {
		t.Fatal(err)
	}
	second, err := acquireAgentModeLock(dir)
	if err != nil {
		t.Fatalf("reacquire lock: %v", err)
	}
	if err := second.release(); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeOptionalFeaturesOnlyAdvertiseBrowserProfile(t *testing.T) {
	if features := runtimeOptionalFeatures("standard", "restricted", true, false); features != nil {
		t.Fatalf("standard Runtime features = %#v", features)
	}
	features := runtimeOptionalFeatures("browser", "restricted", false, false)
	if len(features) != 1 ||
		features[0] != browserExecutionProfileFeature {
		t.Fatalf("Browser Runtime features without Viewer = %#v", features)
	}
	features = runtimeOptionalFeatures("browser", "restricted", true, false)
	if len(features) != 2 ||
		features[0] != browserExecutionProfileFeature ||
		features[1] != browserHumanControlFeature {
		t.Fatalf("browser Runtime features = %#v", features)
	}
	features = runtimeOptionalFeatures("browser", "full", false, false)
	if len(features) != 2 ||
		features[0] != browserExecutionProfileFeature ||
		features[1] != browserFullInteractionFeature {
		t.Fatalf("full Browser Runtime features = %#v", features)
	}
}

func TestRuntimeViewerExtensionIsRegisteredOnlyWithHumanControl(t *testing.T) {
	if routes := runtimeExtensionRoutes("standard", true, false); routes != nil {
		t.Fatalf("standard Agent Runtime extension routes = %#v", routes)
	}
	if routes := runtimeExtensionRoutes("browser", false, false); routes != nil {
		t.Fatalf("Browser Agent without human control routes = %#v", routes)
	}
	routes := runtimeExtensionRoutes("browser", true, false)
	if len(routes) != 1 ||
		routes[0] != browserextension.RuntimeViewerExtensionRoute {
		t.Fatalf("Browser human-control Runtime extension routes = %#v", routes)
	}
}

func TestRuntimeHumanControlIsAnExplicitDeploymentCapability(t *testing.T) {
	values := map[string]string{}
	getenv := func(name string) string { return values[name] }
	if enabled, err := runtimeHumanControlEnabled(
		getenv,
		"browser",
	); err != nil || enabled {
		t.Fatalf("default human control = %v, %v", enabled, err)
	}
	values[browserHumanControlEnvironment] = "true"
	if enabled, err := runtimeHumanControlEnabled(
		getenv,
		"browser",
	); err != nil || !enabled {
		t.Fatalf("configured human control = %v, %v", enabled, err)
	}
	values[browserHumanControlEnvironment] = "site-response"
	if _, err := runtimeHumanControlEnabled(getenv, "browser"); err == nil {
		t.Fatal("invalid human-control capability value was accepted")
	}
}

// A Worker that announces observation but registers no route would make Core
// send commands into a channel nobody reads, so the flag has to drive both.
func TestObservationFeatureAndRouteMoveTogether(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name        string
		observation bool
	}{
		{name: "disabled", observation: false},
		{name: "enabled", observation: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			features := runtimeOptionalFeatures("browser", "restricted", false, testCase.observation)
			routes := runtimeExtensionRoutes("browser", false, testCase.observation)

			declared := slices.Contains(features, browserextension.ObserverBridgeFeature)
			routed := false
			for _, route := range routes {
				if route.CommandType == browserextension.ObserverBridgeCommandType {
					routed = true
				}
			}
			if declared != testCase.observation || routed != testCase.observation {
				t.Fatalf("observation=%v declared=%v routed=%v", testCase.observation, declared, routed)
			}
		})
	}

	// The two Browser extensions are independent: enabling one must not drag the
	// other in, or a deployment could gain takeover by asking only to observe.
	observeOnly := runtimeExtensionRoutes("browser", false, true)
	for _, route := range observeOnly {
		if route.CommandType == browserextension.RuntimeViewerCommandMessage {
			t.Fatal("observation alone registered the human-control route")
		}
	}
	controlOnly := runtimeExtensionRoutes("browser", true, false)
	for _, route := range controlOnly {
		if route.CommandType == browserextension.ObserverBridgeCommandType {
			t.Fatal("human control alone registered the observation route")
		}
	}
}

func TestObservationFlagParsing(t *testing.T) {
	t.Parallel()
	getenv := func(value string) func(string) string {
		return func(name string) string {
			if name == browserObservationEnvironment {
				return value
			}
			return ""
		}
	}
	for value, want := range map[string]bool{"": false, "false": false, "true": true} {
		enabled, err := runtimeObservationEnabled(getenv(value), "browser")
		if err != nil || enabled != want {
			t.Fatalf("%q -> (%v, %v), want (%v, nil)", value, enabled, err, want)
		}
	}
	if _, err := runtimeObservationEnabled(getenv("yes"), "browser"); err == nil {
		t.Fatal("an unparseable flag must fail closed")
	}
	// A non-browser profile has no bridge at all, so the flag cannot turn it on.
	if enabled, err := runtimeObservationEnabled(getenv("true"), "standard"); err != nil || enabled {
		t.Fatalf("standard profile observation = (%v, %v)", enabled, err)
	}
}
