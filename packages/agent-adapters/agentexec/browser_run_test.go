package agentexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	openlinker "github.com/OpenLinker-ai/openlinker-go"
	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserclient"
	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserprotocol"
)

type browserCaptureProvider struct {
	leases []browserclient.Lease
	rotate bool
	root   string
}

func (provider *browserCaptureProvider) Run(
	_ context.Context,
	run RunContext,
) (openlinker.RuntimeResult, error) {
	if run.Browser == nil {
		return openlinker.RuntimeResult{}, errors.New("Browser context is missing")
	}
	readLease := func() error {
		raw, err := os.ReadFile(filepath.Join(provider.root, "runs", run.RunID+".json"))
		if err != nil {
			return err
		}
		var lease browserclient.Lease
		if err := json.Unmarshal(raw, &lease); err != nil {
			return err
		}
		provider.leases = append(provider.leases, lease)
		return nil
	}
	if err := readLease(); err != nil {
		return openlinker.RuntimeResult{}, err
	}
	if provider.rotate {
		if err := run.Browser.Rotate(); err != nil {
			return openlinker.RuntimeResult{}, err
		}
		if err := readLease(); err != nil {
			return openlinker.RuntimeResult{}, err
		}
	}
	return openlinker.RuntimeResult{
		Status: "success",
		Output: map[string]any{"ok": true},
	}, nil
}

func TestBrowserExecutionLeaseUsesAuthorityAndRejectsLateAttachment(t *testing.T) {
	root := shortBrowserTestRoot(t)
	base := &browserCaptureProvider{rotate: true, root: root}
	config := browserProviderTestConfig(root)
	provider, err := newBrowserExecutionProvider(base, config)
	if err != nil {
		t.Fatal(err)
	}
	stubBrowserPreflight(t, provider)
	run := browserProviderTestRun("44444444-4444-4444-8444-444444444444")
	if _, err := provider.Run(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if len(base.leases) != 2 {
		t.Fatalf("leases = %#v", base.leases)
	}
	first, rotated := base.leases[0], base.leases[1]
	if first.Identity.PrincipalScopeID != run.Authority.PrincipalScopeID ||
		first.Identity.AgentID != run.AgentID ||
		first.Identity.RunID != run.RunID {
		t.Fatalf("lease did not use authority: %#v", first.Identity)
	}
	if first.Identity.BrowserSessionID != rotated.Identity.BrowserSessionID ||
		rotated.Identity.SessionEpoch != first.Identity.SessionEpoch+1 ||
		rotated.Identity.ControlEpoch != first.Identity.ControlEpoch+1 ||
		rotated.Identity.AttachmentID == first.Identity.AttachmentID {
		t.Fatalf("rotation did not fence the old attachment: first=%#v rotated=%#v", first.Identity, rotated.Identity)
	}
	for _, path := range []string{
		filepath.Join(root, "active-lease.json"),
		filepath.Join(root, "runs", run.RunID+".json"),
	} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("lease remained after completion at %s: %v", path, err)
		}
	}
}

func TestBrowserExecutionRejectsLocalAndCorePolicyMismatch(t *testing.T) {
	root := shortBrowserTestRoot(t)
	config := browserProviderTestConfig(root)
	provider, err := newBrowserExecutionProvider(&browserCaptureProvider{root: root}, config)
	if err != nil {
		t.Fatal(err)
	}
	stubBrowserPreflight(t, provider)
	run := browserProviderTestRun("44444444-4444-4444-8444-444444444444")
	run.Authority.BrowserInteractionPolicy = "full"
	run.Authority.BrowserMutationOrigins = []string{"https://github.com"}
	_, digest, failure := browserprotocol.CanonicalMutationOrigins(
		"full",
		run.Authority.BrowserMutationOrigins,
	)
	if failure != nil {
		t.Fatal(failure)
	}
	run.Authority.BrowserMutationOriginsSHA256 = digest
	if _, err := provider.Run(context.Background(), run); err == nil ||
		!strings.Contains(err.Error(), "does not match Core-owned Runtime authority") {
		t.Fatalf("policy mismatch error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "runs", run.RunID+".json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("policy mismatch created a Run lease: %v", err)
	}
}

func TestBrowserExecutionPropagatesFullPolicyIntoTheFencedLease(t *testing.T) {
	root := shortBrowserTestRoot(t)
	base := &browserCaptureProvider{root: root}
	config := browserProviderTestConfig(root)
	config.BrowserInteractionPolicy = "full"
	provider, err := newBrowserExecutionProvider(base, config)
	if err != nil {
		t.Fatal(err)
	}
	stubBrowserPreflight(t, provider)
	run := browserProviderTestRun("44444444-4444-4444-8444-444444444444")
	run.Authority.BrowserInteractionPolicy = "full"
	run.Authority.BrowserInteractionPolicyGeneration = 7
	run.Authority.BrowserMutationOrigins = []string{"https://github.com"}
	_, digest, failure := browserprotocol.CanonicalMutationOrigins(
		"full",
		run.Authority.BrowserMutationOrigins,
	)
	if failure != nil {
		t.Fatal(failure)
	}
	run.Authority.BrowserMutationOriginsSHA256 = digest
	if _, err := provider.Run(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if len(base.leases) != 1 {
		t.Fatalf("leases = %#v", base.leases)
	}
	identity := base.leases[0].Identity
	if identity.BrowserInteractionPolicy != "full" ||
		identity.BrowserInteractionPolicyGeneration != 7 ||
		identity.BrowserMutationOriginsSHA256 != digest ||
		!slices.Equal(identity.BrowserMutationOrigins, []string{"https://github.com"}) {
		t.Fatalf("full Browser authority was not preserved: %#v", identity)
	}
}

func TestBrowserRunLeaseCloseReleasesScopeAndCanRetryAfterAuthorityLockFailure(
	t *testing.T,
) {
	root := shortBrowserTestRoot(t)
	released := 0
	lease := &browserRunLease{
		activePath: filepath.Join(root, "active-lease.json"),
		runPath:    filepath.Join(root, "runs", "missing.json"),
		releaseScope: func() {
			released++
		},
	}
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err == nil {
		t.Fatal("Close succeeded with an insecure authority directory")
	}
	if released != 1 || lease.releaseScope != nil {
		t.Fatalf(
			"scope release state = count %d callback_present %v",
			released,
			lease.releaseScope != nil,
		)
	}
	if lease.closed {
		t.Fatal("failed Close became silently idempotent")
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := ensurePrivateBrowserDirectory(filepath.Join(root, "runs")); err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatalf("retry Close failed: %v", err)
	}
	if !lease.closed || released != 1 {
		t.Fatalf("retry state = closed %v release count %d", lease.closed, released)
	}
}

func TestBrowserExecutionReusesConversationSessionAndFencesRuntimeReattach(t *testing.T) {
	root := shortBrowserTestRoot(t)
	base := &browserCaptureProvider{root: root}
	provider, err := newBrowserExecutionProvider(base, browserProviderTestConfig(root))
	if err != nil {
		t.Fatal(err)
	}
	stubBrowserPreflight(t, provider)
	firstRun := browserProviderTestRun("44444444-4444-4444-8444-444444444444")
	if _, err := provider.Run(context.Background(), firstRun); err != nil {
		t.Fatal(err)
	}
	secondRun := browserProviderTestRun("55555555-5555-4555-8555-555555555555")
	if _, err := provider.Run(context.Background(), secondRun); err != nil {
		t.Fatal(err)
	}
	thirdRun := browserProviderTestRun("66666666-6666-4666-8666-666666666666")
	thirdRun.Authority.RuntimeAttachmentID = "77777777-7777-4777-8777-777777777777"
	if _, err := provider.Run(context.Background(), thirdRun); err != nil {
		t.Fatal(err)
	}
	if len(base.leases) != 3 {
		t.Fatalf("leases = %#v", base.leases)
	}
	first, second, reattached := base.leases[0], base.leases[1], base.leases[2]
	if first.Identity.BrowserSessionID != second.Identity.BrowserSessionID ||
		second.Identity.SessionEpoch != first.Identity.SessionEpoch ||
		second.Identity.ControlEpoch <= first.Identity.ControlEpoch {
		t.Fatalf("same conversation was not reused safely: first=%#v second=%#v", first.Identity, second.Identity)
	}
	if reattached.Identity.BrowserSessionID != first.Identity.BrowserSessionID ||
		reattached.Identity.SessionEpoch != second.Identity.SessionEpoch+1 {
		t.Fatalf("Runtime reattach did not advance Browser epoch: %#v", reattached.Identity)
	}
}

func TestBrowserClientConfigurationIsOptInAndSecretFree(t *testing.T) {
	standard := codexArguments(ProviderConfig{}, "/workspace", "read-only", "", true)
	if strings.Contains(strings.Join(standard, " "), "openlinker_browser") {
		t.Fatalf("ordinary Codex arguments changed: %#v", standard)
	}
	run := &BrowserRunContext{
		PluginBin:  "/opt/openlinker",
		ToolSocket: "/browser/tool.sock",
	}
	config := providerConfigForBrowserRun(ProviderConfig{
		Env: []string{
			"CODEX_API_KEY=must-not-enter-mcp-config",
			"ANTHROPIC_API_KEY=must-not-enter-mcp-config",
		},
	}, run)
	codexArgs := strings.Join(codexArguments(config, "/workspace", "read-only", "", true), " ")
	for _, expected := range []string{
		"mcp_servers.openlinker_browser.command",
		"browser-proxy",
		`enabled_tools=["browser_session"]`,
	} {
		if !strings.Contains(codexArgs, expected) {
			t.Fatalf("Browser Codex config is missing %q: %s", expected, codexArgs)
		}
	}
	claudeArgs := strings.Join(claudeArguments(config, "dontAsk", ""), " ")
	if !strings.Contains(claudeArgs, "--bare") ||
		!strings.Contains(claudeArgs, "--strict-mcp-config") ||
		!strings.Contains(claudeArgs, "--allowedTools mcp__openlinker_browser__browser_session") ||
		strings.Contains(claudeArgs, "--safe-mode") {
		t.Fatalf("Browser Claude config is not isolated: %s", claudeArgs)
	}
	for _, secret := range []string{"must-not-enter-mcp-config", "CODEX_API_KEY", "ANTHROPIC_API_KEY"} {
		if strings.Contains(claudeArgs, secret) || strings.Contains(codexArgs, secret) {
			t.Fatalf("Browser client config leaked %q", secret)
		}
	}
}

func TestBrowserClientModesExposeExactlyOneProviderSurface(t *testing.T) {
	run := &BrowserRunContext{
		PluginBin:  "/usr/local/bin/openlinker",
		ToolSocket: "/browser/tool.sock",
	}
	native := providerConfigForBrowserRun(ProviderConfig{
		Provider:                   "codex",
		ExecutionProfile:           "browser",
		BrowserClientModeRequested: "native",
		BrowserClientMode:          "native",
		BrowserNativePlugin:        "/opt/openlinker/agent-runtime-plugin/codex",
	}, run)
	nativeCodex := strings.Join(
		codexArguments(native, "/workspace", "danger-full-access", "", true),
		" ",
	)
	nativePluginServer := `plugins."openlinker@openlinker-agent-runtime".mcp_servers.openlinker_browser`
	for _, expected := range []string{
		nativePluginServer + ".enabled=true",
		nativePluginServer + ".required=true",
		nativePluginServer + `.enabled_tools=["browser_session"]`,
		nativePluginServer + `.default_tools_approval_mode="auto"`,
	} {
		if !strings.Contains(nativeCodex, expected) {
			t.Fatalf("native Codex Plugin config is missing %q: %s", expected, nativeCodex)
		}
	}
	if strings.Contains(nativeCodex, "mcp_servers.openlinker_browser.command") ||
		strings.Contains(nativeCodex, "browser-proxy") ||
		!strings.Contains(nativeCodex, "--dangerously-bypass-approvals-and-sandbox") ||
		!strings.Contains(nativeCodex, "--disable shell_tool") ||
		!strings.Contains(nativeCodex, "--disable multi_agent") ||
		!strings.Contains(nativeCodex, "tools.view_image=false") ||
		strings.Contains(nativeCodex, "--sandbox danger-full-access") ||
		strings.Contains(nativeCodex, "--ignore-user-config") {
		t.Fatalf("native Codex Plugin surface is incomplete or duplicated: %s", nativeCodex)
	}
	nativeReadOnly := strings.Join(
		codexArguments(native, "/workspace", "read-only", "", true),
		" ",
	)
	if strings.Contains(nativeReadOnly, "--dangerously-bypass-approvals-and-sandbox") ||
		!strings.Contains(nativeReadOnly, "--sandbox read-only") {
		t.Fatalf("native Codex bypass escaped the external-sandbox gate: %s", nativeReadOnly)
	}
	nativeClaude := strings.Join(claudeArguments(native, "dontAsk", ""), " ")
	if !strings.Contains(
		nativeClaude,
		"--plugin-dir /opt/openlinker/agent-runtime-plugin/codex",
	) ||
		strings.Contains(nativeClaude, "--strict-mcp-config") ||
		strings.Contains(nativeClaude, "--disable-slash-commands") {
		t.Fatalf("native Claude surface is not exclusive: %s", nativeClaude)
	}

	direct := native
	direct.BrowserClientModeRequested = "mcp"
	direct.BrowserClientMode = "mcp"
	direct.BrowserNativePlugin = ""
	directCodex := strings.Join(
		codexArguments(direct, "/workspace", "read-only", "", true),
		" ",
	)
	if !strings.Contains(directCodex, "mcp_servers.openlinker_browser") ||
		!strings.Contains(directCodex, "--ignore-user-config") {
		t.Fatalf("direct Codex MCP surface is incomplete: %s", directCodex)
	}
	directClaude := strings.Join(claudeArguments(direct, "dontAsk", ""), " ")
	if !strings.Contains(directClaude, "--strict-mcp-config") ||
		!strings.Contains(directClaude, "--disable-slash-commands") ||
		strings.Contains(directClaude, "--plugin-dir") {
		t.Fatalf("direct Claude MCP surface is not exclusive: %s", directClaude)
	}
}

func TestBrowserClientConfigurationRejectsUnboundedFallback(t *testing.T) {
	base := ProviderConfig{
		Provider:              "codex",
		ExecutionProfile:      "browser",
		BrowserClientMode:     "mcp",
		BrowserPluginBin:      "/usr/local/bin/openlinker",
		BrowserSocket:         "/browser/control.sock",
		BrowserCredentialFile: "/browser/channel",
		BrowserLeaseRoot:      "/browser/leases",
		BrowserBrokerRoot:     "/browser/broker",
	}
	for name, mutate := range map[string]func(*ProviderConfig){
		"auto without effective selection": func(config *ProviderConfig) {
			config.BrowserClientModeRequested = "auto"
			config.BrowserClientMode = "auto"
		},
		"strict mode changed": func(config *ProviderConfig) {
			config.BrowserClientModeRequested = "native"
			config.BrowserClientMode = "mcp"
		},
		"policy failure used as fallback": func(config *ProviderConfig) {
			config.BrowserClientModeRequested = "auto"
			config.BrowserClientFallbackReason = "BROWSER_TARGET_BLOCKED"
		},
	} {
		t.Run(name, func(t *testing.T) {
			config := base
			mutate(&config)
			if err := validateBrowserClientConfig(config); err == nil {
				t.Fatalf("invalid Browser client config was accepted: %#v", config)
			}
		})
	}
}

func TestBrowserClientEvidenceIsBoundedAndUsesCanonicalNames(t *testing.T) {
	evidence := browserClientEvidence(ProviderConfig{
		ExecutionProfile:            "browser",
		BrowserClientModeRequested:  "auto",
		BrowserClientMode:           "mcp",
		BrowserClientFallbackReason: "native_bundle_unavailable",
	})
	if len(evidence) != 4 ||
		evidence["browser_client_mode_requested"] != "auto" ||
		evidence["browser_client_mode_selected"] != "direct_mcp" ||
		evidence["browser_backend_selected"] != "isolated_chromium" ||
		evidence["browser_client_mode_fallback_reason"] !=
			"native_bundle_unavailable" {
		t.Fatalf("direct-MCP evidence = %#v", evidence)
	}
	native := browserClientEvidence(ProviderConfig{
		ExecutionProfile:           "browser",
		BrowserClientModeRequested: "native",
		BrowserClientMode:          "native",
	})
	if len(native) != 3 ||
		native["browser_client_mode_selected"] != "plugin_native" ||
		native["browser_backend_selected"] != "isolated_chromium" {
		t.Fatalf("native evidence = %#v", native)
	}
	for _, values := range []map[string]any{evidence, native} {
		encoded, err := json.Marshal(values)
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{
			"/opt/",
			"OPENLINKER_",
			"API_KEY",
			"TOKEN",
			"runtime_session",
			"attachment",
			"plugin_error",
			"raw_error",
		} {
			if strings.Contains(string(encoded), forbidden) {
				t.Fatalf("Browser client evidence leaked %q: %s", forbidden, encoded)
			}
		}
	}
}

func TestOfficialChromeEvidenceIsCompleteAndRedacted(t *testing.T) {
	evidence := browserClientEvidence(ProviderConfig{
		ExecutionProfile:           "browser",
		BrowserClientModeRequested: "auto",
		BrowserClientMode:          "native",
		BrowserBackendSelected:     "official_chrome_extension",
		BrowserSelectionGeneration: 3,
		BrowserProfileGeneration:   7,
		BrowserSessionRecovered:    false,
		BrowserAssetManifestSHA256: strings.Repeat("a", 64),
		BrowserExtensionID:         "abcdefghijklmnopabcdefghijklmnop",
		BrowserExtensionVersion:    "1.2.3.4",
		BrowserNativeHostProtocol:  "openlinker.native-chrome.v2",
	})
	for key, expected := range map[string]any{
		"browser_backend_selected":      "official_chrome_extension",
		"browser_selection_generation":  uint64(3),
		"browser_asset_manifest_sha256": strings.Repeat("a", 64),
		"browser_extension_id":          "abcdefghijklmnopabcdefghijklmnop",
		"browser_extension_version":     "1.2.3.4",
		"browser_native_host_protocol":  "openlinker.native-chrome.v2",
		"browser_profile_generation":    uint64(7),
		"browser_session_recovered":     false,
	} {
		if evidence[key] != expected {
			t.Fatalf("official evidence %s = %#v, want %#v", key, evidence[key], expected)
		}
	}
	encoded, err := json.Marshal(evidence)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"/opt/", "/browser-", "profile_path", "cookie", "https://"} {
		if strings.Contains(strings.ToLower(string(encoded)), forbidden) {
			t.Fatalf("official evidence leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestBrowserAuthorityEvidenceHashesSessionAndAttachmentIdentity(t *testing.T) {
	identity := browserprotocol.Identity{
		BrowserSessionID: "44444444-4444-4444-8444-444444444444",
		SessionEpoch:     7,
		AttachmentID:     "99999999-9999-4999-8999-999999999999",
	}
	evidence := browserAuthorityEvidence(identity)
	for key, expected := range map[string]any{
		"browser_session_sha256":    "59eb27c6ed8826d6ff0da734260f2159b712802132ae1dda90b0e7591f15706d",
		"browser_session_epoch":     uint64(7),
		"browser_attachment_sha256": "855d919c701efec5e6229e8da8cc3235d68f3795432f3712392463a60fca8a93",
	} {
		if evidence[key] != expected {
			t.Fatalf("Browser authority evidence %s = %#v, want %#v", key, evidence[key], expected)
		}
	}
	encoded, err := json.Marshal(evidence)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{identity.BrowserSessionID, identity.AttachmentID} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("Browser authority evidence leaked raw identity %q: %s", forbidden, encoded)
		}
	}
}

func TestBrowserLifecycleEventBudgetIsFixed(t *testing.T) {
	root := shortBrowserTestRoot(t)
	base := &browserCaptureProvider{root: root}
	provider, err := newBrowserExecutionProvider(base, browserProviderTestConfig(root))
	if err != nil {
		t.Fatal(err)
	}
	stubBrowserPreflight(t, provider)
	run := browserProviderTestRun("44444444-4444-4444-8444-444444444444")
	var events []string
	var payloads []map[string]any
	run.Emit = func(eventType string, payload any) error {
		events = append(events, eventType)
		value, ok := payload.(map[string]any)
		if !ok {
			t.Fatalf("Browser lifecycle payload = %#v", payload)
		}
		payloads = append(payloads, value)
		return nil
	}
	result, err := provider.Run(context.Background(), run)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 ||
		events[0] != "run.browser.lifecycle" ||
		events[1] != "run.browser.lifecycle" ||
		events[2] != "run.browser.lifecycle" {
		t.Fatalf("Browser lifecycle events = %#v, want exactly three", events)
	}
	output, ok := result.Output.(map[string]any)
	if !ok || output["browser_execution_profile"] != "isolated" ||
		output["browser_tool"] != "browser_session" ||
		output["browser_interaction_policy"] != "restricted" ||
		output["browser_interaction_policy_generation"] != int64(1) ||
		output["browser_mutation_origins_sha256"] !=
			browserprotocol.RestrictedMutationOriginsSHA256 ||
		output["browser_contract_id"] != browserprotocol.ContractID {
		t.Fatalf("Browser result evidence = %#v", result.Output)
	}
	if len(base.leases) != 1 {
		t.Fatalf("Browser leases = %#v, want exactly one", base.leases)
	}
	authorityEvidence := browserAuthorityEvidence(base.leases[0].Identity)
	for _, key := range []string{
		"browser_session_sha256",
		"browser_session_epoch",
		"browser_attachment_sha256",
	} {
		if output[key] != authorityEvidence[key] {
			t.Fatalf("Browser result authority evidence %s = %#v, want %#v", key, output[key], authorityEvidence[key])
		}
		for _, index := range []int{1, 2} {
			if payloads[index][key] != authorityEvidence[key] {
				t.Fatalf("Browser lifecycle authority evidence %s at %d = %#v, want %#v", key, index, payloads[index][key], authorityEvidence[key])
			}
		}
	}
	origins, ok := output["browser_mutation_origins"].([]string)
	if !ok || len(origins) != 0 {
		t.Fatalf("Browser result mutation origins = %#v", output["browser_mutation_origins"])
	}
	for _, index := range []int{1, 2} {
		if payloads[index]["browser_interaction_policy"] != "restricted" ||
			payloads[index]["browser_interaction_policy_generation"] != int64(1) ||
			payloads[index]["browser_mutation_origins_sha256"] !=
				browserprotocol.RestrictedMutationOriginsSHA256 ||
			payloads[index]["browser_contract_id"] != browserprotocol.ContractID {
			t.Fatalf("Browser lifecycle evidence at %d = %#v", index, payloads[index])
		}
	}
	if _, ok := payloads[2]["browser_mutation_summary"].(map[string]any); !ok {
		t.Fatalf("closed Browser lifecycle mutation summary = %#v", payloads[2])
	}
}

func TestBrowserPreflightFailureNeverEmitsReadyOrStartsProvider(t *testing.T) {
	root := shortBrowserTestRoot(t)
	base := &browserCaptureProvider{root: root}
	provider, err := newBrowserExecutionProvider(
		base,
		browserProviderTestConfig(root),
	)
	if err != nil {
		t.Fatal(err)
	}
	browserProvider := provider.(*browserExecutionProvider)
	browserProvider.preflight = func(
		context.Context,
		*browserRunLease,
	) error {
		return errors.New("fixture preflight failure")
	}
	run := browserProviderTestRun("44444444-4444-4444-8444-444444444444")
	var phases []string
	run.Emit = func(eventType string, payload any) error {
		if eventType != "run.browser.lifecycle" {
			t.Fatalf("event type = %q", eventType)
		}
		value, ok := payload.(map[string]any)
		if !ok {
			t.Fatalf("payload = %#v", payload)
		}
		phases = append(phases, value["phase"].(string))
		return nil
	}
	if _, err := provider.Run(context.Background(), run); err == nil {
		t.Fatal("Browser Run succeeded despite failed preflight")
	}
	if strings.Join(phases, ",") != "preparing,failed" {
		t.Fatalf("lifecycle phases = %#v", phases)
	}
	if len(base.leases) != 0 {
		t.Fatalf("Provider started after failed preflight: %#v", base.leases)
	}
}

func TestBrowserSessionPruneIsBoundedOldestFirstAndProtectsCurrentAndActive(
	t *testing.T,
) {
	root := shortBrowserTestRoot(t)
	sessionRoot := filepath.Join(root, "sessions")
	if err := ensurePrivateBrowserDirectory(sessionRoot); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	writeState := func(name, browserSessionID string, updatedAt time.Time) string {
		t.Helper()
		path := filepath.Join(sessionRoot, name+".json")
		if err := writePrivateBrowserJSON(path, browserSessionState{
			Version:             browserSessionStateVersion,
			SessionKeyHash:      name,
			BrowserSessionID:    browserSessionID,
			SessionEpoch:        1,
			InteractionPolicy:   "restricted",
			PolicyGeneration:    1,
			MutationOriginsHash: browserprotocol.RestrictedMutationOriginsSHA256,
			UpdatedAt:           updatedAt.Format(time.RFC3339Nano),
		}); err != nil {
			t.Fatal(err)
		}
		return path
	}
	currentPath := writeState(
		"current",
		"44444444-4444-4444-8444-444444444444",
		now.Add(-40*24*time.Hour),
	)
	activeSessionID := "55555555-5555-4555-8555-555555555555"
	activePath := writeState(
		"active",
		activeSessionID,
		now.Add(-40*24*time.Hour),
	)
	expiredPath := writeState(
		"expired",
		"66666666-6666-4666-8666-666666666666",
		now.Add(-31*24*time.Hour),
	)
	oldestCapacityPath := writeState(
		"oldest-capacity",
		"77777777-7777-4777-8777-777777777777",
		now.Add(-20*24*time.Hour),
	)
	for index := 0; index < browserprotocol.BrowserSessionStateLimit-2; index++ {
		writeState(
			fmt.Sprintf("fresh-%03d", index),
			fmt.Sprintf("88888888-8888-4888-8888-%012d", index),
			now.Add(time.Duration(index)*time.Second),
		)
	}
	if err := writePrivateBrowserJSON(
		filepath.Join(root, "active-lease.json"),
		browserclient.Lease{
			ContractID: browserclient.LeaseContractID,
			ExpiresAt:  now.Add(time.Hour),
			Identity: browserprotocol.Identity{
				RunID:                              "11111111-1111-4111-8111-111111111111",
				AgentID:                            "22222222-2222-4222-8222-222222222222",
				PrincipalScopeID:                   "33333333-3333-4333-8333-333333333333",
				BrowserSessionID:                   activeSessionID,
				SessionEpoch:                       1,
				AttachmentID:                       "99999999-9999-4999-8999-999999999999",
				ControlEpoch:                       1,
				Controller:                         browserprotocol.ControllerAgent,
				BrowserInteractionPolicy:           "restricted",
				BrowserInteractionPolicyGeneration: 1,
				BrowserMutationOrigins:             []string{},
				BrowserMutationOriginsSHA256:       browserprotocol.RestrictedMutationOriginsSHA256,
			},
		},
	); err != nil {
		t.Fatal(err)
	}
	if err := pruneBrowserSessionStates(root, currentPath, now); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{currentPath, activePath} {
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("protected Session state %s was removed: %v", path, err)
		}
	}
	for _, path := range []string{expiredPath, oldestCapacityPath} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("prunable Session state %s remained: %v", path, err)
		}
	}
	entries, err := os.ReadDir(sessionRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != browserprotocol.BrowserSessionStateLimit {
		t.Fatalf("Session files = %d, want %d", len(entries), browserprotocol.BrowserSessionStateLimit)
	}
}

func TestBrowserSessionPruneRejectsUnboundedDirectoryWithoutDeleting(t *testing.T) {
	root := shortBrowserTestRoot(t)
	sessionRoot := filepath.Join(root, "sessions")
	if err := ensurePrivateBrowserDirectory(sessionRoot); err != nil {
		t.Fatal(err)
	}
	for index := 0; index <= browserprotocol.BrowserSessionStateScanLimit; index++ {
		path := filepath.Join(sessionRoot, fmt.Sprintf("%03d.json", index))
		if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	err := pruneBrowserSessionStates(
		root,
		filepath.Join(sessionRoot, "current.json"),
		time.Now().UTC(),
	)
	if err == nil || !strings.Contains(err.Error(), "bounded prune scan limit") {
		t.Fatalf("prune error = %v", err)
	}
	entries, readErr := os.ReadDir(sessionRoot)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != browserprotocol.BrowserSessionStateScanLimit+1 {
		t.Fatalf("bounded prune deleted state before rejecting: %d files", len(entries))
	}
}

func TestBrowserSessionPruneDoesNotProtectExpiredActiveLease(t *testing.T) {
	root := shortBrowserTestRoot(t)
	sessionRoot := filepath.Join(root, "sessions")
	if err := ensurePrivateBrowserDirectory(sessionRoot); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	sessionID := "55555555-5555-4555-8555-555555555555"
	statePath := filepath.Join(sessionRoot, "expired-active.json")
	if err := writePrivateBrowserJSON(statePath, browserSessionState{
		Version:             browserSessionStateVersion,
		SessionKeyHash:      "expired-active",
		BrowserSessionID:    sessionID,
		SessionEpoch:        1,
		InteractionPolicy:   "restricted",
		PolicyGeneration:    1,
		MutationOriginsHash: browserprotocol.RestrictedMutationOriginsSHA256,
		UpdatedAt:           now.Add(-31 * 24 * time.Hour).Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatal(err)
	}
	if err := writePrivateBrowserJSON(
		filepath.Join(root, "active-lease.json"),
		browserclient.Lease{
			ContractID: browserclient.LeaseContractID,
			ExpiresAt:  now.Add(-time.Minute),
			Identity: browserprotocol.Identity{
				RunID:                              "11111111-1111-4111-8111-111111111111",
				AgentID:                            "22222222-2222-4222-8222-222222222222",
				PrincipalScopeID:                   "33333333-3333-4333-8333-333333333333",
				BrowserSessionID:                   sessionID,
				SessionEpoch:                       1,
				AttachmentID:                       "99999999-9999-4999-8999-999999999999",
				ControlEpoch:                       1,
				Controller:                         browserprotocol.ControllerAgent,
				BrowserInteractionPolicy:           "restricted",
				BrowserInteractionPolicyGeneration: 1,
				BrowserMutationOrigins:             []string{},
				BrowserMutationOriginsSHA256:       browserprotocol.RestrictedMutationOriginsSHA256,
			},
		},
	); err != nil {
		t.Fatal(err)
	}

	if err := pruneBrowserSessionStates(
		root,
		filepath.Join(sessionRoot, "current.json"),
		now,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(statePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expired active Session state remained: %v", err)
	}
}

func stubBrowserPreflight(t *testing.T, provider Provider) {
	t.Helper()
	browserProvider, ok := provider.(*browserExecutionProvider)
	if !ok {
		t.Fatalf("provider = %T, want *browserExecutionProvider", provider)
	}
	preflight := func(
		context.Context,
		*browserRunLease,
	) error {
		return nil
	}
	browserProvider.preflight = preflight
	browserProvider.repreflight = preflight
}

func browserProviderTestConfig(root string) ProviderConfig {
	return ProviderConfig{
		Provider:                 "codex",
		ExecutionProfile:         "browser",
		BrowserInteractionPolicy: "restricted",
		BrowserPluginBin:         "/opt/openlinker",
		BrowserSocket:            "/browser/control.sock",
		BrowserCredentialFile:    "/browser/channel",
		BrowserLeaseRoot:         root,
		BrowserBrokerRoot:        filepath.Join(root, "broker"),
		Timeout:                  time.Minute,
	}
}

func shortBrowserTestRoot(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "olb-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	return root
}

func browserProviderTestRun(runID string) RunContext {
	return RunContext{
		RunID:         runID,
		AgentID:       "11111111-1111-4111-8111-111111111111",
		RunDeadlineAt: time.Now().Add(time.Minute),
		Authority: &openlinker.RuntimeAuthorityContext{
			PrincipalScopeID:                   "22222222-2222-4222-8222-222222222222",
			RuntimeSessionID:                   "33333333-3333-4333-8333-333333333333",
			RuntimeSessionEpoch:                1,
			RuntimeAttachmentID:                "99999999-9999-4999-8999-999999999999",
			ExecutionProfile:                   "browser",
			BrowserInteractionPolicy:           "restricted",
			BrowserInteractionPolicyGeneration: 1,
			BrowserMutationOrigins:             []string{},
			BrowserMutationOriginsSHA256:       browserprotocol.RestrictedMutationOriginsSHA256,
		},
		Conversation: &ConversationContext{
			ID:           "conversation-one",
			SessionKey:   "conversation-one",
			CurrentRunID: runID,
			Source:       "core",
		},
	}
}
