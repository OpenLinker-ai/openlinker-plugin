package agent

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const configVersion = 1

type Config struct {
	Version                  int      `json:"version"`
	Enabled                  bool     `json:"enabled"`
	Provider                 string   `json:"provider"`
	OpenLinkerURL            string   `json:"openlinker_url,omitempty"`
	AgentID                  string   `json:"agent_id"`
	Workspace                string   `json:"workspace"`
	StateDir                 string   `json:"state_dir,omitempty"`
	ProviderBin              string   `json:"provider_bin,omitempty"`
	Model                    string   `json:"model,omitempty"`
	Transport                string   `json:"transport,omitempty"`
	Capacity                 int64    `json:"capacity"`
	TimeoutSeconds           int      `json:"timeout_seconds"`
	SessionReuse             bool     `json:"session_reuse"`
	WebSearch                bool     `json:"web_search"`
	CodexBaseURL             string   `json:"codex_base_url,omitempty"`
	CodexSandbox             string   `json:"codex_sandbox,omitempty"`
	CodexApproval            string   `json:"codex_approval,omitempty"`
	ClaudePermission         string   `json:"claude_permission,omitempty"`
	AllowedTools             []string `json:"allowed_tools,omitempty"`
	ExecutionProfile         string   `json:"execution_profile,omitempty"`
	BrowserInteractionPolicy string   `json:"browser_interaction_policy,omitempty"`
	BrowserClientMode        string   `json:"browser_client_mode,omitempty"`
	BrowserPluginBin         string   `json:"browser_plugin_bin,omitempty"`
	BrowserNativePlugin      string   `json:"browser_native_plugin,omitempty"`
	BrowserSocket            string   `json:"browser_socket,omitempty"`
	BrowserCredentialFile    string   `json:"browser_credential_file,omitempty"`
	BrowserLeaseRoot         string   `json:"browser_lease_root,omitempty"`
	BrowserBrokerRoot        string   `json:"browser_broker_root,omitempty"`
	browserSelectedMode      string
	browserBackendMode       string
	browserFallbackReason    string
}

func defaultConfig() Config {
	return Config{
		Version: configVersion, Capacity: 1, TimeoutSeconds: 1800, SessionReuse: true,
		Transport: "auto", ExecutionProfile: "standard", BrowserInteractionPolicy: "restricted", CodexSandbox: "read-only", CodexApproval: "never", ClaudePermission: "dontAsk",
	}
}

func configPath(getenv func(string) string) (string, error) {
	if getenv != nil {
		if value := strings.TrimSpace(getenv("OPENLINKER_AGENT_CONFIG")); value != "" {
			return filepath.Abs(value)
		}
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "openlinker", "agent.json"), nil
}

func loadConfig(getenv func(string) string) (Config, string, error) {
	path, err := configPath(getenv)
	if err != nil {
		return Config{}, "", err
	}
	config := defaultConfig()
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return config, path, nil
	}
	if err != nil {
		return Config{}, path, err
	}
	if err := decodeStrictJSON(raw, &config); err != nil {
		return Config{}, path, fmt.Errorf("read Agent config: %w", err)
	}
	if config.Version != configVersion {
		return Config{}, path, fmt.Errorf("unsupported Agent config version %d", config.Version)
	}
	return config, path, nil
}

func saveConfig(path string, config Config) error {
	config.Version = configVersion
	return writePrivateJSON(path, config)
}

func stateDir(config Config, getenv func(string) string) (string, error) {
	for _, value := range []string{
		envValue(getenv, "OPENLINKER_AGENT_STATE_DIR"), config.StateDir,
	} {
		if strings.TrimSpace(value) != "" {
			return filepath.Abs(strings.TrimSpace(value))
		}
	}
	if xdg := envValue(getenv, "XDG_STATE_HOME"); strings.TrimSpace(xdg) != "" {
		return filepath.Abs(filepath.Join(xdg, "openlinker", "agent"))
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "openlinker", "agent"), nil
}

func loadOrCreateNodeID(dir, explicit string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "node-id")
	raw, err := os.ReadFile(path)
	if err == nil {
		stored := strings.TrimSpace(string(raw))
		if !validUUID(stored) {
			return "", errors.New("stored OpenLinker Node ID is invalid")
		}
		if explicit = strings.TrimSpace(explicit); explicit != "" && explicit != stored {
			return "", errors.New("OPENLINKER_NODE_ID does not match the persisted Node ID")
		}
		return stored, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	nodeID := strings.TrimSpace(explicit)
	if nodeID == "" {
		nodeID, err = newUUID()
		if err != nil {
			return "", err
		}
	}
	if !validUUID(nodeID) {
		return "", errors.New("OPENLINKER_NODE_ID must be a lowercase UUID")
	}
	if err := writePrivateFile(path, []byte(nodeID+"\n")); err != nil {
		return "", err
	}
	return nodeID, nil
}

func newUUID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16]), nil
}

func validUUID(value string) bool {
	if len(value) != 36 || value != strings.ToLower(value) {
		return false
	}
	for index, char := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			if char != '-' {
				return false
			}
			continue
		}
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')) {
			return false
		}
	}
	return value != "00000000-0000-0000-0000-000000000000"
}

func resolveSecret(getenv func(string) string, directName, fileName string, required bool) (string, string, error) {
	direct := strings.TrimSpace(envValue(getenv, directName))
	path := strings.TrimSpace(envValue(getenv, fileName))
	if direct != "" && path != "" {
		return "", "", fmt.Errorf("%s and %s are mutually exclusive", directName, fileName)
	}
	if direct != "" {
		return direct, "environment", nil
	}
	if path != "" {
		value, err := readPrivateSecret(path)
		if err != nil {
			return "", "", fmt.Errorf("%s: %w", fileName, err)
		}
		return value, "file", nil
	}
	if required {
		return "", "missing", fmt.Errorf("%s or %s is required", directName, fileName)
	}
	return "", "absent", nil
}

func readPrivateSecret(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", errors.New("secret path must be a regular file and not a symlink")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return "", errors.New("secret file must not be accessible by group or other users")
	}
	if !secretFileOwnedByCurrentUser(info) {
		return "", errors.New("secret file must be owned by the current user")
	}
	if info.Size() <= 0 || info.Size() > 64<<10 {
		return "", errors.New("secret file size is invalid")
	}
	file, err := os.Open(path) // #nosec G304 -- operator-selected secret path is validated above.
	if err != nil {
		return "", err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(raw))
	if value == "" {
		return "", errors.New("secret file is empty")
	}
	return value, nil
}

func envValue(getenv func(string) string, key string) string {
	if getenv == nil {
		return os.Getenv(key)
	}
	return getenv(key)
}

func applyRuntimeEnvironment(config *Config, getenv func(string) string) error {
	config.Transport = firstNonEmpty(envValue(getenv, "OPENLINKER_AGENT_TRANSPORT"), config.Transport)
	config.Model = firstNonEmpty(envValue(getenv, "OPENLINKER_"+strings.ToUpper(config.Provider)+"_MODEL"), config.Model)
	config.CodexBaseURL = firstNonEmpty(envValue(getenv, "OPENLINKER_CODEX_BASE_URL"), config.CodexBaseURL)
	config.CodexSandbox = firstNonEmpty(envValue(getenv, "OPENLINKER_CODEX_SANDBOX"), config.CodexSandbox)
	config.CodexApproval = firstNonEmpty(envValue(getenv, "OPENLINKER_CODEX_APPROVAL"), config.CodexApproval)
	config.ClaudePermission = firstNonEmpty(envValue(getenv, "OPENLINKER_CLAUDE_PERMISSION"), config.ClaudePermission)
	config.ExecutionProfile = firstNonEmpty(envValue(getenv, "OPENLINKER_AGENT_EXECUTION_PROFILE"), config.ExecutionProfile)
	config.BrowserInteractionPolicy = firstNonEmpty(
		envValue(getenv, "OPENLINKER_BROWSER_INTERACTION_POLICY"),
		config.BrowserInteractionPolicy,
	)
	config.BrowserClientMode = firstNonEmpty(envValue(getenv, "OPENLINKER_BROWSER_CLIENT_MODE"), config.BrowserClientMode)
	config.BrowserPluginBin = firstNonEmpty(envValue(getenv, "OPENLINKER_BROWSER_PLUGIN_BIN"), config.BrowserPluginBin)
	config.BrowserNativePlugin = firstNonEmpty(envValue(getenv, "OPENLINKER_BROWSER_NATIVE_PLUGIN_PATH"), config.BrowserNativePlugin)
	config.BrowserSocket = firstNonEmpty(envValue(getenv, "OPENLINKER_BROWSER_SOCKET"), config.BrowserSocket)
	config.BrowserCredentialFile = firstNonEmpty(envValue(getenv, "OPENLINKER_BROWSER_CHANNEL_CREDENTIAL_FILE"), config.BrowserCredentialFile)
	config.BrowserLeaseRoot = firstNonEmpty(envValue(getenv, "OPENLINKER_BROWSER_LEASE_ROOT"), config.BrowserLeaseRoot)
	config.BrowserBrokerRoot = firstNonEmpty(envValue(getenv, "OPENLINKER_BROWSER_BROKER_ROOT"), config.BrowserBrokerRoot)
	config.browserSelectedMode = strings.TrimSpace(envValue(getenv, "OPENLINKER_BROWSER_CLIENT_MODE_EFFECTIVE"))
	config.browserBackendMode = strings.TrimSpace(envValue(getenv, "OPENLINKER_BROWSER_BACKEND_MODE"))
	config.browserFallbackReason = strings.TrimSpace(envValue(getenv, "OPENLINKER_BROWSER_CLIENT_FALLBACK_REASON"))
	if value := strings.TrimSpace(envValue(getenv, "OPENLINKER_CLAUDE_ALLOWED_TOOLS")); value != "" {
		config.AllowedTools = splitNonEmpty(value)
	}
	if err := applyPositiveInteger(getenv, "OPENLINKER_AGENT_CAPACITY", &config.Capacity); err != nil {
		return err
	}
	if err := applyPositiveIntegerInt(getenv, "OPENLINKER_AGENT_TIMEOUT_SECONDS", &config.TimeoutSeconds); err != nil {
		return err
	}
	if err := applyBoolean(getenv, "OPENLINKER_AGENT_SESSION_REUSE", &config.SessionReuse); err != nil {
		return err
	}
	webSearchName := "OPENLINKER_" + strings.ToUpper(config.Provider) + "_WEB_SEARCH"
	if strings.TrimSpace(envValue(getenv, webSearchName)) == "" {
		webSearchName = "OPENLINKER_AGENT_WEB_SEARCH"
	}
	if err := applyBoolean(getenv, webSearchName, &config.WebSearch); err != nil {
		return err
	}
	return nil
}

func applyPositiveInteger(getenv func(string) string, name string, target *int64) error {
	value := strings.TrimSpace(envValue(getenv, name))
	if value == "" {
		return nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < 1 {
		return fmt.Errorf("%s must be a positive integer", name)
	}
	*target = parsed
	return nil
}

func applyPositiveIntegerInt(getenv func(string) string, name string, target *int) error {
	var parsed int64
	if strings.TrimSpace(envValue(getenv, name)) == "" {
		return nil
	}
	if err := applyPositiveInteger(getenv, name, &parsed); err != nil {
		return err
	}
	if int64(int(parsed)) != parsed {
		return fmt.Errorf("%s is too large", name)
	}
	*target = int(parsed)
	return nil
}

func applyBoolean(getenv func(string) string, name string, target *bool) error {
	value := strings.ToLower(strings.TrimSpace(envValue(getenv, name)))
	if value == "" {
		return nil
	}
	switch value {
	case "1", "true", "yes", "on", "enabled":
		*target = true
	case "0", "false", "no", "off", "disabled":
		*target = false
	default:
		return fmt.Errorf("%s must be true or false", name)
	}
	return nil
}

func splitNonEmpty(value string) []string {
	items := strings.Split(value, ",")
	result := make([]string, 0, len(items))
	for _, item := range items {
		if item = strings.TrimSpace(item); item != "" {
			result = append(result, item)
		}
	}
	return result
}
