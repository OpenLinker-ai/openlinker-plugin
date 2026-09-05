package browserclientmode

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	ModeAuto                   = "auto"
	ModeOpenLinkerNativeChrome = "openlinker-native-chrome"
	ModeOfficialChromeAlias    = "official-chrome"
	// ModeOfficialChrome is retained for source compatibility. New callers
	// should use ModeOpenLinkerNativeChrome.
	ModeOfficialChrome = ModeOfficialChromeAlias
	ModeIsolatedNative = "isolated-native"
	ModeIsolatedMCP    = "isolated-mcp"
	ModeNativeAlias    = "native"
	ModeMCPAlias       = "mcp"

	SurfacePluginNative = "native"
	SurfaceDirectMCP    = "mcp"

	BackendAuto           = "auto"
	BackendOfficialChrome = "openlinker-native-chrome"
	BackendIsolated       = "isolated"
)

type RunHostCommand func(args ...string) ([]byte, error)

type Options struct {
	Provider         string
	Requested        string
	PluginPath       string
	RequireImmutable bool
	RunHostCommand   RunHostCommand
	Platform         string
	NativePreflight  func() (string, error)
}

type Selection struct {
	Requested        string
	Selected         string
	BackendRequested string
	PluginPath       string
	FallbackReason   string
}

func Select(options Options) (Selection, error) {
	selection := Selection{
		Requested:        canonicalRequestedMode(options.Requested),
		Selected:         canonicalRequestedMode(options.Requested),
		BackendRequested: BackendIsolated,
		PluginPath:       filepath.Clean(strings.TrimSpace(options.PluginPath)),
	}
	if selection.Requested == "" {
		selection.Requested = ModeMCPAlias
		selection.Selected = SurfaceDirectMCP
	}
	switch selection.Requested {
	case ModeMCPAlias, ModeIsolatedMCP:
		selection.Selected = SurfaceDirectMCP
		selection.PluginPath = ""
		return selection, nil
	case ModeNativeAlias, ModeIsolatedNative:
		selection.Selected = SurfacePluginNative
	case ModeOpenLinkerNativeChrome:
		selection.Selected = SurfacePluginNative
		selection.BackendRequested = BackendOfficialChrome
		if !officialChromePlatform(options.Provider, options.Platform) {
			return Selection{}, errors.New(
				"strict official Chrome Browser client is unavailable (official_platform_unsupported)",
			)
		}
	case ModeAuto:
		selection.Selected = SurfacePluginNative
		if officialChromePlatform(options.Provider, options.Platform) {
			selection.BackendRequested = BackendAuto
		}
	default:
		return Selection{}, errors.New(
			"OPENLINKER_BROWSER_CLIENT_MODE must be auto, openlinker-native-chrome, isolated-native, isolated-mcp, native, or mcp (official-chrome is a compatibility alias)",
		)
	}
	if options.NativePreflight == nil && options.RunHostCommand == nil {
		return Selection{}, errors.New("Browser Plugin host command runner is unavailable")
	}

	var reason string
	var err error
	if options.NativePreflight != nil {
		reason, err = options.NativePreflight()
	} else {
		reason, err = prepareNativeBrowserPlugin(
			options.Provider,
			selection.PluginPath,
			options.RequireImmutable,
			options.RunHostCommand,
		)
	}
	if err == nil {
		selection.Selected = SurfacePluginNative
		return selection, nil
	}
	if selection.Requested != ModeAuto {
		return Selection{}, fmt.Errorf(
			"strict native Browser client is unavailable (%s)",
			reason,
		)
	}
	selection.Selected = SurfaceDirectMCP
	selection.BackendRequested = BackendIsolated
	selection.PluginPath = ""
	selection.FallbackReason = reason
	return selection, nil
}

func canonicalRequestedMode(value string) string {
	value = strings.TrimSpace(value)
	if value == ModeOfficialChromeAlias {
		return ModeOpenLinkerNativeChrome
	}
	return value
}

func officialChromePlatform(provider, platform string) bool {
	if platform = strings.TrimSpace(platform); platform == "" {
		platform = runtime.GOOS
	}
	return strings.TrimSpace(provider) == "codex" && platform == "linux"
}

func prepareNativeBrowserPlugin(
	provider,
	pluginPath string,
	requireImmutable bool,
	runHostCommand RunHostCommand,
) (string, error) {
	if err := validateAgentRuntimePlugin(
		provider,
		pluginPath,
		requireImmutable,
	); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "native_bundle_unavailable", err
		}
		return "native_bundle_invalid", err
	}
	switch provider {
	case "codex":
		if _, err := runHostCommand(
			"plugin",
			"marketplace",
			"add",
			pluginPath,
			"--json",
		); err != nil {
			return "native_activation_failed", err
		}
		if _, err := runHostCommand(
			"plugin",
			"add",
			"openlinker@openlinker-agent-runtime",
			"--json",
		); err != nil {
			return "native_activation_failed", err
		}
		raw, err := runHostCommand("plugin", "list", "--json")
		if err != nil {
			return "native_host_incompatible", err
		}
		if err := validateCodexPluginList(raw, pluginPath); err != nil {
			return "native_tool_handshake_failed", err
		}
	case "claude":
		if _, err := runHostCommand(
			"plugin",
			"validate",
			"--strict",
			pluginPath,
		); err != nil {
			return "native_host_incompatible", err
		}
	default:
		return "native_host_incompatible", errors.New(
			"image provider is not fixed to codex or claude",
		)
	}
	return "", nil
}

func validateAgentRuntimePlugin(
	provider,
	root string,
	requireImmutable bool,
) error {
	if !filepath.IsAbs(root) {
		return errors.New("native Browser Plugin path must be absolute")
	}
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("native Browser Plugin root must be a real directory")
	}

	expected := expectedAgentRuntimePluginFiles(provider)
	if expected == nil {
		return errors.New("image provider is not fixed to codex or claude")
	}
	seen := make(map[string]struct{}, len(expected))
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return validateImmutablePluginEntry(path, entry, requireImmutable)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("native Browser Plugin contains a symlink")
		}
		if err := validateImmutablePluginEntry(path, entry, requireImmutable); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		relative = filepath.ToSlash(relative)
		if _, ok := expected[relative]; !ok {
			return errors.New("native Browser Plugin contains an unexpected file")
		}
		seen[relative] = struct{}{}
		if entry.Type().IsRegular() {
			raw, err := os.ReadFile(path) // #nosec G304 -- path is below the validated immutable Plugin root.
			if err != nil {
				return err
			}
			if len(raw) > 1<<20 {
				return errors.New("native Browser Plugin file exceeded the limit")
			}
			for _, forbidden := range []string{
				"OPENLINKER_USER_TOKEN",
				"OPENLINKER_AGENT_TOKEN",
				"CODEX_API_KEY",
				"ANTHROPIC_API_KEY",
			} {
				if bytes.Contains(raw, []byte(forbidden)) {
					return errors.New(
						"native Browser Plugin contains a forbidden credential declaration",
					)
				}
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if len(seen) != len(expected) {
		return errors.New("native Browser Plugin is missing a required file")
	}

	mcpPath := filepath.Join(root, ".mcp.json")
	manifestPath := filepath.Join(root, ".claude-plugin", "plugin.json")
	if provider == "codex" {
		mcpPath = filepath.Join(root, "plugins", "openlinker", ".mcp.json")
		manifestPath = filepath.Join(
			root,
			"plugins",
			"openlinker",
			".codex-plugin",
			"plugin.json",
		)
	}
	if err := validateBrowserOnlyMCP(mcpPath, provider); err != nil {
		return err
	}
	return validatePluginManifest(manifestPath, provider)
}

func expectedAgentRuntimePluginFiles(provider string) map[string]struct{} {
	var values []string
	switch provider {
	case "codex":
		values = []string{
			".agents/plugins/marketplace.json",
			"plugins/openlinker/.codex-plugin/plugin.json",
			"plugins/openlinker/.mcp.json",
			"plugins/openlinker/LICENSE",
			"plugins/openlinker/skills/use-isolated-browser/SKILL.md",
			"plugins/openlinker/skills/use-isolated-browser/agents/openai.yaml",
		}
	case "claude":
		values = []string{
			".claude-plugin/plugin.json",
			".mcp.json",
			"LICENSE",
			"commands/use-isolated-browser.md",
			"skills/use-isolated-browser/SKILL.md",
		}
	default:
		return nil
	}
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

func validateBrowserOnlyMCP(path, provider string) error {
	raw, err := os.ReadFile(path) // #nosec G304 -- fixed path below validated Plugin root.
	if err != nil {
		return err
	}
	var document struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := decodeStrictBrowserClientJSON(raw, &document); err != nil {
		return errors.New("native Browser Plugin MCP declaration is invalid")
	}
	if len(document.MCPServers) != 1 {
		return errors.New("native Browser Plugin must declare exactly one MCP server")
	}
	serverRaw, ok := document.MCPServers["openlinker_browser"]
	if !ok {
		return errors.New("native Browser Plugin is missing openlinker_browser")
	}
	if provider == "codex" {
		var server struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
			Cwd     string   `json:"cwd"`
			EnvVars []string `json:"env_vars"`
		}
		if err := decodeStrictBrowserClientJSON(serverRaw, &server); err != nil ||
			server.Command != "/usr/local/bin/openlinker" ||
			server.Cwd != "/workspace" ||
			!equalStrings(server.Args, []string{"plugin", "browser-proxy", "--host", "codex"}) ||
			!equalStrings(server.EnvVars, []string{"OPENLINKER_BROWSER_TOOL_SOCKET"}) {
			return errors.New("Codex Browser MCP declaration is not the bounded Runtime contract")
		}
		return nil
	}
	var server struct {
		Command string            `json:"command"`
		Args    []string          `json:"args"`
		Env     map[string]string `json:"env"`
	}
	if err := decodeStrictBrowserClientJSON(serverRaw, &server); err != nil ||
		server.Command != "/usr/local/bin/openlinker" ||
		!equalStrings(server.Args, []string{"plugin", "browser-proxy", "--host", "claude"}) ||
		len(server.Env) != 1 ||
		server.Env["OPENLINKER_BROWSER_TOOL_SOCKET"] != "${OPENLINKER_BROWSER_TOOL_SOCKET}" {
		return errors.New("Claude Browser MCP declaration is not the bounded Runtime contract")
	}
	return nil
}

func validatePluginManifest(path, provider string) error {
	raw, err := os.ReadFile(path) // #nosec G304 -- fixed path below validated Plugin root.
	if err != nil {
		return err
	}
	var manifest map[string]any
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return errors.New("native Browser Plugin manifest is invalid")
	}
	if manifest["name"] != "openlinker" {
		return errors.New("native Browser Plugin manifest has an unexpected name")
	}
	if provider == "codex" {
		if manifest["mcpServers"] != "./.mcp.json" ||
			manifest["skills"] != "./skills/" {
			return errors.New("Codex Browser Plugin manifest is incomplete")
		}
		pluginInterface, ok := manifest["interface"].(map[string]any)
		if !ok ||
			!nonEmptyManifestString(pluginInterface["displayName"]) ||
			!nonEmptyManifestString(pluginInterface["shortDescription"]) ||
			!nonEmptyManifestString(pluginInterface["longDescription"]) ||
			!nonEmptyManifestString(pluginInterface["developerName"]) ||
			!nonEmptyManifestString(pluginInterface["category"]) ||
			!nonEmptyManifestStringSlice(pluginInterface["capabilities"]) ||
			!nonEmptyManifestStringSlice(pluginInterface["defaultPrompt"]) {
			return errors.New(
				"Codex Browser Plugin interface metadata is incomplete",
			)
		}
	} else if manifest["skills"] != "./skills/" ||
		manifest["commands"] != "./commands/" {
		return errors.New("Claude Browser Plugin manifest is incomplete")
	}
	return nil
}

func nonEmptyManifestString(value any) bool {
	text, ok := value.(string)
	return ok && strings.TrimSpace(text) != ""
}

func nonEmptyManifestStringSlice(value any) bool {
	values, ok := value.([]any)
	if !ok || len(values) == 0 {
		return false
	}
	for _, value := range values {
		if !nonEmptyManifestString(value) {
			return false
		}
	}
	return true
}

func validateCodexPluginList(raw []byte, expectedRoot string) error {
	var list struct {
		Installed []struct {
			PluginID        string `json:"pluginId"`
			Name            string `json:"name"`
			MarketplaceName string `json:"marketplaceName"`
			Version         string `json:"version"`
			Installed       bool   `json:"installed"`
			Enabled         bool   `json:"enabled"`
			Source          struct {
				Source string `json:"source"`
				Path   string `json:"path"`
			} `json:"source"`
			MarketplaceSource json.RawMessage `json:"marketplaceSource"`
			InstallPolicy     string          `json:"installPolicy"`
			AuthPolicy        string          `json:"authPolicy"`
		} `json:"installed"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return errors.New("Codex Plugin list is invalid")
	}
	if len(list.Installed) != 1 {
		return errors.New("Codex Provider home contains an unexpected Plugin")
	}
	plugin := list.Installed[0]
	expectedSource := filepath.Join(expectedRoot, "plugins", "openlinker")
	if plugin.PluginID != "openlinker@openlinker-agent-runtime" ||
		plugin.Name != "openlinker" ||
		plugin.MarketplaceName != "openlinker-agent-runtime" ||
		!plugin.Installed ||
		!plugin.Enabled ||
		plugin.Source.Source != "local" ||
		filepath.Clean(plugin.Source.Path) != expectedSource {
		return errors.New("Codex Browser Plugin activation did not match the immutable artifact")
	}
	return nil
}

func decodeStrictBrowserClientJSON(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("JSON document has trailing data")
	}
	return nil
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
