package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

var fixedProvider string

const (
	runtimeUID  = 10001
	providerUID = 10002
	runtimeGID  = 10001
	providerGID = 10002
)

const entrypointStageEnv = "OPENLINKER_RUNTIME_ENTRYPOINT_AGENT_STAGE"
const maxEntrypointSecretBytes = 64 << 10

var canonicalUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func main() {
	if os.Getenv(entrypointStageEnv) != "1" {
		if err := runAgentStage("/usr/local/bin/openlinker-runtime-entrypoint"); err != nil {
			fatal(err.Error())
		}
		return
	}
	provider := strings.ToLower(strings.TrimSpace(fixedProvider))
	if provider != "codex" && provider != "claude" {
		fatal("image provider is not fixed to codex or claude")
	}
	if err := prepareBrowserMounts(); err != nil {
		fatal(err.Error())
	}
	if err := configure(provider, "/runtime", "/workspace", true); err != nil {
		fatal(err.Error())
	}
	if err := runCLI("/usr/local/bin/openlinker", provider); err != nil {
		fatal(err.Error())
	}
}

func configure(provider, runtimeDir, workspace string, requireMount bool) error {
	if requireMount && (os.Geteuid() != runtimeUID || os.Getegid() != runtimeGID) {
		return errors.New("official Provider images must run the Worker as the fixed Runtime UID/GID")
	}
	if err := validateRuntimeDir(runtimeDir, requireMount); err != nil {
		return err
	}
	if requireMount {
		if err := validateSetUIDCapability(); err != nil {
			return err
		}
	}
	if err := validateWorkspace(workspace, requireMount); err != nil {
		return err
	}
	providerHome := filepath.Join(runtimeDir, "providers", provider, "home")
	if requireMount {
		providerHome = "/provider"
		if err := validateProviderDir(providerHome); err != nil {
			return err
		}
	}
	if value := strings.TrimSpace(os.Getenv("OPENLINKER_BLOCK_PRIVATE_NETWORK")); (requireMount || value != "") && !truthy(value) {
		return errors.New("OPENLINKER_BLOCK_PRIVATE_NETWORK cannot be disabled in official images")
	}
	platformURL, err := validatePlatformURL(os.Getenv("OPENLINKER_URL"))
	if err != nil {
		return err
	}
	agentID := strings.ToLower(strings.TrimSpace(os.Getenv("OPENLINKER_AGENT_ID")))
	if !canonicalUUID.MatchString(agentID) {
		return errors.New("OPENLINKER_AGENT_ID must be a canonical non-zero UUID")
	}
	agentToken, err := resolveRequiredSecret("OPENLINKER_AGENT_TOKEN")
	if err != nil {
		return err
	}
	if !strings.HasPrefix(agentToken, "ol_agent_") {
		return errors.New("OPENLINKER_AGENT_TOKEN has an invalid prefix")
	}
	providerSecret, err := providerSecretName(provider)
	if err != nil {
		return err
	}
	providerKey, err := resolveRequiredSecret(providerSecret)
	if err != nil {
		return err
	}
	nodeID, err := resolveNodeID(runtimeDir, os.Getenv("OPENLINKER_NODE_ID"))
	if err != nil {
		return err
	}
	proxyURL, err := validateGatewayProxy(os.Getenv("OPENLINKER_EGRESS_PROXY_URL"))
	if err != nil {
		return err
	}

	statePaths := []string{filepath.Join(runtimeDir, "runtime"), filepath.Join(runtimeDir, "session-map")}
	if !requireMount {
		statePaths = append(statePaths, providerHome)
	}
	for _, path := range statePaths {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return fmt.Errorf("create runtime state directory: %w", err)
		}
		if err := ensureNoSymlinkComponents(runtimeDir, path); err != nil {
			return err
		}
		if err := os.Chmod(path, 0o700); err != nil {
			return fmt.Errorf("protect runtime state directory: %w", err)
		}
	}

	setenv := func(key, value string) error {
		if err := os.Setenv(key, value); err != nil {
			return fmt.Errorf("set %s: %w", key, err)
		}
		return nil
	}
	browserProfile := strings.EqualFold(
		strings.TrimSpace(os.Getenv("OPENLINKER_AGENT_EXECUTION_PROFILE")),
		"browser",
	)
	if browserProfile {
		if value, present := os.LookupEnv("OPENLINKER_USER_TOKEN"); present &&
			strings.TrimSpace(value) != "" {
			return errors.New(
				"OPENLINKER_USER_TOKEN is not supported by packaged Browser Agents",
			)
		}
	}

	values := map[string]string{
		"OPENLINKER_URL":                   platformURL,
		"OPENLINKER_AGENT_ID":              agentID,
		"OPENLINKER_AGENT_TOKEN":           agentToken,
		"OPENLINKER_NODE_ID":               nodeID,
		"OPENLINKER_PROVIDER":              provider,
		"OPENLINKER_AGENT_STATE_DIR":       runtimeDir,
		"OPENLINKER_AGENT_CONFIG":          filepath.Join(runtimeDir, "agent.json"),
		"OPENLINKER_WORKSPACE":             workspace,
		"OPENLINKER_AGENT_SESSION_REUSE":   "true",
		"OPENLINKER_BLOCK_PRIVATE_NETWORK": "true",
		"HOME":                             providerHome,
		"HTTP_PROXY":                       proxyURL,
		"HTTPS_PROXY":                      proxyURL,
		"http_proxy":                       proxyURL,
		"https_proxy":                      proxyURL,
		"NO_PROXY":                         "",
		"no_proxy":                         "",
		providerSecret:                     providerKey,
	}
	if browserProfile {
		values["OPENLINKER_AGENT_EXECUTION_PROFILE"] = "browser"
		values["OPENLINKER_BROWSER_PLUGIN_BIN"] = "/usr/local/bin/openlinker"
		values["OPENLINKER_BROWSER_SOCKET"] = filepath.Join(officialBrowserControlRoot, "openlinker.browser.sock")
		values["OPENLINKER_BROWSER_CHANNEL_CREDENTIAL_FILE"] = filepath.Join(officialBrowserControlRoot, "channel-credential")
		values["OPENLINKER_BROWSER_LEASE_ROOT"] = filepath.Join(officialBrowserControlRoot, "leases")
		values["OPENLINKER_BROWSER_BROKER_ROOT"] = officialBrowserBrokerRoot
	}
	if provider == "codex" {
		values["OPENLINKER_CODEX_BIN"] = "/usr/local/bin/openlinker-provider-launcher"
		values["CODEX_HOME"] = providerHome
		values["OPENLINKER_CODEX_BASE_URL"] = strings.TrimSpace(os.Getenv("OPENLINKER_CODEX_BASE_URL"))
		values["OPENLINKER_CODEX_WEB_SEARCH"] = defaultString(os.Getenv("OPENLINKER_CODEX_WEB_SEARCH"), "disabled")
		values["OPENLINKER_CODEX_APPROVAL"] = "never"
		values["OPENLINKER_CODEX_SANDBOX"] = defaultString(os.Getenv("OPENLINKER_CODEX_SANDBOX"), "read-only")
	} else {
		values["OPENLINKER_CLAUDE_BIN"] = "/usr/local/bin/openlinker-provider-launcher"
		values["CLAUDE_CONFIG_DIR"] = providerHome
		values["OPENLINKER_CLAUDE_WEB_SEARCH"] = defaultString(os.Getenv("OPENLINKER_CLAUDE_WEB_SEARCH"), "false")
		values["OPENLINKER_CLAUDE_PERMISSION"] = defaultString(os.Getenv("OPENLINKER_CLAUDE_PERMISSION"), "dontAsk")
	}
	for key, value := range values {
		if err := setenv(key, value); err != nil {
			return err
		}
	}
	if browserProfile {
		requested, present := os.LookupEnv("OPENLINKER_BROWSER_CLIENT_MODE")
		if !present {
			requested = defaultBrowserClientMode(provider, runtime.GOOS)
		} else if requested = strings.TrimSpace(requested); requested == "" {
			return errors.New(
				"OPENLINKER_BROWSER_CLIENT_MODE cannot be empty when explicitly set",
			)
		}
		pluginPath := officialClaudeBrowserPlugin
		if provider == "codex" {
			pluginPath = officialCodexBrowserPlugin
		}
		if !requireMount {
			pluginPath = strings.TrimSpace(
				os.Getenv("OPENLINKER_BROWSER_NATIVE_PLUGIN_PATH"),
			)
		}
		selection, err := selectBrowserClientMode(
			provider,
			requested,
			pluginPath,
			requireMount,
		)
		if err != nil {
			return err
		}
		for key, value := range map[string]string{
			"OPENLINKER_BROWSER_CLIENT_MODE":            selection.Requested,
			"OPENLINKER_BROWSER_CLIENT_MODE_EFFECTIVE":  selection.Selected,
			"OPENLINKER_BROWSER_BACKEND_MODE":           selection.BackendRequested,
			"OPENLINKER_BROWSER_NATIVE_PLUGIN_PATH":     selection.PluginPath,
			"OPENLINKER_BROWSER_CLIENT_FALLBACK_REASON": selection.FallbackReason,
		} {
			if err := setenv(key, value); err != nil {
				return err
			}
		}
	}
	for _, key := range []string{"OPENLINKER_AGENT_TOKEN_FILE", "CODEX_API_KEY_FILE", "ANTHROPIC_API_KEY_FILE", "ALL_PROXY", "all_proxy"} {
		_ = os.Unsetenv(key)
	}
	_ = os.Unsetenv("OPENLINKER_USER_TOKEN")
	return nil
}

func defaultBrowserClientMode(provider, platform string) string {
	if provider == "codex" && platform == "linux" {
		return "auto"
	}
	return "mcp"
}

func validateRuntimeDir(path string, requireMount bool) error {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("/runtime must be a real directory")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() {
		return errors.New("/runtime must be owned by the container user")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return errors.New("/runtime must be owner-only")
	}
	probe := filepath.Join(path, ".write-probe")
	file, err := os.OpenFile(probe, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return errors.New("/runtime must be writable")
	}
	_ = file.Close()
	_ = os.Remove(probe)
	if requireMount && !isMountPoint(path) {
		return errors.New("/runtime must be an independent persistent mount")
	}
	lock, err := os.OpenFile(filepath.Join(path, ".runtime-worker.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return errors.New("cannot open /runtime ownership lock")
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lock.Close()
		return errors.New("/runtime is already used by another Agent container")
	}
	// Keep the advisory lock alive for the lifetime of the entrypoint process.
	runtimeLock = lock
	return nil
}

var runtimeLock *os.File

func validateWorkspace(path string, requireMount bool) error {
	clean := filepath.Clean(path)
	if clean == string(filepath.Separator) || clean == "." {
		return errors.New("/workspace must be a restricted directory")
	}
	info, err := os.Lstat(clean)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("/workspace must be a real directory")
	}
	if info.Mode().Perm()&0o022 != 0 {
		return errors.New("/workspace must not be group/world writable")
	}
	if requireMount && !isMountPoint(clean) {
		return errors.New("/workspace must be an independent mount")
	}
	return nil
}

func validateProviderDir(path string) error {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("/provider must be a real directory")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != providerUID || int(stat.Gid) != providerGID {
		return errors.New("/provider must be owned by the fixed Provider UID/GID")
	}
	if info.Mode().Perm() != 0o700 {
		return errors.New("/provider must have mode 0700")
	}
	if !isMountPoint(path) {
		return errors.New("/provider must be an independent persistent mount")
	}
	return nil
}

func validateSetUIDCapability() error {
	raw, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return errors.New("cannot inspect Linux process capabilities")
	}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[0] != "CapEff:" {
			continue
		}
		mask, err := strconv.ParseUint(fields[1], 16, 64)
		const required = (uint64(1) << 6) | (uint64(1) << 7)
		if err != nil || mask != required {
			return errors.New("official Provider images require only CAP_SETUID and CAP_SETGID for process isolation")
		}
		return nil
	}
	return errors.New("cannot inspect Linux process capabilities")
}

func validatePlatformURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return "", errors.New("OPENLINKER_URL must be a public HTTPS origin")
	}
	parsed.Path = strings.TrimSuffix(parsed.Path, "/")
	return parsed.String(), nil
}

func validateGatewayProxy(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		raw = "http://openlinker-egress-gateway:3128"
	}
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "http" || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return "", errors.New("OPENLINKER_EGRESS_PROXY_URL must be an unauthenticated HTTP proxy URL")
	}
	return parsed.String(), nil
}

func resolveRequiredSecret(name string) (string, error) {
	direct, directSet := os.LookupEnv(name)
	file, fileSet := os.LookupEnv(name + "_FILE")
	direct = strings.TrimSpace(direct)
	file = strings.TrimSpace(file)
	if directSet && direct != "" && fileSet && file != "" {
		return "", fmt.Errorf("%s and %s_FILE are mutually exclusive", name, name)
	}
	if file != "" {
		info, err := os.Lstat(file)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() <= 0 || info.Size() > maxEntrypointSecretBytes {
			return "", fmt.Errorf("%s_FILE must be an owner-only regular non-symlink file", name)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || int(stat.Uid) != os.Geteuid() {
			return "", fmt.Errorf("%s_FILE must be owned by the current process user", name)
		}
		raw, err := os.ReadFile(file)
		if err != nil {
			return "", fmt.Errorf("read %s_FILE", name)
		}
		value := strings.TrimSuffix(string(raw), "\n")
		value = strings.TrimSuffix(value, "\r")
		if strings.TrimSpace(value) == "" {
			return "", fmt.Errorf("%s_FILE is empty", name)
		}
		return value, nil
	}
	if direct == "" {
		return "", fmt.Errorf("%s or %s_FILE is required", name, name)
	}
	return direct, nil
}

func providerSecretName(provider string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "codex":
		return "CODEX_API_KEY", nil
	case "claude":
		return "ANTHROPIC_API_KEY", nil
	default:
		return "", errors.New("image provider is not fixed to codex or claude")
	}
}

func agentStageEnvironment(provider string) ([]string, error) {
	providerSecret, err := providerSecretName(provider)
	if err != nil {
		return nil, err
	}
	resolved := make(map[string]string, 2)
	for _, name := range []string{"OPENLINKER_AGENT_TOKEN", providerSecret} {
		value, err := resolveRequiredSecret(name)
		if err != nil {
			return nil, err
		}
		resolved[name] = value
	}

	blocked := map[string]struct{}{
		entrypointStageEnv:            {},
		"OPENLINKER_AGENT_TOKEN":      {},
		"OPENLINKER_AGENT_TOKEN_FILE": {},
		"CODEX_API_KEY":               {},
		"CODEX_API_KEY_FILE":          {},
		"ANTHROPIC_API_KEY":           {},
		"ANTHROPIC_API_KEY_FILE":      {},
	}
	environment := make([]string, 0, len(os.Environ())+len(resolved)+1)
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		if _, skip := blocked[key]; !skip {
			environment = append(environment, item)
		}
	}
	for name, value := range resolved {
		environment = append(environment, name+"="+value)
	}
	return append(environment, entrypointStageEnv+"=1"), nil
}

func resolveNodeID(runtimeDir, configured string) (string, error) {
	path := filepath.Join(runtimeDir, "node-id")
	configured = strings.ToLower(strings.TrimSpace(configured))
	if configured != "" && !canonicalUUID.MatchString(configured) {
		return "", errors.New("OPENLINKER_NODE_ID must be a canonical non-zero UUID")
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
			return "", errors.New("/runtime/node-id must be an owner-only regular non-symlink file")
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return "", errors.New("read /runtime/node-id")
		}
		persisted := strings.ToLower(strings.TrimSpace(string(raw)))
		if !canonicalUUID.MatchString(persisted) {
			return "", errors.New("/runtime/node-id is invalid")
		}
		if configured != "" && configured != persisted {
			return "", errors.New("OPENLINKER_NODE_ID does not match /runtime/node-id")
		}
		return persisted, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", errors.New("inspect /runtime/node-id")
	}
	if configured == "" {
		var err error
		configured, err = newUUIDv4()
		if err != nil {
			return "", err
		}
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", errors.New("atomically create /runtime/node-id")
	}
	if _, err := file.WriteString(configured + "\n"); err != nil {
		_ = file.Close()
		return "", errors.New("write /runtime/node-id")
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return "", errors.New("sync /runtime/node-id")
	}
	if err := file.Close(); err != nil {
		return "", errors.New("close /runtime/node-id")
	}
	return configured, nil
}

func newUUIDv4() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", errors.New("generate Runtime Node ID")
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	hexValue := hex.EncodeToString(value)
	return hexValue[0:8] + "-" + hexValue[8:12] + "-" + hexValue[12:16] + "-" + hexValue[16:20] + "-" + hexValue[20:32], nil
}

func ensureNoSymlinkComponents(root, target string) error {
	relative, err := filepath.Rel(root, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("runtime state path escaped /runtime")
	}
	current := root
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return errors.New("inspect runtime state path")
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("runtime state path contains a symlink")
		}
	}
	return nil
}

func isMountPoint(path string) bool {
	raw, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return false
	}
	clean := filepath.Clean(path)
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 4 && strings.ReplaceAll(fields[4], `\040`, " ") == clean {
			return true
		}
	}
	return false
}

func runCLI(binary, provider string) error {
	cmd := exec.Command(binary, "agent", "serve", "--provider", provider) // #nosec G204 -- fixed image binary and provider.
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return runPreparedCommand(cmd)
}

func runPreparedCommand(cmd *exec.Cmd) error {
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start isolated process: %w", err)
	}
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	for {
		select {
		case current := <-signals:
			_ = cmd.Process.Signal(current)
		case err := <-done:
			signal.Stop(signals)
			if err != nil {
				return fmt.Errorf("isolated process exited: %w", err)
			}
			return nil
		}
	}
}

func truthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}

func fatal(message string) {
	fmt.Fprintln(os.Stderr, "openlinker runtime entrypoint:", message)
	os.Exit(1)
}
