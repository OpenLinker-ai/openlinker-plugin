package agentexec

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	openlinker "github.com/OpenLinker-ai/openlinker-go"
	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserclient"
	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserplugin"
	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserprotocol"
)

const (
	browserSessionStateVersion      = 2
	maxBrowserSessionStateSize      = 16 << 10
	browserSessionEvidenceDomain    = "openlinker.browser-session.v1\x00"
	browserAttachmentEvidenceDomain = "openlinker.browser-attachment.v1\x00"
)

type browserExecutionProvider struct {
	base        Provider
	config      ProviderConfig
	preflight   func(context.Context, *browserRunLease) error
	repreflight func(context.Context, *browserRunLease) error
}

type browserSessionState struct {
	Version             int                        `json:"version"`
	SessionKeyHash      string                     `json:"session_key_hash"`
	BrowserSessionID    string                     `json:"browser_session_id"`
	SessionEpoch        uint64                     `json:"session_epoch"`
	ControlEpoch        uint64                     `json:"control_epoch"`
	Controller          browserprotocol.Controller `json:"controller"`
	RuntimeSessionID    string                     `json:"runtime_session_id"`
	RuntimeSessionEpoch int64                      `json:"runtime_session_epoch"`
	RuntimeAttachmentID string                     `json:"runtime_attachment_id"`
	InteractionPolicy   string                     `json:"browser_interaction_policy"`
	PolicyGeneration    int64                      `json:"browser_interaction_policy_generation"`
	MutationOriginsHash string                     `json:"browser_mutation_origins_sha256"`
	UpdatedAt           string                     `json:"updated_at"`
}

type browserRunLease struct {
	config       ProviderConfig
	statePath    string
	activePath   string
	runPath      string
	state        browserSessionState
	identity     browserprotocol.Identity
	expiresAt    time.Time
	releaseScope func()
	mu           sync.Mutex
	runtimeUsed  bool
	environment  *browserprotocol.EnvironmentEvidence
	backend      *browserprotocol.BackendSelectionEvidence
	closed       bool
}

var browserRootLocks = struct {
	sync.Mutex
	entries map[string]*sessionLockEntry
}{entries: map[string]*sessionLockEntry{}}

func newBrowserExecutionProvider(base Provider, config ProviderConfig) (Provider, error) {
	for label, value := range map[string]string{
		"Browser plugin binary":   config.BrowserPluginBin,
		"Browser socket":          config.BrowserSocket,
		"Browser credential file": config.BrowserCredentialFile,
		"Browser lease root":      config.BrowserLeaseRoot,
		"Browser broker root":     config.BrowserBrokerRoot,
	} {
		if strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("%s is required for the browser execution profile", label)
		}
	}
	if !filepath.IsAbs(config.BrowserSocket) ||
		!filepath.IsAbs(config.BrowserCredentialFile) ||
		!filepath.IsAbs(config.BrowserLeaseRoot) ||
		!filepath.IsAbs(config.BrowserBrokerRoot) {
		return nil, errors.New("Browser execution paths must be absolute")
	}
	if config.BrowserInteractionPolicy != "restricted" &&
		config.BrowserInteractionPolicy != "full" {
		return nil, errors.New(
			"Browser interaction policy must be restricted or full",
		)
	}
	return &browserExecutionProvider{
		base:        base,
		config:      config,
		preflight:   preflightBrowserRuntime,
		repreflight: preflightSelectedBrowserRuntime,
	}, nil
}

func (provider *browserExecutionProvider) Run(
	ctx context.Context,
	run RunContext,
) (result openlinker.RuntimeResult, resultErr error) {
	emitBrowserLifecycle(run.Emit, "preparing", "")
	lease, err := acquireBrowserRunLease(provider.config, run)
	if err != nil {
		emitBrowserLifecycle(run.Emit, "failed", "failed")
		return openlinker.RuntimeResult{}, err
	}
	err = provider.preflight(ctx, lease)
	if err != nil {
		runtimeErr := lease.closeBrowserRuntimeWithFailureFence()
		leaseErr := lease.Close()
		emitBrowserLifecycle(run.Emit, "failed", "failed")
		return openlinker.RuntimeResult{}, errors.Join(err, runtimeErr, leaseErr)
	}
	humanControl := newBrowserHumanControl(
		lease,
		run.RuntimeExtensions,
		run.Emit,
	)
	go humanControl.run(ctx)
	defer humanControl.stopFrames()
	mutationJournal := newBrowserMutationJournal(lease.identity)
	broker, err := startBrowserToolBroker(
		ctx,
		provider.config.Provider,
		provider.config.BrowserBrokerRoot,
		lease,
		humanControl.executor,
		mutationJournal,
		func() {
			if run.Emit == nil {
				return
			}
			// Browser MCP action events are suppressed to keep durable event
			// volume O(1). Publish one bounded marker at the trusted broker when
			// the Agent first obtains an executor, which proves real Browser work
			// without persisting action inputs, outputs, URLs, or action counts.
			_ = run.Emit("run.status.changed", map[string]any{
				"status":    "provider_tool_started",
				"provider":  provider.config.Provider,
				"phase":     "started",
				"tool_kind": "mcp_tool",
			})
		},
	)
	if err != nil {
		runtimeErr := lease.closeBrowserRuntimeWithFailureFence()
		leaseErr := lease.Close()
		emitBrowserLifecycle(run.Emit, "failed", "failed")
		return openlinker.RuntimeResult{}, errors.Join(err, runtimeErr, leaseErr)
	}
	run.Browser = &BrowserRunContext{
		PluginBin:  provider.config.BrowserPluginBin,
		ToolSocket: broker.SocketPath(),
	}
	refreshBrowserRunContext(run.Browser, lease)
	run.Browser.Rotate = func() error {
		if err := lease.Rotate(); err != nil {
			return err
		}
		if err := provider.repreflight(ctx, lease); err != nil {
			return err
		}
		refreshBrowserRunContext(run.Browser, lease)
		return nil
	}
	runConfig := providerConfigForBrowserRun(provider.config, run.Browser)
	emitBrowserLifecycle(
		run.Emit,
		"ready",
		"",
		browserClientEvidence(runConfig),
		browserAuthorityEvidence(lease.identity),
	)
	status := "failed"
	defer func() {
		brokerErr := broker.Close()
		runtimeErr := lease.closeBrowserRuntimeWithFailureFence()
		leaseErr := lease.Close()
		if cleanupErr := errors.Join(brokerErr, runtimeErr, leaseErr); cleanupErr != nil {
			status = "failed"
			if resultErr == nil {
				result = openlinker.RuntimeResult{}
				resultErr = fmt.Errorf("close Browser execution attachment: %w", cleanupErr)
			}
		}
		emitBrowserLifecycle(
			run.Emit,
			"closed",
			status,
			browserAuthorityEvidence(lease.identity),
			mutationJournal.summary(status),
		)
	}()
	result, resultErr = provider.base.Run(ctx, run)
	if resultErr == nil {
		status = "success"
	}
	if output, ok := result.Output.(map[string]any); ok {
		finalRunConfig := providerConfigForBrowserRun(provider.config, run.Browser)
		evidence := browserClientEvidence(finalRunConfig)
		copied := make(map[string]any, len(output)+2+len(evidence))
		for key, value := range output {
			copied[key] = value
		}
		copied["browser_execution_profile"] = "isolated"
		copied["browser_tool"] = "browser_session"
		for key, value := range evidence {
			copied[key] = value
		}
		for key, value := range browserAuthorityEvidence(lease.identity) {
			copied[key] = value
		}
		result.Output = copied
	}
	return result, resultErr
}

func browserAuthorityEvidence(identity browserprotocol.Identity) map[string]any {
	return map[string]any{
		"browser_interaction_policy":            identity.BrowserInteractionPolicy,
		"browser_interaction_policy_generation": identity.BrowserInteractionPolicyGeneration,
		"browser_mutation_origins":              append([]string{}, identity.BrowserMutationOrigins...),
		"browser_mutation_origins_sha256":       identity.BrowserMutationOriginsSHA256,
		"browser_contract_id":                   browserprotocol.ContractID,
		"browser_session_sha256":                browserIdentityEvidenceSHA256(browserSessionEvidenceDomain, identity.BrowserSessionID),
		"browser_session_epoch":                 identity.SessionEpoch,
		"browser_attachment_sha256":             browserIdentityEvidenceSHA256(browserAttachmentEvidenceDomain, identity.AttachmentID),
	}
}

func browserIdentityEvidenceSHA256(domain, value string) string {
	digest := sha256.Sum256([]byte(domain + value))
	return hex.EncodeToString(digest[:])
}

func preflightBrowserRuntime(
	ctx context.Context,
	lease *browserRunLease,
) error {
	return preflightBrowserRuntimeMode(
		ctx,
		lease,
		lease.config.BrowserBackendModeRequested,
	)
}

func preflightSelectedBrowserRuntime(
	ctx context.Context,
	lease *browserRunLease,
) error {
	return preflightBrowserRuntimeMode(ctx, lease, "")
}

func preflightBrowserRuntimeMode(
	ctx context.Context,
	lease *browserRunLease,
	backendMode string,
) error {
	lease.mu.Lock()
	lease.environment = nil
	lease.backend = nil
	lease.mu.Unlock()
	client, err := lease.browserClient()
	if err != nil {
		return err
	}
	observation, failure := client.Execute(ctx, browserprotocol.Action{
		Kind:        browserprotocol.ActionPreflight,
		Observation: browserprotocol.ObservationSemantic,
		BackendMode: backendMode,
	})
	if failure != nil {
		return failure
	}
	if observation.Environment == nil {
		return errors.New("Browser preflight did not return environment evidence")
	}
	environment := *observation.Environment
	if failure := environment.Validate(); failure != nil {
		return failure
	}
	backend := observation.BackendSelection
	if backend == nil && requestedBackendMode(backendMode) == "isolated" {
		backend = &browserprotocol.BackendSelectionEvidence{
			RequestedMode:   "isolated",
			SelectedBackend: "isolated_chromium",
		}
	}
	if backend == nil || backend.Validate() != nil {
		return errors.New("Browser preflight did not return valid backend selection evidence")
	}
	lease.mu.Lock()
	lease.environment = &environment
	selection := *backend
	lease.backend = &selection
	lease.mu.Unlock()
	return nil
}

func refreshBrowserRunContext(run *BrowserRunContext, lease *browserRunLease) {
	if run == nil || lease == nil {
		return
	}
	selection := lease.backendSelection()
	lease.mu.Lock()
	generation := lease.identity.SessionEpoch
	lease.mu.Unlock()
	run.BackendSelected = selection.SelectedBackend
	run.BackendFallbackReason = selection.FallbackReason
	run.SelectionGeneration = generation
	run.ProfileGeneration = selection.ProfileGeneration
	run.SessionRecovered = selection.SessionRecovered
	run.AssetManifestSHA256 = selection.AssetManifestSHA256
	run.ExtensionID = selection.ExtensionID
	run.ExtensionVersion = selection.ExtensionVersion
	run.NativeHostProtocol = selection.NativeHostProtocol
}

func (lease *browserRunLease) browserEvidenceSnapshot() (
	browserplugin.EvidenceSnapshot,
	error,
) {
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.closed || lease.environment == nil || lease.backend == nil {
		return browserplugin.EvidenceSnapshot{}, errors.New(
			"Browser attachment evidence is unavailable",
		)
	}
	return browserplugin.EvidenceSnapshot{
		Environment:                        *lease.environment,
		BackendSelection:                   *lease.backend,
		BrowserSessionID:                   lease.identity.BrowserSessionID,
		SessionEpoch:                       lease.identity.SessionEpoch,
		ControlEpoch:                       lease.identity.ControlEpoch,
		BrowserInteractionPolicy:           lease.identity.BrowserInteractionPolicy,
		BrowserInteractionPolicyGeneration: lease.identity.BrowserInteractionPolicyGeneration,
		BrowserMutationOrigins:             append([]string{}, lease.identity.BrowserMutationOrigins...),
		BrowserMutationOriginsSHA256:       lease.identity.BrowserMutationOriginsSHA256,
	}, nil
}

func (lease *browserRunLease) backendSelection() browserprotocol.BackendSelectionEvidence {
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.backend == nil {
		return browserprotocol.BackendSelectionEvidence{}
	}
	return *lease.backend
}

func emitBrowserLifecycle(
	emit func(string, any) error,
	phase,
	status string,
	evidence ...map[string]any,
) {
	if emit == nil {
		return
	}
	payload := map[string]any{
		"phase":             phase,
		"execution_profile": "browser",
		"runtime":           "isolated",
	}
	if status != "" {
		payload["status"] = status
	}
	for _, values := range evidence {
		for key, value := range values {
			payload[key] = value
		}
	}
	_ = emit("run.browser.lifecycle", payload)
}

func (lease *browserRunLease) browserClient() (browserplugin.Executor, error) {
	lease.mu.Lock()
	if lease.closed {
		lease.mu.Unlock()
		return nil, errors.New("Browser lease is closed")
	}
	lease.runtimeUsed = true
	lease.mu.Unlock()
	return browserclient.New(browserclient.Config{
		SocketPath:     lease.config.BrowserSocket,
		CredentialFile: lease.config.BrowserCredentialFile,
		LeaseFile:      lease.runPath,
	})
}

func (lease *browserRunLease) closeBrowserRuntime() error {
	if lease == nil {
		return nil
	}
	lease.mu.Lock()
	used := lease.runtimeUsed
	closed := lease.closed
	selected := lease.backend != nil
	lease.mu.Unlock()
	if !used || closed || !selected {
		return nil
	}
	identity, err := lease.identitySnapshot()
	if err != nil {
		return err
	}
	if identity.Controller != browserprotocol.ControllerAgent {
		if _, err := lease.transitionController(
			identity.Controller,
			browserprotocol.ControllerAgent,
			0,
		); err != nil {
			return err
		}
	}
	client, err := browserclient.New(browserclient.Config{
		SocketPath:     lease.config.BrowserSocket,
		CredentialFile: lease.config.BrowserCredentialFile,
		LeaseFile:      lease.runPath,
	})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, failure := client.Execute(ctx, browserprotocol.Action{
		Kind: browserprotocol.ActionClose,
	}); failure != nil {
		if failure.Code == browserprotocol.ErrorIdentityMismatch ||
			failure.Code == browserprotocol.ErrorStaleControlEpoch {
			return nil
		}
		return failure
	}
	return nil
}

func (lease *browserRunLease) closeBrowserRuntimeWithFailureFence() error {
	err := lease.closeBrowserRuntime()
	if err == nil {
		return nil
	}
	return errors.Join(err, lease.Rotate())
}

func acquireBrowserRunLease(
	config ProviderConfig,
	run RunContext,
) (*browserRunLease, error) {
	if run.Authority == nil {
		return nil, errors.New("Browser execution requires Core-owned Runtime authority")
	}
	if run.Conversation != nil && run.Conversation.Source != "core" {
		return nil, errors.New("Browser execution rejected untrusted conversation identity")
	}
	authority := run.Authority
	if authority.ExecutionProfile != "browser" ||
		authority.BrowserInteractionPolicy != config.BrowserInteractionPolicy ||
		authority.BrowserInteractionPolicyGeneration < 1 {
		return nil, errors.New(
			"Browser execution policy does not match Core-owned Runtime authority",
		)
	}
	if failure := browserprotocol.ValidateMutationOrigins(
		authority.BrowserInteractionPolicy,
		authority.BrowserMutationOrigins,
		authority.BrowserMutationOriginsSHA256,
	); failure != nil {
		return nil, failure
	}
	sessionKey := conversationSessionKey(run)
	if sessionKey == "" {
		sessionKey = strings.TrimSpace(run.RunID)
	}
	if sessionKey == "" {
		return nil, errors.New("Browser execution requires a conversation or Run identity")
	}
	releaseScope := lockBrowserRoot(config.BrowserLeaseRoot)
	keepLock := false
	defer func() {
		if !keepLock {
			releaseScope()
		}
	}()
	for _, dir := range []string{
		config.BrowserLeaseRoot,
		filepath.Join(config.BrowserLeaseRoot, "sessions"),
		filepath.Join(config.BrowserLeaseRoot, "runs"),
	} {
		if err := ensurePrivateBrowserDirectory(dir); err != nil {
			return nil, err
		}
	}
	sessionDigest := sha256.Sum256([]byte(
		run.AgentID + "\x00" +
			run.Authority.PrincipalScopeID + "\x00" +
			sessionKey,
	))
	sessionKeyHash := hex.EncodeToString(sessionDigest[:])
	statePath := filepath.Join(
		config.BrowserLeaseRoot,
		"sessions",
		sessionKeyHash+".json",
	)
	if err := pruneBrowserSessionStates(
		config.BrowserLeaseRoot,
		statePath,
		time.Now().UTC(),
	); err != nil {
		return nil, err
	}
	state, err := readBrowserSessionState(statePath)
	if err != nil {
		return nil, err
	}
	if state.Version == 0 {
		browserSessionID, idErr := newBrowserUUID()
		if idErr != nil {
			return nil, idErr
		}
		state = browserSessionState{
			Version:             browserSessionStateVersion,
			SessionKeyHash:      sessionKeyHash,
			BrowserSessionID:    browserSessionID,
			SessionEpoch:        1,
			Controller:          browserprotocol.ControllerAgent,
			InteractionPolicy:   authority.BrowserInteractionPolicy,
			PolicyGeneration:    authority.BrowserInteractionPolicyGeneration,
			MutationOriginsHash: authority.BrowserMutationOriginsSHA256,
		}
	} else if state.SessionKeyHash != sessionKeyHash {
		return nil, errors.New("Browser session state identity does not match")
	}
	if state.Version == 1 {
		state.Version = browserSessionStateVersion
		state.SessionEpoch++
		state.InteractionPolicy = authority.BrowserInteractionPolicy
		state.PolicyGeneration = authority.BrowserInteractionPolicyGeneration
		state.MutationOriginsHash = authority.BrowserMutationOriginsSHA256
	}
	if state.RuntimeSessionID != "" &&
		(state.RuntimeSessionID != authority.RuntimeSessionID ||
			state.RuntimeSessionEpoch != authority.RuntimeSessionEpoch ||
			state.RuntimeAttachmentID != authority.RuntimeAttachmentID) {
		state.SessionEpoch++
	}
	if state.InteractionPolicy != authority.BrowserInteractionPolicy ||
		state.PolicyGeneration != authority.BrowserInteractionPolicyGeneration ||
		state.MutationOriginsHash != authority.BrowserMutationOriginsSHA256 {
		state.SessionEpoch++
	}
	state.RuntimeSessionID = authority.RuntimeSessionID
	state.RuntimeSessionEpoch = authority.RuntimeSessionEpoch
	state.RuntimeAttachmentID = authority.RuntimeAttachmentID
	state.InteractionPolicy = authority.BrowserInteractionPolicy
	state.PolicyGeneration = authority.BrowserInteractionPolicyGeneration
	state.MutationOriginsHash = authority.BrowserMutationOriginsSHA256
	state.ControlEpoch++
	state.Controller = browserprotocol.ControllerAgent
	state.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	attachmentID, err := newBrowserUUID()
	if err != nil {
		return nil, err
	}
	identity := browserprotocol.Identity{
		RunID:                              run.RunID,
		AgentID:                            run.AgentID,
		PrincipalScopeID:                   authority.PrincipalScopeID,
		BrowserSessionID:                   state.BrowserSessionID,
		SessionEpoch:                       state.SessionEpoch,
		AttachmentID:                       attachmentID,
		ControlEpoch:                       state.ControlEpoch,
		Controller:                         state.Controller,
		BrowserInteractionPolicy:           authority.BrowserInteractionPolicy,
		BrowserInteractionPolicyGeneration: authority.BrowserInteractionPolicyGeneration,
		BrowserMutationOrigins:             append([]string{}, authority.BrowserMutationOrigins...),
		BrowserMutationOriginsSHA256:       authority.BrowserMutationOriginsSHA256,
	}
	if failure := identity.Validate(); failure != nil {
		return nil, failure
	}
	expiresAt, err := browserLeaseExpiry(config, run)
	if err != nil {
		return nil, err
	}
	lease := &browserRunLease{
		config:     config,
		statePath:  statePath,
		activePath: filepath.Join(config.BrowserLeaseRoot, "active-lease.json"),
		runPath: filepath.Join(
			config.BrowserLeaseRoot,
			"runs",
			run.RunID+".json",
		),
		state:        state,
		identity:     identity,
		expiresAt:    expiresAt,
		releaseScope: releaseScope,
	}
	if err := lease.persist(); err != nil {
		return nil, err
	}
	keepLock = true
	return lease, nil
}

type browserSessionPruneCandidate struct {
	path      string
	updatedAt time.Time
}

func pruneBrowserSessionStates(root, currentPath string, now time.Time) error {
	sessionRoot := filepath.Join(root, "sessions")
	directory, err := os.Open(sessionRoot) // #nosec G304 -- validated operator-owned state root.
	if err != nil {
		return err
	}
	entries, readErr := directory.ReadDir(browserprotocol.BrowserSessionStateScanLimit + 1)
	closeErr := directory.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return readErr
	}
	if closeErr != nil {
		return closeErr
	}
	if len(entries) > browserprotocol.BrowserSessionStateScanLimit {
		return errors.New("Browser Session state exceeds the bounded prune scan limit")
	}

	currentExists := false
	if info, statErr := os.Lstat(currentPath); statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return errors.New("Browser Session state path is invalid")
		}
		currentExists = true
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}

	activeSessionID, err := activeBrowserSessionID(root, now)
	if err != nil {
		return err
	}
	candidates := make([]browserSessionPruneCandidate, 0, len(entries))
	protected := 0
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 ||
			filepath.Ext(entry.Name()) != ".json" {
			return errors.New("Browser Session directory contains an invalid entry")
		}
		path := filepath.Join(sessionRoot, entry.Name())
		state, stateErr := readBrowserSessionState(path)
		if stateErr != nil {
			return stateErr
		}
		updatedAt, parseErr := time.Parse(time.RFC3339Nano, state.UpdatedAt)
		if parseErr != nil {
			return errors.New("Browser Session state timestamp is invalid")
		}
		if path == currentPath ||
			(activeSessionID != "" && state.BrowserSessionID == activeSessionID) {
			protected++
			continue
		}
		candidates = append(candidates, browserSessionPruneCandidate{
			path:      path,
			updatedAt: updatedAt,
		})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].updatedAt.Equal(candidates[j].updatedAt) {
			return candidates[i].path < candidates[j].path
		}
		return candidates[i].updatedAt.Before(candidates[j].updatedAt)
	})

	target := browserprotocol.BrowserSessionStateLimit
	if !currentExists {
		target--
	}
	remaining := len(candidates) + protected
	oldestAllowed := now.Add(-browserprotocol.BrowserStateRetention)
	for _, candidate := range candidates {
		if !candidate.updatedAt.Before(oldestAllowed) && remaining <= target {
			break
		}
		if err := removePrivateBrowserFile(candidate.path); err != nil {
			return err
		}
		remaining--
	}
	if remaining > target {
		return errors.New("Browser Session state cannot be pruned without deleting an active Session")
	}
	return nil
}

func activeBrowserSessionID(root string, now time.Time) (string, error) {
	lease, err := readBrowserLease(filepath.Join(root, "active-lease.json"))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if failure := lease.Validate(now); failure != nil {
		if failure.Code == browserprotocol.ErrorDeadlineExceeded {
			return "", nil
		}
		return "", errors.New("active Browser lease is invalid")
	}
	return lease.Identity.BrowserSessionID, nil
}

func browserLeaseExpiry(config ProviderConfig, run RunContext) (time.Time, error) {
	now := time.Now().UTC()
	expiresAt := now.Add(config.Timeout)
	if config.Timeout <= 0 {
		expiresAt = now.Add(30 * time.Minute)
	}
	for _, deadline := range []time.Time{run.AttemptDeadlineAt, run.RunDeadlineAt} {
		if !deadline.IsZero() && deadline.Before(expiresAt) {
			expiresAt = deadline.UTC()
		}
	}
	if !expiresAt.After(now) {
		return time.Time{}, errors.New("Browser execution deadline has elapsed")
	}
	return expiresAt, nil
}

func (lease *browserRunLease) Rotate() error {
	if lease == nil {
		return errors.New("Browser lease is unavailable")
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.closed {
		return errors.New("Browser lease is closed")
	}
	attachmentID, err := newBrowserUUID()
	if err != nil {
		return err
	}
	lease.state.SessionEpoch++
	lease.state.ControlEpoch++
	lease.state.Controller = browserprotocol.ControllerAgent
	lease.state.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	lease.identity.SessionEpoch = lease.state.SessionEpoch
	lease.identity.ControlEpoch = lease.state.ControlEpoch
	lease.identity.Controller = lease.state.Controller
	lease.identity.AttachmentID = attachmentID
	if err := lease.persist(); err != nil {
		return err
	}
	return nil
}

func (lease *browserRunLease) Pause() (browserprotocol.Identity, error) {
	return lease.transitionController(
		browserprotocol.ControllerAgent,
		browserprotocol.ControllerNone,
		0,
	)
}

func (lease *browserRunLease) transitionController(
	expected browserprotocol.Controller,
	next browserprotocol.Controller,
	targetEpoch uint64,
) (browserprotocol.Identity, error) {
	if lease == nil {
		return browserprotocol.Identity{}, errors.New("Browser lease is unavailable")
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.closed {
		return browserprotocol.Identity{}, errors.New("Browser lease is closed")
	}
	if lease.identity.Controller != expected {
		return browserprotocol.Identity{}, fmt.Errorf(
			"Browser controller is %s, expected %s",
			lease.identity.Controller,
			expected,
		)
	}
	nextEpoch := lease.identity.ControlEpoch + 1
	if targetEpoch != 0 && targetEpoch != nextEpoch {
		return browserprotocol.Identity{}, errors.New(
			"Browser control transition epoch is stale",
		)
	}
	previousState := lease.state
	previousIdentity := lease.identity
	lease.state.ControlEpoch = nextEpoch
	lease.state.Controller = next
	lease.state.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	lease.identity.ControlEpoch = nextEpoch
	lease.identity.Controller = next
	if err := lease.persist(); err != nil {
		lease.state = previousState
		lease.identity = previousIdentity
		return browserprotocol.Identity{}, err
	}
	return lease.identity, nil
}

func (lease *browserRunLease) identitySnapshot() (
	browserprotocol.Identity,
	error,
) {
	if lease == nil {
		return browserprotocol.Identity{}, errors.New("Browser lease is unavailable")
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.closed {
		return browserprotocol.Identity{}, errors.New("Browser lease is closed")
	}
	return lease.identity, nil
}

func (lease *browserRunLease) persist() error {
	unlock, err := browserclient.LockAuthority(filepath.Dir(lease.activePath))
	if err != nil {
		return fmt.Errorf("lock Browser lease authority: %w", err)
	}
	defer unlock()
	if err := writePrivateBrowserJSON(lease.statePath, lease.state); err != nil {
		return fmt.Errorf("persist Browser session state: %w", err)
	}
	envelope := browserclient.Lease{
		ContractID: browserclient.LeaseContractID,
		ExpiresAt:  lease.expiresAt,
		Identity:   lease.identity,
	}
	if err := writePrivateBrowserJSON(lease.activePath, envelope); err != nil {
		return fmt.Errorf("activate Browser lease: %w", err)
	}
	if err := writePrivateBrowserJSON(lease.runPath, envelope); err != nil {
		lease.removeActiveIfCurrent()
		return fmt.Errorf("persist per-Run Browser lease: %w", err)
	}
	return nil
}

func (lease *browserRunLease) Close() error {
	if lease == nil {
		return nil
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.closed {
		return nil
	}
	defer func() {
		if lease.releaseScope != nil {
			lease.releaseScope()
			lease.releaseScope = nil
		}
	}()
	unlock, lockErr := browserclient.LockAuthority(filepath.Dir(lease.activePath))
	if lockErr != nil {
		return fmt.Errorf("lock Browser lease authority: %w", lockErr)
	}
	defer unlock()
	runErr := removePrivateBrowserFile(lease.runPath)
	activeErr := lease.removeActiveIfCurrent()
	if cleanupErr := errors.Join(runErr, activeErr); cleanupErr != nil {
		return cleanupErr
	}
	lease.closed = true
	return nil
}

func (lease *browserRunLease) removeActiveIfCurrent() error {
	current, err := readBrowserLease(lease.activePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !browserprotocol.SameIdentity(current.Identity, lease.identity) {
		return nil
	}
	return removePrivateBrowserFile(lease.activePath)
}

func readBrowserSessionState(path string) (browserSessionState, error) {
	raw, err := readPrivateBrowserFile(path, maxBrowserSessionStateSize)
	if errors.Is(err, os.ErrNotExist) {
		return browserSessionState{}, nil
	}
	if err != nil {
		return browserSessionState{}, err
	}
	var state browserSessionState
	if err := decodeStrictBrowserJSON(raw, &state); err != nil {
		return browserSessionState{}, errors.New("Browser session state is invalid")
	}
	// Session-state files predate the explicit controller field. They were
	// agent-owned by definition, so upgrade them in memory and persist the
	// explicit value on the next authority mutation.
	if state.Controller == "" {
		state.Controller = browserprotocol.ControllerAgent
	}
	if (state.Version != 1 && state.Version != browserSessionStateVersion) ||
		state.SessionKeyHash == "" ||
		state.BrowserSessionID == "" ||
		state.SessionEpoch == 0 ||
		(state.Controller != browserprotocol.ControllerAgent &&
			state.Controller != browserprotocol.ControllerNone &&
			state.Controller != browserprotocol.ControllerHuman) {
		return browserSessionState{}, errors.New("Browser session state is invalid")
	}
	if state.Version == browserSessionStateVersion {
		if state.InteractionPolicy == "" || state.PolicyGeneration < 1 ||
			state.MutationOriginsHash == "" {
			return browserSessionState{}, errors.New("Browser session state is invalid")
		}
	} else if state.InteractionPolicy != "" || state.PolicyGeneration != 0 ||
		state.MutationOriginsHash != "" {
		return browserSessionState{}, errors.New("Browser session state is invalid")
	}
	return state, nil
}

func readBrowserLease(path string) (browserclient.Lease, error) {
	raw, err := readPrivateBrowserFile(path, maxBrowserSessionStateSize)
	if err != nil {
		return browserclient.Lease{}, err
	}
	var lease browserclient.Lease
	if err := decodeStrictBrowserJSON(raw, &lease); err != nil {
		return browserclient.Lease{}, errors.New("Browser lease is invalid")
	}
	return lease, nil
}

func readPrivateBrowserFile(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 ||
		!info.Mode().IsRegular() ||
		info.Mode().Perm()&0o077 != 0 ||
		!sessionFileOwnedByCurrentUser(info) ||
		info.Size() <= 0 ||
		info.Size() > limit {
		return nil, errors.New("Browser state file must be an owner-only regular non-symlink file")
	}
	file, err := os.Open(path) // #nosec G304 -- private state path is operator-controlled and validated.
	if err != nil {
		return nil, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return nil, errors.New("Browser state file exceeds its size limit")
	}
	return raw, nil
}

func writePrivateBrowserJSON(path string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	dir := filepath.Dir(path)
	if err := ensurePrivateBrowserDirectory(dir); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 ||
			!info.Mode().IsRegular() ||
			info.Mode().Perm()&0o077 != 0 ||
			!sessionFileOwnedByCurrentUser(info) {
			return errors.New("Browser state destination is not an owner-only regular file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	temporary, err := os.CreateTemp(dir, ".browser-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	keep := false
	defer func() {
		_ = temporary.Close()
		if !keep {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	if _, err := temporary.Write(raw); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := replaceFileAtomic(temporaryPath, path); err != nil {
		return err
	}
	keep = true
	return nil
}

func ensurePrivateBrowserDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 ||
		!info.IsDir() ||
		info.Mode().Perm()&0o077 != 0 ||
		!sessionFileOwnedByCurrentUser(info) {
		return errors.New("Browser state directory must be owner-only and not a symlink")
	}
	return nil
}

func removePrivateBrowserFile(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 ||
		!info.Mode().IsRegular() ||
		!sessionFileOwnedByCurrentUser(info) {
		return errors.New("refusing to remove an unowned Browser state path")
	}
	return os.Remove(path)
}

func decodeStrictBrowserJSON(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("unexpected trailing Browser JSON value")
		}
		return err
	}
	return nil
}

func newBrowserUUID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	return fmt.Sprintf(
		"%08x-%04x-%04x-%04x-%012x",
		value[0:4],
		value[4:6],
		value[6:8],
		value[8:10],
		value[10:16],
	), nil
}

func lockBrowserRoot(root string) func() {
	key := filepath.Clean(root)
	browserRootLocks.Lock()
	entry := browserRootLocks.entries[key]
	if entry == nil {
		entry = &sessionLockEntry{}
		browserRootLocks.entries[key] = entry
	}
	entry.refs++
	browserRootLocks.Unlock()
	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		browserRootLocks.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(browserRootLocks.entries, key)
		}
		browserRootLocks.Unlock()
	}
}
