package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	openlinker "github.com/OpenLinker-ai/openlinker-go"
	"github.com/OpenLinker-ai/openlinker-plugin/packages/agent-adapters/agentexec"
	"github.com/OpenLinker-ai/openlinker-plugin/packages/agent-adapters/agenthost"
	"github.com/OpenLinker-ai/openlinker-plugin/packages/agent-adapters/browserclientmode"
	"github.com/OpenLinker-ai/openlinker-plugin/packages/agent-adapters/browserextension"
)

const (
	shutdownTimeout                = 15 * time.Second
	browserExecutionProfileFeature = "browser_execution_profile.v1"
	browserFullInteractionFeature  = "browser_full_interaction.v1"
	browserHumanControlFeature     = "browser_human_control.v1"
	browserHumanControlEnvironment = "OPENLINKER_BROWSER_HUMAN_CONTROL_ENABLED"
	browserObservationEnvironment  = "OPENLINKER_BROWSER_AUTHENTICATED_OBSERVATION_ENABLED"
)

func runtimeOptionalFeatures(
	executionProfile string,
	interactionPolicy string,
	humanControlEnabled bool,
	observationEnabled bool,
) []string {
	if strings.TrimSpace(executionProfile) != "browser" {
		return nil
	}
	features := []string{browserExecutionProfileFeature}
	if strings.TrimSpace(interactionPolicy) == "full" {
		features = append(features, browserFullInteractionFeature)
	}
	if humanControlEnabled {
		features = append(features, browserHumanControlFeature)
	}
	if observationEnabled {
		features = append(features, browserextension.ObserverBridgeFeature)
	}
	return features
}

func runtimeExtensionRoutes(
	executionProfile string,
	humanControlEnabled bool,
	observationEnabled bool,
) []openlinker.RuntimeExtensionRoute {
	if strings.TrimSpace(executionProfile) != "browser" {
		return nil
	}
	// The feature declaration and the route registration are driven by the same
	// flags on purpose: a Worker that announces a capability it cannot route
	// would make Core send commands into a channel nobody reads.
	var routes []openlinker.RuntimeExtensionRoute
	if humanControlEnabled {
		routes = append(routes, browserextension.RuntimeViewerExtensionRoute)
	}
	if observationEnabled {
		routes = append(routes, browserextension.ObserverBridgeExtensionRoute)
	}
	return routes
}

// runtimeObservationEnabled mirrors runtimeHumanControlEnabled so the Worker and
// the Browser Runtime read the same flag name.
func runtimeObservationEnabled(
	getenv func(string) string,
	executionProfile string,
) (bool, error) {
	if strings.TrimSpace(executionProfile) != "browser" {
		return false, nil
	}
	raw := strings.TrimSpace(getenv(browserObservationEnvironment))
	if raw == "" {
		return false, nil
	}
	enabled, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf(
			"%s must be true or false",
			browserObservationEnvironment,
		)
	}
	return enabled, nil
}

func runtimeHumanControlEnabled(
	getenv func(string) string,
	executionProfile string,
) (bool, error) {
	if strings.TrimSpace(executionProfile) != "browser" {
		return false, nil
	}
	raw := strings.TrimSpace(getenv(browserHumanControlEnvironment))
	if raw == "" {
		return false, nil
	}
	enabled, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf(
			"%s must be true or false",
			browserHumanControlEnvironment,
		)
	}
	return enabled, nil
}

type Status struct {
	State                    string `json:"state"`
	Enabled                  bool   `json:"enabled"`
	Provider                 string `json:"provider,omitempty"`
	AgentID                  string `json:"agent_id,omitempty"`
	NodeID                   string `json:"node_id,omitempty"`
	Workspace                string `json:"workspace,omitempty"`
	Transport                string `json:"transport,omitempty"`
	AgentTokenSource         string `json:"agent_token_source,omitempty"`
	ProviderAuthSource       string `json:"provider_auth_source,omitempty"`
	ConfigPath               string `json:"config_path,omitempty"`
	StateDir                 string `json:"state_dir,omitempty"`
	ExecutionProfile         string `json:"execution_profile,omitempty"`
	BrowserInteractionPolicy string `json:"browser_interaction_policy,omitempty"`
	BrowserClientMode        string `json:"browser_client_mode,omitempty"`
	Message                  string `json:"message,omitempty"`
	UpdatedAt                string `json:"updated_at"`
}

type resolvedRuntime struct {
	config             Config
	configPath         string
	stateDir           string
	url                string
	runtimeURL         string
	nodeID             string
	agentToken         string
	agentTokenSource   string
	providerAuthSource string
	handler            agentexec.Handler
	workerLock         *agentModeLock
}

type Service struct {
	getenv  func(string) string
	logger  *log.Logger
	version string

	lifecycleMu sync.Mutex
	mu          sync.Mutex
	status      Status
	worker      *openlinker.RuntimeWorker
	workerLock  *agentModeLock
	cancel      context.CancelFunc
	done        chan error
	ready       chan struct{}
}

func NewService(getenv func(string) string, logger *log.Logger, version string) *Service {
	if getenv == nil {
		getenv = os.Getenv
	}
	return &Service{getenv: getenv, logger: logger, version: firstNonEmpty(version, "dev"), status: newStatus("stopped")}
}

func (service *Service) Run(ctx context.Context, providerOverride string) error {
	if err := service.Enable(ctx, providerOverride); err != nil {
		return err
	}
	service.mu.Lock()
	done := service.done
	service.mu.Unlock()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := service.Disable(shutdownCtx); err != nil {
			return err
		}
		return nil
	}
}

func (service *Service) Enable(parent context.Context, providerOverride string) error {
	service.lifecycleMu.Lock()
	lifecycleLocked := true
	defer func() {
		if lifecycleLocked {
			service.lifecycleMu.Unlock()
		}
	}()
	service.mu.Lock()
	if service.worker != nil {
		service.mu.Unlock()
		return errors.New("Agent mode is already running")
	}
	service.mu.Unlock()

	resolved, err := resolveRuntime(service.getenv, providerOverride, service.version)
	if err != nil {
		service.setStatus(Status{State: "error", Message: boundedStatusMessage(err), UpdatedAt: nowText()})
		return err
	}
	humanControlEnabled, err := runtimeHumanControlEnabled(
		service.getenv,
		resolved.config.ExecutionProfile,
	)
	if err != nil {
		service.setStatus(Status{State: "error", Message: boundedStatusMessage(err), UpdatedAt: nowText()})
		return err
	}
	observationEnabled, err := runtimeObservationEnabled(
		service.getenv,
		resolved.config.ExecutionProfile,
	)
	if err != nil {
		service.setStatus(Status{State: "error", Message: boundedStatusMessage(err), UpdatedAt: nowText()})
		return err
	}
	workerLock := resolved.workerLock
	ready := make(chan struct{})
	readyOnce := sync.Once{}
	optionalFeatures := runtimeOptionalFeatures(resolved.config.ExecutionProfile, resolved.config.BrowserInteractionPolicy, humanControlEnabled, observationEnabled)
	if len(resolved.config.DelegationTargets) > 0 {
		optionalFeatures = append(optionalFeatures, openlinker.RuntimeDelegatedRunReadFeature)
	}
	worker, err := openlinker.NewRuntimeWorker(openlinker.RuntimeWorkerConfig{
		PlatformURL: resolved.url, RuntimeURL: resolved.runtimeURL,
		Transport: openlinker.RuntimeTransportMode(resolved.config.Transport),
		NodeID:    resolved.nodeID, NodeVersion: "openlinker-cli/" + service.version,
		AgentID: resolved.config.AgentID, AgentToken: resolved.agentToken,
		RequireTokenOnly: true,
		DataDir:          filepath.Join(resolved.stateDir, "runtime"), Capacity: resolved.config.Capacity,
		Handler: resolved.handler, Logger: service.logger,
		OptionalFeatures: optionalFeatures,
		ExtensionRoutes: runtimeExtensionRoutes(
			resolved.config.ExecutionProfile,
			humanControlEnabled,
			observationEnabled,
		),
		OnReady: func(_ openlinker.RuntimeReadyPayload) {
			service.setStatus(Status{
				State: "ready", Enabled: resolved.config.Enabled, Provider: resolved.config.Provider, AgentID: resolved.config.AgentID,
				NodeID: resolved.nodeID, Workspace: resolved.config.Workspace,
				Transport: resolved.config.Transport, AgentTokenSource: resolved.agentTokenSource,
				ProviderAuthSource: resolved.providerAuthSource, ConfigPath: resolved.configPath,
				StateDir: resolved.stateDir, UpdatedAt: nowText(),
				ExecutionProfile:         resolved.config.ExecutionProfile,
				BrowserInteractionPolicy: resolved.config.BrowserInteractionPolicy,
				BrowserClientMode: firstNonEmpty(
					resolved.config.browserSelectedMode,
					resolved.config.BrowserClientMode,
				),
			})
			readyOnce.Do(func() { close(ready) })
		},
	})
	if err != nil {
		_ = workerLock.release()
		return err
	}
	lifetime, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	service.mu.Lock()
	service.worker, service.workerLock, service.cancel, service.done, service.ready = worker, workerLock, cancel, done, ready
	service.status = Status{
		State: "starting", Enabled: resolved.config.Enabled, Provider: resolved.config.Provider, AgentID: resolved.config.AgentID,
		NodeID: resolved.nodeID, Workspace: resolved.config.Workspace, Transport: resolved.config.Transport,
		AgentTokenSource: resolved.agentTokenSource, ProviderAuthSource: resolved.providerAuthSource,
		ConfigPath: resolved.configPath, StateDir: resolved.stateDir, UpdatedAt: nowText(),
		ExecutionProfile:         resolved.config.ExecutionProfile,
		BrowserInteractionPolicy: resolved.config.BrowserInteractionPolicy,
		BrowserClientMode: firstNonEmpty(
			resolved.config.browserSelectedMode,
			resolved.config.BrowserClientMode,
		),
	}
	service.persistStatusLocked()
	service.mu.Unlock()

	go func() {
		err := worker.Start(lifetime)
		lockErr := workerLock.release()
		service.mu.Lock()
		if service.worker == worker {
			service.worker, service.workerLock, service.cancel = nil, nil, nil
			if err != nil {
				service.status.State, service.status.Message = "error", boundedStatusMessage(err)
			} else if lockErr != nil {
				service.status.State, service.status.Message = "error", boundedStatusMessage(lockErr)
			} else {
				service.status.State = "stopped"
			}
			service.status.UpdatedAt = nowText()
			service.persistStatusLocked()
		}
		service.mu.Unlock()
		if err == nil {
			err = lockErr
		}
		done <- err
		close(done)
	}()
	service.lifecycleMu.Unlock()
	lifecycleLocked = false

	select {
	case <-ready:
		return nil
	case err := <-done:
		if err == nil {
			return errors.New("Runtime Worker stopped before becoming ready")
		}
		return err
	case <-parent.Done():
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer shutdownCancel()
		_ = service.Disable(shutdownCtx)
		return parent.Err()
	}
}

func (service *Service) Disable(ctx context.Context) error {
	service.lifecycleMu.Lock()
	defer service.lifecycleMu.Unlock()
	service.mu.Lock()
	worker, cancel, done := service.worker, service.cancel, service.done
	if worker == nil {
		service.mu.Unlock()
		return nil
	}
	wasReady := service.status.State == "ready"
	service.status.State = "draining"
	service.status.UpdatedAt = nowText()
	service.persistStatusLocked()
	if !wasReady && cancel != nil {
		// A Worker that has not reported ready has not opened admission yet. Stop
		// startup directly; the SDK only permits server-authoritative Drain after
		// its lifecycle is running.
		cancel()
	}
	service.mu.Unlock()

	var drainErr error
	if wasReady {
		drainErr = worker.Drain(ctx, openlinker.RuntimeWorkerDrainOptions{Timeout: shutdownTimeout, ReasonCode: "operator_request"})
		if cancel != nil {
			cancel()
		}
	}
	select {
	case runErr := <-done:
		if drainErr != nil {
			return drainErr
		}
		return runErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (service *Service) Status() Status {
	service.mu.Lock()
	defer service.mu.Unlock()
	return service.status
}

func resolveRuntime(getenv func(string) string, providerOverride, version string) (resolvedRuntime, error) {
	config, path, err := loadConfig(getenv)
	if err != nil {
		return resolvedRuntime{}, err
	}
	config.Provider = firstNonEmpty(providerOverride, envValue(getenv, "OPENLINKER_PROVIDER"), config.Provider)
	config.OpenLinkerURL = firstNonEmpty(envValue(getenv, "OPENLINKER_URL"), envValue(getenv, "OPENLINKER_API_BASE"), config.OpenLinkerURL)
	config.AgentID = firstNonEmpty(envValue(getenv, "OPENLINKER_AGENT_ID"), config.AgentID)
	config.Workspace = firstNonEmpty(envValue(getenv, "OPENLINKER_WORKSPACE"), config.Workspace)
	if err := applyRuntimeEnvironment(&config, getenv); err != nil {
		return resolvedRuntime{}, err
	}
	if config.Capacity == 0 {
		config.Capacity = 1
	}
	if config.TimeoutSeconds <= 0 {
		config.TimeoutSeconds = 1800
	}
	if config.Transport == "" {
		config.Transport = "auto"
	}
	if config.ExecutionProfile == "" {
		config.ExecutionProfile = "standard"
	}
	if config.ExecutionProfile == "browser" &&
		strings.TrimSpace(config.BrowserClientMode) == "" {
		config.BrowserClientMode = defaultBrowserClientMode(
			config.Provider,
			runtime.GOOS,
		)
	}
	if config.Capacity > 1024 {
		return resolvedRuntime{}, errors.New("OPENLINKER_AGENT_CAPACITY must not exceed 1024")
	}
	if err := validateExecutionProfile(config); err != nil {
		return resolvedRuntime{}, err
	}
	if err := validateProviderPolicy(config); err != nil {
		return resolvedRuntime{}, err
	}
	if config.Provider != "codex" && config.Provider != "claude" {
		return resolvedRuntime{}, errors.New("Agent provider must be codex or claude")
	}
	if config.OpenLinkerURL == "" {
		return resolvedRuntime{}, errors.New("OPENLINKER_URL is required")
	}
	if !validUUID(config.AgentID) {
		return resolvedRuntime{}, errors.New("OPENLINKER_AGENT_ID must be a lowercase UUID")
	}
	workspace, err := filepath.Abs(config.Workspace)
	if err != nil || strings.TrimSpace(config.Workspace) == "" {
		return resolvedRuntime{}, errors.New("Agent workspace is required")
	}
	info, err := os.Stat(workspace)
	if err != nil || !info.IsDir() {
		return resolvedRuntime{}, errors.New("Agent workspace must be an existing directory")
	}
	config.Workspace = workspace
	dir, err := stateDir(config, getenv)
	if err != nil {
		return resolvedRuntime{}, err
	}
	workerLock, err := acquireAgentModeLock(dir)
	if err != nil {
		return resolvedRuntime{}, err
	}
	keepLock := false
	defer func() {
		if !keepLock {
			_ = workerLock.release()
		}
	}()
	nodeID, err := loadOrCreateNodeID(dir, envValue(getenv, "OPENLINKER_NODE_ID"))
	if err != nil {
		return resolvedRuntime{}, err
	}
	agentToken, agentTokenSource, err := resolveSecret(getenv, "OPENLINKER_AGENT_TOKEN", "OPENLINKER_AGENT_TOKEN_FILE", true)
	if err != nil {
		return resolvedRuntime{}, err
	}
	providerKeyName, providerKeyFileName := "CODEX_API_KEY", "CODEX_API_KEY_FILE"
	providerBinEnv := "OPENLINKER_CODEX_BIN"
	if config.Provider == "claude" {
		providerKeyName, providerKeyFileName, providerBinEnv = "ANTHROPIC_API_KEY", "ANTHROPIC_API_KEY_FILE", "OPENLINKER_CLAUDE_BIN"
	}
	providerKey, providerSource, err := resolveSecret(getenv, providerKeyName, providerKeyFileName, providerAPIKeyRequired(config))
	if err != nil {
		return resolvedRuntime{}, err
	}
	providerBin := firstNonEmpty(envValue(getenv, providerBinEnv), config.ProviderBin, config.Provider)
	if _, err := agentexec.CheckProviderCLI(context.Background(), agentexec.ProviderConfig{Provider: config.Provider, Bin: providerBin}); err != nil {
		return resolvedRuntime{}, err
	}
	if config.ExecutionProfile == "browser" &&
		config.browserSelectedMode == "" &&
		browserModeRequiresNativePreflight(config.BrowserClientMode) {
		selection, selectErr := browserclientmode.Select(
			browserclientmode.Options{
				Provider:   config.Provider,
				Requested:  config.BrowserClientMode,
				PluginPath: config.BrowserNativePlugin,
				RunHostCommand: browserClientModeHostRunner(
					providerBin,
					removeEnvironmentKeys(
						os.Environ(),
						"OPENLINKER_AGENT_TOKEN",
						"OPENLINKER_AGENT_TOKEN_FILE",
						"OPENLINKER_USER_TOKEN",
						"CODEX_API_KEY",
						"CODEX_API_KEY_FILE",
						"ANTHROPIC_API_KEY",
						"ANTHROPIC_API_KEY_FILE",
					),
				),
			},
		)
		if selectErr != nil {
			return resolvedRuntime{}, selectErr
		}
		config.browserSelectedMode = selection.Selected
		config.browserBackendMode = selection.BackendRequested
		config.browserFallbackReason = selection.FallbackReason
		config.BrowserNativePlugin = selection.PluginPath
		if err := validateExecutionProfile(config); err != nil {
			return resolvedRuntime{}, err
		}
	}
	browserPluginBin := ""
	if config.ExecutionProfile == "browser" {
		if _, err := readPrivateSecret(config.BrowserCredentialFile); err != nil {
			return resolvedRuntime{}, fmt.Errorf("Browser channel credential: %w", err)
		}
		browserPluginBin, err = agenthost.Resolve(context.Background(), config.BrowserPluginBin, "browser_proxy")
		if err != nil {
			return resolvedRuntime{}, err
		}
	}
	environment := removeEnvironmentKeys(os.Environ(),
		"OPENLINKER_AGENT_TOKEN", "OPENLINKER_AGENT_TOKEN_FILE", "OPENLINKER_USER_TOKEN",
		"CODEX_API_KEY", "CODEX_API_KEY_FILE", "ANTHROPIC_API_KEY", "ANTHROPIC_API_KEY_FILE")
	if providerKey != "" {
		environment = append(environment, providerKeyName+"="+providerKey)
	}
	handler, err := agentexec.NewHandler(agentexec.ProviderConfig{
		DelegationTargets:  append([]string(nil), config.DelegationTargets...),
		DelegationProxyBin: config.DelegationProxyBin, DelegationBrokerRoot: config.DelegationBrokerRoot,
		Version:  version,
		Provider: config.Provider, Bin: providerBin, Workspace: workspace, Model: config.Model,
		Sandbox: config.CodexSandbox, CodexApproval: config.CodexApproval, CodexBaseURL: config.CodexBaseURL,
		Permission: config.ClaudePermission, AllowedTools: append([]string(nil), config.AllowedTools...),
		Timeout:      time.Duration(config.TimeoutSeconds) * time.Second,
		SessionReuse: config.SessionReuse,
		SessionStore: filepath.Join(dir, "session-map", config.Provider+".json"),
		WebSearch:    config.WebSearch, Env: environment,
		ExecutionProfile:         config.ExecutionProfile,
		BrowserInteractionPolicy: config.BrowserInteractionPolicy,
		BrowserClientModeRequested: firstNonEmpty(
			config.BrowserClientMode,
			"mcp",
		),
		BrowserClientMode: firstNonEmpty(
			config.browserSelectedMode,
			config.BrowserClientMode,
			"mcp",
		),
		BrowserClientFallbackReason: config.browserFallbackReason,
		BrowserBackendModeRequested: firstNonEmpty(
			config.browserBackendMode,
			browserBackendMode(config.BrowserClientMode),
		),
		BrowserPluginBin:      browserPluginBin,
		BrowserNativePlugin:   config.BrowserNativePlugin,
		BrowserSocket:         config.BrowserSocket,
		BrowserCredentialFile: config.BrowserCredentialFile,
		BrowserLeaseRoot:      config.BrowserLeaseRoot,
		BrowserBrokerRoot:     config.BrowserBrokerRoot,
	})
	if err != nil {
		return resolvedRuntime{}, err
	}
	resolved := resolvedRuntime{
		config: config, configPath: path, stateDir: dir,
		url: config.OpenLinkerURL, runtimeURL: strings.TrimSpace(envValue(getenv, "OPENLINKER_RUNTIME_BASE")),
		nodeID: nodeID, agentToken: agentToken, agentTokenSource: agentTokenSource,
		providerAuthSource: providerSource, handler: handler, workerLock: workerLock,
	}
	keepLock = true
	return resolved, nil
}

func browserModeRequiresNativePreflight(mode string) bool {
	switch strings.TrimSpace(mode) {
	case "auto", "native", "isolated-native", "openlinker-native-chrome", "official-chrome":
		return true
	default:
		return false
	}
}

func defaultBrowserClientMode(provider, platform string) string {
	if strings.TrimSpace(provider) == "codex" && strings.TrimSpace(platform) == "linux" {
		return "auto"
	}
	return "mcp"
}

func browserBackendMode(mode string) string {
	switch strings.TrimSpace(mode) {
	case "auto":
		return "auto"
	case "openlinker-native-chrome", "official-chrome":
		return "openlinker-native-chrome"
	default:
		return "isolated"
	}
}

func browserClientModeHostRunner(
	providerBin string,
	environment []string,
) browserclientmode.RunHostCommand {
	return func(args ...string) ([]byte, error) {
		command := exec.Command(providerBin, args...) // #nosec G204 -- validated operator Provider binary and internally constructed arguments.
		command.Env = append([]string(nil), environment...)
		var stdout bytes.Buffer
		command.Stdout = &stdout
		command.Stderr = &bytes.Buffer{}
		if err := command.Run(); err != nil {
			return nil, err
		}
		if stdout.Len() > 1<<20 {
			return nil, errors.New(
				"Provider Plugin command output exceeded the limit",
			)
		}
		return stdout.Bytes(), nil
	}
}

func removeEnvironmentKeys(environment []string, keys ...string) []string {
	blocked := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		blocked[key] = struct{}{}
	}
	result := make([]string, 0, len(environment))
	for _, item := range environment {
		key, _, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		if _, found := blocked[key]; !found {
			result = append(result, item)
		}
	}
	return result
}

func (service *Service) setStatus(status Status) {
	service.mu.Lock()
	defer service.mu.Unlock()
	service.status = status
	service.persistStatusLocked()
}

func (service *Service) persistStatusLocked() {
	if service.status.StateDir == "" {
		return
	}
	if err := writePrivateJSON(filepath.Join(service.status.StateDir, "status.json"), service.status); err != nil && service.logger != nil {
		service.logger.Printf("persist Agent status: %v", err)
	}
}

func newStatus(state string) Status { return Status{State: state, UpdatedAt: nowText()} }
func nowText() string               { return time.Now().UTC().Format(time.RFC3339Nano) }

func boundedStatusMessage(err error) string {
	if err == nil {
		return ""
	}
	value := strings.TrimSpace(err.Error())
	if len(value) > 500 {
		value = value[:500]
	}
	return value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
