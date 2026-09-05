package agent

import (
	"path/filepath"
	"strings"
)

type ConfigureOptions struct {
	// ChangedFields, when non-nil, is the exact set of configuration fields to
	// update, using their command flag names. It preserves explicit zero, empty,
	// and false values. Nil retains the non-empty patch behavior of MCP callers.
	ChangedFields            []string
	Enabled                  *bool
	Provider                 string
	AgentID                  string
	Workspace                string
	OpenLinkerURL            string
	StateDir                 string
	ProviderBin              string
	Model                    string
	Transport                string
	Capacity                 int64
	TimeoutSeconds           int
	SessionReuse             *bool
	WebSearch                *bool
	CodexBaseURL             string
	CodexSandbox             string
	CodexApproval            string
	ClaudePermission         string
	AllowedTools             []string
	ExecutionProfile         string
	BrowserInteractionPolicy string
	BrowserClientMode        string
	BrowserPluginBin         string
	BrowserNativePlugin      string
	BrowserSocket            string
	BrowserCredentialFile    string
	BrowserLeaseRoot         string
	BrowserBrokerRoot        string
}

func ConfigureNonSecret(getenv func(string) string, options ConfigureOptions) (Config, string, error) {
	selected := make(map[string]bool, len(options.ChangedFields))
	for _, name := range options.ChangedFields {
		selected[name] = true
	}
	provided := func(name string, legacy bool) bool {
		if options.ChangedFields != nil {
			return selected[name]
		}
		return legacy
	}
	config, path, err := loadConfig(getenv)
	if err != nil {
		return Config{}, path, err
	}
	if value := strings.TrimSpace(options.Provider); provided("provider", value != "") {
		config.Provider = strings.ToLower(value)
	}
	if value := strings.TrimSpace(options.AgentID); provided("agent-id", value != "") {
		config.AgentID = value
	}
	if value := strings.TrimSpace(options.Workspace); provided("workspace", value != "") {
		config.Workspace, err = filepath.Abs(value)
		if err != nil {
			return Config{}, path, err
		}
	}
	if value := strings.TrimSpace(options.OpenLinkerURL); provided("url", value != "") {
		config.OpenLinkerURL = value
	}
	if value := strings.TrimSpace(options.StateDir); provided("state-dir", value != "") {
		config.StateDir, err = filepath.Abs(value)
		if err != nil {
			return Config{}, path, err
		}
	}
	if value := strings.TrimSpace(options.ProviderBin); provided("provider-bin", value != "") {
		config.ProviderBin = value
	}
	if value := strings.TrimSpace(options.Model); provided("model", value != "") {
		config.Model = value
	}
	if value := strings.TrimSpace(options.Transport); provided("transport", value != "") {
		config.Transport = strings.ToLower(value)
	}
	if provided("capacity", options.Capacity != 0) {
		config.Capacity = options.Capacity
	}
	if provided("timeout", options.TimeoutSeconds != 0) {
		config.TimeoutSeconds = options.TimeoutSeconds
	}
	if provided("session-reuse", options.SessionReuse != nil) && options.SessionReuse != nil {
		config.SessionReuse = *options.SessionReuse
	}
	if provided("web-search", options.WebSearch != nil) && options.WebSearch != nil {
		config.WebSearch = *options.WebSearch
	}
	if value := strings.TrimSpace(options.CodexBaseURL); provided("codex-base-url", value != "") {
		config.CodexBaseURL = value
	}
	if value := strings.TrimSpace(options.CodexSandbox); provided("codex-sandbox", value != "") {
		config.CodexSandbox = value
		if options.ChangedFields != nil {
			config.CodexSandbox = options.CodexSandbox
		}
	}
	if value := strings.TrimSpace(options.CodexApproval); provided("codex-approval", value != "") {
		config.CodexApproval = value
		if options.ChangedFields != nil {
			config.CodexApproval = options.CodexApproval
		}
	}
	if value := strings.TrimSpace(options.ClaudePermission); provided("claude-permission", value != "") {
		config.ClaudePermission = value
		if options.ChangedFields != nil {
			config.ClaudePermission = options.ClaudePermission
		}
	}
	if provided("allowed-tool", options.AllowedTools != nil) {
		config.AllowedTools = append([]string(nil), options.AllowedTools...)
	}
	if value := strings.TrimSpace(options.ExecutionProfile); provided("execution-profile", value != "") {
		config.ExecutionProfile = strings.ToLower(value)
	}
	if value := strings.TrimSpace(options.BrowserInteractionPolicy); provided("browser-interaction-policy", value != "") {
		config.BrowserInteractionPolicy = strings.ToLower(value)
	}
	if value := strings.TrimSpace(options.BrowserClientMode); provided("browser-client-mode", value != "") {
		config.BrowserClientMode = strings.ToLower(value)
	}
	if value := strings.TrimSpace(options.BrowserPluginBin); provided("browser-plugin-bin", value != "") {
		config.BrowserPluginBin = value
	}
	if value := strings.TrimSpace(options.BrowserNativePlugin); provided("browser-native-plugin", value != "") {
		config.BrowserNativePlugin, err = filepath.Abs(value)
		if err != nil {
			return Config{}, path, err
		}
	}
	if value := strings.TrimSpace(options.BrowserSocket); provided("browser-socket", value != "") {
		config.BrowserSocket, err = filepath.Abs(value)
		if err != nil {
			return Config{}, path, err
		}
	}
	if value := strings.TrimSpace(options.BrowserCredentialFile); provided("browser-credential-file", value != "") {
		config.BrowserCredentialFile, err = filepath.Abs(value)
		if err != nil {
			return Config{}, path, err
		}
	}
	if value := strings.TrimSpace(options.BrowserLeaseRoot); provided("browser-lease-root", value != "") {
		config.BrowserLeaseRoot, err = filepath.Abs(value)
		if err != nil {
			return Config{}, path, err
		}
	}
	if value := strings.TrimSpace(options.BrowserBrokerRoot); provided("browser-broker-root", value != "") {
		config.BrowserBrokerRoot, err = filepath.Abs(value)
		if err != nil {
			return Config{}, path, err
		}
	}
	if provided("enabled", options.Enabled != nil) && options.Enabled != nil {
		config.Enabled = *options.Enabled
	}
	if err := validateNonSecretConfig(config); err != nil {
		return Config{}, path, err
	}
	if err := saveConfig(path, config); err != nil {
		return Config{}, path, err
	}
	return config, path, nil
}

func ModeEnabled(getenv func(string) string) bool {
	config, _, err := loadConfig(getenv)
	return err == nil && config.Enabled
}
