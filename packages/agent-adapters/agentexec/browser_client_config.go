package agentexec

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

var browserEnvironmentNames = []string{
	"OPENLINKER_BROWSER_TOOL_SOCKET",
}

var browserClientFallbackReasons = map[string]struct{}{
	"native_bundle_unavailable":    {},
	"native_bundle_invalid":        {},
	"native_host_incompatible":     {},
	"native_activation_failed":     {},
	"native_tool_handshake_failed": {},
}

var browserBackendFallbackReasons = map[string]struct{}{
	"official_assets_unavailable":             {},
	"official_assets_invalid":                 {},
	"official_platform_unsupported":           {},
	"official_sandbox_unavailable":            {},
	"official_chrome_start_failed":            {},
	"official_extension_unavailable":          {},
	"official_native_host_unavailable":        {},
	"official_protocol_mismatch":              {},
	"official_capability_incomplete":          {},
	"official_profile_preflight_failed":       {},
	"official_policy_enforcement_unavailable": {},
	"official_egress_preflight_failed":        {},
}

const (
	browserModeOpenLinkerNativeChrome = "openlinker-native-chrome"
	browserModeOfficialChromeAlias    = "official-chrome"
)

func canonicalBrowserClientMode(value string) string {
	value = strings.TrimSpace(value)
	if value == browserModeOfficialChromeAlias {
		return browserModeOpenLinkerNativeChrome
	}
	return value
}

func canonicalBrowserBackendMode(value string) string {
	return canonicalBrowserClientMode(value)
}

func providerConfigForBrowserRun(
	config ProviderConfig,
	run *BrowserRunContext,
) ProviderConfig {
	if run == nil {
		return config
	}
	config.ExecutionProfile = "browser"
	config.BrowserPluginBin = run.PluginBin
	config.BrowserBackendSelected = run.BackendSelected
	config.BrowserBackendFallbackReason = run.BackendFallbackReason
	config.BrowserSelectionGeneration = run.SelectionGeneration
	config.BrowserProfileGeneration = run.ProfileGeneration
	config.BrowserSessionRecovered = run.SessionRecovered
	config.BrowserAssetManifestSHA256 = run.AssetManifestSHA256
	config.BrowserExtensionID = run.ExtensionID
	config.BrowserExtensionVersion = run.ExtensionVersion
	config.BrowserNativeHostProtocol = run.NativeHostProtocol
	config.Env = setEnvironmentValues(config.Env, map[string]string{
		"OPENLINKER_BROWSER_TOOL_SOCKET": run.ToolSocket,
	})
	for _, name := range browserEnvironmentNames {
		config.EnvAllowlist = appendUniqueString(config.EnvAllowlist, name)
	}
	return config
}

func browserProfileEnabled(config ProviderConfig) bool {
	return strings.EqualFold(strings.TrimSpace(config.ExecutionProfile), "browser")
}

func browserClientMode(config ProviderConfig) string {
	if !browserProfileEnabled(config) {
		return ""
	}
	switch canonicalBrowserClientMode(config.BrowserClientMode) {
	case "native", "isolated-native", browserModeOpenLinkerNativeChrome:
		return "native"
	default:
		return "mcp"
	}
}

func nativeBrowserClientEnabled(config ProviderConfig) bool {
	return browserClientMode(config) == "native"
}

func directMCPBrowserClientEnabled(config ProviderConfig) bool {
	return browserClientMode(config) == "mcp"
}

func validateBrowserClientConfig(config ProviderConfig) error {
	if !browserProfileEnabled(config) {
		return nil
	}
	requested := canonicalBrowserClientMode(config.BrowserClientModeRequested)
	if requested == "" {
		requested = "mcp"
	}
	switch requested {
	case "auto", browserModeOpenLinkerNativeChrome, "isolated-native", "isolated-mcp", "native", "mcp":
	default:
		return errors.New("Browser client mode request is invalid")
	}
	selected := strings.TrimSpace(config.BrowserClientMode)
	if selected == "" {
		selected = "mcp"
	}
	if selected != "native" && selected != "mcp" {
		return errors.New("effective Browser client mode must be native or mcp")
	}
	strictSurface := ""
	switch requested {
	case browserModeOpenLinkerNativeChrome, "isolated-native", "native":
		strictSurface = "native"
	case "isolated-mcp", "mcp":
		strictSurface = "mcp"
	}
	if strictSurface != "" && strictSurface != selected {
		return errors.New("strict Browser client mode cannot select another surface")
	}
	fallback := strings.TrimSpace(config.BrowserClientFallbackReason)
	if fallback != "" {
		if requested != "auto" || selected != "mcp" {
			return errors.New("Browser fallback evidence requires auto to select mcp")
		}
		if _, ok := browserClientFallbackReasons[fallback]; !ok {
			return errors.New("Browser fallback reason is invalid")
		}
	}
	if selected == "native" && strings.TrimSpace(config.BrowserNativePlugin) == "" {
		return errors.New("native Browser client mode requires a Runtime-owned Plugin path")
	}
	backendMode := canonicalBrowserBackendMode(config.BrowserBackendModeRequested)
	if backendMode == "" {
		backendMode = requestedBackendMode(requested)
	}
	if backendMode != "auto" && backendMode != browserModeOpenLinkerNativeChrome && backendMode != "isolated" {
		return errors.New("Browser backend mode request is invalid")
	}
	if selected == "mcp" && backendMode != "isolated" {
		return errors.New("direct MCP Browser surface requires the isolated backend")
	}
	if requested == browserModeOpenLinkerNativeChrome && backendMode != browserModeOpenLinkerNativeChrome {
		return errors.New("strict official Chrome mode cannot select another backend")
	}
	if (requested == "native" || requested == "isolated-native" ||
		requested == "mcp" || requested == "isolated-mcp") && backendMode != "isolated" {
		return errors.New("strict isolated Browser mode cannot select another backend")
	}
	backend := strings.TrimSpace(config.BrowserBackendSelected)
	if backend != "" && backend != "official_chrome_extension" && backend != "isolated_chromium" {
		return errors.New("effective Browser backend is invalid")
	}
	if backend == "official_chrome_extension" &&
		(selected != "native" || backendMode == "isolated") {
		return errors.New("official Chrome backend requires the native Plugin surface")
	}
	if backend == "official_chrome_extension" && config.BrowserProfileGeneration == 0 {
		return errors.New("official Chrome backend requires positive Profile generation evidence")
	}
	if backend == "isolated_chromium" && backendMode == browserModeOpenLinkerNativeChrome {
		return errors.New("strict official Chrome mode cannot select the isolated backend")
	}
	backendFallback := strings.TrimSpace(config.BrowserBackendFallbackReason)
	if backendFallback != "" {
		if requested != "auto" || backend != "isolated_chromium" || selected != "native" {
			return errors.New("Browser backend fallback evidence is invalid")
		}
		if _, ok := browserBackendFallbackReasons[backendFallback]; !ok {
			return errors.New("Browser backend fallback reason is invalid")
		}
	}
	return nil
}

func requestedBackendMode(requested string) string {
	switch strings.TrimSpace(requested) {
	case "auto":
		return "auto"
	case browserModeOpenLinkerNativeChrome:
		return browserModeOpenLinkerNativeChrome
	default:
		return "isolated"
	}
}

func browserClientEvidence(config ProviderConfig) map[string]any {
	requested := canonicalBrowserClientMode(config.BrowserClientModeRequested)
	if requested == "" {
		requested = "mcp"
	}
	selected := "direct_mcp"
	if nativeBrowserClientEnabled(config) {
		selected = "plugin_native"
	}
	evidence := map[string]any{
		"browser_client_mode_requested": requested,
		"browser_client_mode_selected":  selected,
	}
	backend := strings.TrimSpace(config.BrowserBackendSelected)
	if backend == "" &&
		(requestedBackendMode(requested) == "isolated" || selected == "direct_mcp") {
		backend = "isolated_chromium"
	}
	if backend != "" {
		evidence["browser_backend_selected"] = backend
	}
	if config.BrowserSelectionGeneration > 0 {
		evidence["browser_selection_generation"] = config.BrowserSelectionGeneration
	}
	if backend == "official_chrome_extension" {
		evidence["browser_asset_manifest_sha256"] = config.BrowserAssetManifestSHA256
		evidence["browser_extension_id"] = config.BrowserExtensionID
		evidence["browser_extension_version"] = config.BrowserExtensionVersion
		evidence["browser_native_host_protocol"] = config.BrowserNativeHostProtocol
		evidence["browser_profile_generation"] = config.BrowserProfileGeneration
		evidence["browser_session_recovered"] = config.BrowserSessionRecovered
	}
	if fallback := strings.TrimSpace(config.BrowserClientFallbackReason); fallback != "" {
		evidence["browser_client_mode_fallback_reason"] = fallback
	}
	if fallback := strings.TrimSpace(config.BrowserBackendFallbackReason); fallback != "" {
		evidence["browser_backend_fallback_reason"] = fallback
	}
	return evidence
}

func codexBrowserMCPArguments(config ProviderConfig) []string {
	if nativeBrowserClientEnabled(config) {
		// The Runtime-owned Codex Plugin declares the MCP transport. Per-run
		// overrides only make that bundled server mandatory and bound its tool
		// policy; declaring a top-level command here would create a second,
		// direct-MCP Browser surface.
		const server = `plugins."openlinker@openlinker-agent-runtime".mcp_servers.openlinker_browser`
		return []string{
			"-c", server + ".enabled=true",
			"-c", server + ".required=true",
			"-c", server + `.enabled_tools=["browser_session"]`,
			"-c", server + `.default_tools_approval_mode="auto"`,
		}
	}
	if !directMCPBrowserClientEnabled(config) {
		return nil
	}
	command, _ := json.Marshal(config.BrowserPluginBin)
	arguments, _ := json.Marshal([]string{
		"plugin",
		"browser-proxy",
		"--host",
		"codex",
	})
	environment, _ := json.Marshal(browserEnvironmentNames)
	return []string{
		"-c", "mcp_servers.openlinker_browser.command=" + string(command),
		"-c", "mcp_servers.openlinker_browser.args=" + string(arguments),
		"-c", "mcp_servers.openlinker_browser.env_vars=" + string(environment),
		"-c", "mcp_servers.openlinker_browser.required=true",
		"-c", `mcp_servers.openlinker_browser.enabled_tools=["browser_session"]`,
		"-c", `mcp_servers.openlinker_browser.default_tools_approval_mode="auto"`,
	}
}

func claudeBrowserMCPConfig(config ProviderConfig) string {
	if !directMCPBrowserClientEnabled(config) {
		return ""
	}
	environment := map[string]string{}
	for _, item := range config.Env {
		key, value, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		for _, allowed := range browserEnvironmentNames {
			if key == allowed {
				environment[key] = value
			}
		}
	}
	payload := map[string]any{
		"mcpServers": map[string]any{
			"openlinker_browser": map[string]any{
				"type":    "stdio",
				"command": config.BrowserPluginBin,
				"args": []string{
					"plugin",
					"browser-proxy",
					"--host",
					"claude",
				},
				"env": environment,
			},
		},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		panic(fmt.Sprintf("marshal static Browser MCP configuration: %v", err))
	}
	return string(raw)
}

func setEnvironmentValues(environment []string, values map[string]string) []string {
	result := make([]string, 0, len(environment)+len(values))
	for _, item := range environment {
		key, _, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		if _, replaced := values[key]; !replaced {
			result = append(result, item)
		}
	}
	for _, key := range browserEnvironmentNames {
		if value, ok := values[key]; ok {
			result = append(result, key+"="+value)
		}
	}
	return result
}

func appendUniqueString(values []string, candidate string) []string {
	for _, value := range values {
		if value == candidate {
			return values
		}
	}
	return append(values, candidate)
}
