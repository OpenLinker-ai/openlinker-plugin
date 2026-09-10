package agentexec

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/agenthost"
)

func TestPluginSessionScopesKeepLegacyBridgeAndDeepModes(t *testing.T) {
	// The Node bridge tests freeze these same baseline vectors. Neither source
	// location nor product refactoring may become a new session authority domain.
	for provider, want := range map[string]string{
		"codex":  "c08446c2529ca0fb053fb543e5afe30caafccf65a4f92354cfbf710bfaee7aae",
		"claude": "8433f70bde587dd3c1c61314631fa3e23f31b596cd472dafa3eb6ac55693b88c",
	} {
		if got := sessionStoreKey(" "+provider+" ", "workspace", " root-context "); got != want {
			t.Fatalf("%s session hash changed: %s", provider, got)
		}
		path := filepath.Join(t.TempDir(), "sessions.json")
		if err := saveSessionForClientMode(path, provider, "workspace", "root-context", "existing-private-session", "standard", 7); err != nil {
			t.Fatal(err)
		}
		if id, generation, changed := loadSessionForClientMode(path, provider, "workspace", "root-context", "standard"); id != "existing-private-session" || generation != 7 || changed {
			t.Fatalf("%s existing standard session changed: %q, %d, %v", provider, id, generation, changed)
		}
	}
	if got := providerSessionClientMode(ProviderConfig{DelegationTargets: []string{"b", "a"}, DelegationProxyBin: "openlinker"}); got != "standard_delegation_v1_b74172aca8150458c0c1c42b" {
		t.Fatalf("delegation authority domain changed: %q", got)
	}
	for _, test := range []struct {
		mode, backend, want string
	}{
		{"mcp", "isolated_chromium", "browser_mcp"},
		{"native", "isolated_chromium", "browser_native_isolated_v2"},
		{"native", "official_chrome_extension", "browser_native_official_chrome_isolated_v2"},
	} {
		config := ProviderConfig{ExecutionProfile: "browser", BrowserClientMode: test.mode, BrowserBackendSelected: test.backend}
		if got := providerSessionClientMode(config); got != test.want {
			t.Fatalf("Plugin deep mode %s/%s changed: %q", test.mode, test.backend, got)
		}
	}
}

// These are Plugin consumer tests, not just tests that the Node leaf exports
// a symbol. Freeze the installed v1 argv and exercise both injection paths.
func TestPluginBrowserProxyUsesPreservedAgentHostV1Contract(t *testing.T) {
	capabilities := agenthost.SupportedCapabilities()
	if capabilities.Protocol != "openlinker.agent-host.v1" || !capabilities.BrowserProxy {
		t.Fatalf("Browser proxy handshake changed: %#v", capabilities)
	}
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			want := []string{"plugin", "browser-proxy", "--host", provider}
			if got := BrowserProxyArguments(provider); !reflect.DeepEqual(got, want) {
				t.Fatalf("Browser proxy argv = %#v, want %#v", got, want)
			}
			config := ProviderConfig{Provider: provider, ExecutionProfile: "browser", BrowserClientMode: "mcp", BrowserPluginBin: "/trusted/plugin-host"}
			var args []string
			if provider == "claude" {
				var payload struct {
					Servers map[string]struct {
						Args []string `json:"args"`
					} `json:"mcpServers"`
				}
				if err := json.Unmarshal([]byte(claudeBrowserMCPConfig(config)), &payload); err != nil {
					t.Fatal(err)
				}
				args = payload.Servers["openlinker_browser"].Args
			} else {
				for _, arg := range codexBrowserMCPArguments(config) {
					if raw, ok := strings.CutPrefix(arg, "mcp_servers.openlinker_browser.args="); ok {
						if err := json.Unmarshal([]byte(raw), &args); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			if !reflect.DeepEqual(args, want) {
				t.Fatalf("injected Browser argv = %#v, want %#v", args, want)
			}
		})
	}
}

func TestPluginFactoryStillRejectsClaudeNativeBrowserDelegation(t *testing.T) {
	for _, requested := range []string{"native", "isolated-native", "openlinker-native-chrome", "official-chrome", "auto"} {
		t.Run(requested, func(t *testing.T) {
			config := ProviderConfig{
				Provider: "claude", ExecutionProfile: "browser", BrowserInteractionPolicy: "restricted",
				BrowserClientModeRequested: requested, BrowserClientMode: "native",
				BrowserNativePlugin: "/trusted/native-plugin", BrowserPluginBin: "/trusted/plugin-host",
				BrowserSocket: "/private/browser.sock", BrowserCredentialFile: "/private/channel-credential",
				BrowserLeaseRoot: "/private/leases", BrowserBrokerRoot: "/private/broker",
			}
			// Positive control reaches Plugin's independent Browser factory without
			// starting a host. An unrelated validation failure cannot satisfy denial.
			provider, err := NewProvider(config)
			if err != nil {
				t.Fatalf("Browser-only factory failed before delegation check: %v", err)
			}
			browser, ok := provider.(*browserExecutionProvider)
			if !ok {
				t.Fatalf("Plugin lost its independent Browser provider: %T", provider)
			}
			if _, ok := browser.base.(ClaudeProvider); !ok {
				t.Fatalf("Plugin delegated its deep Provider.Run to another product: %T", browser.base)
			}
			config.DelegationTargets = []string{nativeTargetID}
			config.DelegationProxyBin = "/must-not-be-probed"
			provider, err = NewProvider(config)
			const want = "Claude native Browser mode cannot be combined with delegation yet; select isolated-mcp explicitly"
			if provider != nil || err == nil || err.Error() != want {
				t.Fatalf("native Browser delegation denial = %T, %v", provider, err)
			}
		})
	}
}
