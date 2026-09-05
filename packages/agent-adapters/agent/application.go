package agent

import (
	"context"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
)

type Diagnostic struct {
	OK      bool              `json:"ok"`
	Checks  map[string]string `json:"checks"`
	Config  string            `json:"config_path"`
	Message string            `json:"message,omitempty"`
}

func Diagnose(getenv func(string) string, providerOverride string) Diagnostic {
	config, path, err := loadConfig(getenv)
	result := Diagnostic{OK: true, Checks: map[string]string{}, Config: path}
	if err != nil {
		result.OK, result.Message = false, boundedStatusMessage(err)
		return result
	}
	config.Provider = firstNonEmpty(providerOverride, envValue(getenv, "OPENLINKER_PROVIDER"), config.Provider)
	config.AgentID = firstNonEmpty(envValue(getenv, "OPENLINKER_AGENT_ID"), config.AgentID)
	config.Workspace = firstNonEmpty(envValue(getenv, "OPENLINKER_WORKSPACE"), config.Workspace)
	config.OpenLinkerURL = firstNonEmpty(envValue(getenv, "OPENLINKER_URL"), envValue(getenv, "OPENLINKER_API_BASE"), config.OpenLinkerURL)
	runtimeOptionsErr := applyRuntimeEnvironment(&config, getenv)
	if runtimeOptionsErr == nil {
		runtimeOptionsErr = validateProviderPolicy(config)
	}
	check := func(name string, ok bool, success, failure string) {
		if ok {
			result.Checks[name] = success
		} else {
			result.Checks[name], result.OK = failure, false
		}
	}
	check("provider", config.Provider == "codex" || config.Provider == "claude", config.Provider, "missing_or_invalid")
	check("agent_id", validUUID(config.AgentID), "valid", "missing_or_invalid")
	check("openlinker_url", config.OpenLinkerURL != "", "present", "missing")
	check("runtime_options", runtimeOptionsErr == nil, "valid", "invalid")
	check("execution_profile", config.ExecutionProfile == "standard" || config.ExecutionProfile == "browser", config.ExecutionProfile, "invalid")
	check(
		"browser_interaction_policy",
		config.BrowserInteractionPolicy == "restricted" ||
			(config.ExecutionProfile == "browser" && config.BrowserInteractionPolicy == "full"),
		config.BrowserInteractionPolicy,
		"invalid",
	)
	if config.ExecutionProfile == "browser" {
		check(
			"browser_client_mode",
			validBrowserClientMode(config.BrowserClientMode),
			firstNonEmpty(config.browserSelectedMode, config.BrowserClientMode, "mcp"),
			"invalid",
		)
	}
	workspaceInfo, workspaceErr := os.Stat(config.Workspace)
	check("workspace", workspaceErr == nil && workspaceInfo.IsDir(), "directory", "missing_or_invalid")
	_, tokenSource, tokenErr := resolveSecret(getenv, "OPENLINKER_AGENT_TOKEN", "OPENLINKER_AGENT_TOKEN_FILE", true)
	check("agent_token", tokenErr == nil, tokenSource, "missing_or_invalid")
	if config.Provider == "codex" || config.Provider == "claude" {
		binEnv, fallback := "OPENLINKER_CODEX_BIN", "codex"
		key, keyFile := "CODEX_API_KEY", "CODEX_API_KEY_FILE"
		if config.Provider == "claude" {
			binEnv, fallback, key, keyFile = "OPENLINKER_CLAUDE_BIN", "claude", "ANTHROPIC_API_KEY", "ANTHROPIC_API_KEY_FILE"
		}
		bin := firstNonEmpty(envValue(getenv, binEnv), config.ProviderBin, fallback)
		_, binErr := exec.LookPath(bin)
		check("provider_cli", binErr == nil, "present", "missing")
		_, authSource, authErr := resolveSecret(getenv, key, keyFile, false)
		check("provider_auth", authErr == nil, authSource, "invalid")
	}
	state, stateErr := stateDir(config, getenv)
	check("state_dir", stateErr == nil && state != "", "available", "invalid")
	if config.ExecutionProfile == "browser" {
		_, credentialErr := readPrivateSecret(config.BrowserCredentialFile)
		check("browser_credential", credentialErr == nil, "owner_only_file", "missing_or_invalid")
		_, pluginErr := exec.LookPath(firstNonEmpty(config.BrowserPluginBin, currentExecutable()))
		check("browser_plugin", pluginErr == nil, "present", "missing")
		if firstNonEmpty(config.browserSelectedMode, config.BrowserClientMode, "mcp") == "native" {
			nativeInfo, nativeErr := os.Stat(config.BrowserNativePlugin)
			check(
				"browser_native_plugin",
				nativeErr == nil && nativeInfo.IsDir() && filepath.IsAbs(config.BrowserNativePlugin),
				"present",
				"missing_or_invalid",
			)
		}
	}
	result.Checks["runtime_security"] = "token_only"
	return result
}

func validateNonSecretConfig(config Config) error {
	if config.Provider != "codex" && config.Provider != "claude" {
		return errors.New("--provider must be codex or claude")
	}
	if !validUUID(config.AgentID) {
		return errors.New("--agent-id must be a lowercase UUID")
	}
	info, err := os.Stat(config.Workspace)
	if err != nil || !info.IsDir() {
		return errors.New("--workspace must be an existing directory")
	}
	if config.Capacity < 1 || config.Capacity > 1024 {
		return errors.New("--capacity must be between 1 and 1024")
	}
	if config.TimeoutSeconds < 1 {
		return errors.New("--timeout must be positive")
	}
	switch config.Transport {
	case "auto", "websocket", "ws", "pull", "http":
	default:
		return errors.New("--transport must be auto, websocket/ws, or pull/http")
	}
	if err := validateExecutionProfile(config); err != nil {
		return err
	}
	return validateProviderPolicy(config)
}

func validateExecutionProfile(config Config) error {
	switch config.ExecutionProfile {
	case "", "standard":
		if config.BrowserInteractionPolicy != "" && config.BrowserInteractionPolicy != "restricted" {
			return errors.New("standard execution profile requires restricted Browser interaction policy")
		}
		return nil
	case "browser":
	default:
		return errors.New("--execution-profile must be standard or browser")
	}
	if config.Capacity != 1 {
		return errors.New("Browser execution profile requires --capacity 1")
	}
	if config.BrowserInteractionPolicy != "restricted" &&
		config.BrowserInteractionPolicy != "full" {
		return errors.New("Browser interaction policy must be restricted or full")
	}
	if !config.SessionReuse {
		return errors.New("Browser execution profile requires --session-reuse")
	}
	if !validBrowserClientMode(config.BrowserClientMode) {
		return errors.New("--browser-client-mode must be auto, openlinker-native-chrome, isolated-native, isolated-mcp, native, or mcp")
	}
	selected := config.browserSelectedMode
	if selected == "" && config.BrowserClientMode != "auto" {
		switch config.BrowserClientMode {
		case "native", "isolated-native", "openlinker-native-chrome", "official-chrome":
			selected = "native"
		case "mcp", "isolated-mcp":
			selected = "mcp"
		}
	}
	selected = firstNonEmpty(selected, "mcp")
	if selected != "native" && selected != "mcp" {
		return errors.New("effective Browser client mode must be native or mcp")
	}
	if config.browserFallbackReason != "" && config.BrowserClientMode != "auto" {
		return errors.New("Browser fallback evidence requires auto mode")
	}
	if selected == "native" &&
		(strings.TrimSpace(config.BrowserNativePlugin) == "" ||
			!filepath.IsAbs(config.BrowserNativePlugin)) {
		return errors.New("--browser-native-plugin must be an absolute path in native mode")
	}
	for label, value := range map[string]string{
		"--browser-socket":          config.BrowserSocket,
		"--browser-credential-file": config.BrowserCredentialFile,
		"--browser-lease-root":      config.BrowserLeaseRoot,
		"--browser-broker-root":     config.BrowserBrokerRoot,
	} {
		if strings.TrimSpace(value) == "" || !filepath.IsAbs(value) {
			return errors.New(label + " must be an absolute path for the Browser execution profile")
		}
	}
	return nil
}

func validBrowserClientMode(value string) bool {
	switch firstNonEmpty(strings.TrimSpace(value), "mcp") {
	case "auto", "openlinker-native-chrome", "official-chrome", "isolated-native", "isolated-mcp", "native", "mcp":
		return true
	default:
		return false
	}
}

func currentExecutable() string {
	value, err := os.Executable()
	if err != nil {
		return ""
	}
	return value
}

func validateCodexBaseURL(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	parsed, err := url.Parse(value)
	if err != nil || !parsed.IsAbs() || parsed.Host == "" || parsed.Opaque != "" {
		return errors.New("Codex Base URL must be an absolute HTTP(S) URL with a host")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("Codex Base URL must use http or https")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return errors.New("Codex Base URL must not contain credentials, a query, or a fragment")
	}
	return nil
}

func validateProviderPolicy(config Config) error {
	if err := validateCodexBaseURL(config.CodexBaseURL); err != nil {
		return err
	}
	switch config.CodexSandbox {
	case "", "read-only", "workspace-write", "danger-full-access":
	default:
		return errors.New("Codex sandbox must be read-only, workspace-write, or danger-full-access")
	}
	switch config.CodexApproval {
	case "", "never", "untrusted", "on-request":
	default:
		return errors.New("Codex approval must be never, untrusted, or on-request")
	}
	switch config.ClaudePermission {
	case "", "acceptEdits", "auto", "dontAsk", "manual", "plan":
	default:
		return errors.New("Claude permission must be acceptEdits, auto, dontAsk, manual, or plan")
	}
	for _, tool := range config.AllowedTools {
		if strings.TrimSpace(tool) == "" || strings.ContainsAny(tool, "\r\n\x00") {
			return errors.New("Claude allowed tools must be non-empty single-line values")
		}
	}
	return nil
}

func SetEnabled(getenv func(string) string, enabled bool) (Config, string, error) {
	config, path, err := loadConfig(getenv)
	if err != nil {
		return Config{}, path, err
	}
	config.Enabled = enabled
	if err := saveConfig(path, config); err != nil {
		return Config{}, path, err
	}
	return config, path, nil
}

func storedStatus(config Config, getenv func(string) string) (Status, bool) {
	dir, err := stateDir(config, getenv)
	if err != nil {
		return Status{}, false
	}
	raw, err := os.ReadFile(filepath.Join(dir, "status.json"))
	if err != nil {
		return Status{}, false
	}
	var status Status
	if decodeStrictJSON(raw, &status) != nil {
		return Status{}, false
	}
	return status, true
}

func ContextWithSignals(parent context.Context) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
}

// ReadStatus resolves the persisted enabled setting and uses durable status when
// no Worker is running in this process.
func ReadStatus(getenv func(string) string, service *Service) (Status, error) {
	status := service.Status()
	config, _, err := loadConfig(getenv)
	if err != nil {
		return Status{}, err
	}
	status.Enabled = config.Enabled
	if status.State == "stopped" {
		if stored, ok := storedStatus(config, getenv); ok {
			status = stored
			status.Enabled = config.Enabled
		}
	}
	return status, nil
}
