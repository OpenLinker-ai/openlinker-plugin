//go:build !windows

package browserruntime

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserprotocol"
)

const (
	BackendOfficialChrome      = "official_chrome_extension"
	BackendIsolated            = "isolated_chromium"
	ModeOpenLinkerNativeChrome = "openlinker-native-chrome"
	ModeOfficialChromeAlias    = "official-chrome"
)

type BrowserBackend interface {
	Engine
	AbortStartup() error
	Close() error
}

type StartupFailureClassifier interface {
	StartupFallbackReason(*browserprotocol.Failure) string
}

type ProfileSelectionEvidenceProvider interface {
	ProfileSelectionEvidence() (profileGeneration uint64, sessionRecovered bool, ok bool)
}

type BackendSelectorOptions struct {
	Official                  BrowserBackend
	OfficialEvidence          browserprotocol.BackendSelectionEvidence
	OfficialUnavailableReason string
	Isolated                  BrowserBackend
}

type BackendSelector struct {
	options  BackendSelectorOptions
	mu       sync.Mutex
	selected BrowserBackend
	evidence browserprotocol.BackendSelectionEvidence
	scope    backendSelectionScope
	closed   bool
	ops      atomic.Pointer[backendOpsSnapshot]
}

type backendOpsSnapshot struct {
	observer OpsObserverEngine
	evidence browserprotocol.BackendSelectionEvidence
	runID    string
}

type backendSelectionScope struct {
	RunID            string
	AgentID          string
	PrincipalScopeID string
	BrowserSessionID string
	SessionEpoch     uint64
	AttachmentID     string
}

func NewBackendSelector(options BackendSelectorOptions) (*BackendSelector, error) {
	if options.Isolated == nil {
		return nil, errors.New("isolated Browser backend is required")
	}
	if options.Official != nil && options.OfficialUnavailableReason != "" {
		return nil, errors.New("official Chrome backend cannot also be unavailable")
	}
	if options.Official == nil &&
		options.OfficialEvidence != (browserprotocol.BackendSelectionEvidence{}) {
		return nil, errors.New("official Chrome evidence requires an available backend")
	}
	if options.Official != nil {
		if _, ok := options.Official.(ProfileSelectionEvidenceProvider); !ok {
			return nil, errors.New("official Chrome backend must report Profile selection evidence")
		}
		evidence := options.OfficialEvidence
		evidence.RequestedMode = ModeOpenLinkerNativeChrome
		evidence.SelectedBackend = BackendOfficialChrome
		evidence.FallbackReason = ""
		if failure := evidence.Validate(); failure != nil {
			return nil, errors.New("official Chrome backend evidence is invalid")
		}
		options.OfficialEvidence = evidence
	}
	if options.OfficialUnavailableReason != "" {
		evidence := browserprotocol.BackendSelectionEvidence{
			RequestedMode:   "auto",
			SelectedBackend: BackendIsolated,
			FallbackReason:  options.OfficialUnavailableReason,
		}
		if failure := evidence.Validate(); failure != nil {
			return nil, errors.New("official Chrome unavailable reason is invalid")
		}
	}
	return &BackendSelector{options: options}, nil
}

func (selector *BackendSelector) Execute(
	ctx context.Context,
	identity browserprotocol.Identity,
	action browserprotocol.Action,
) (browserprotocol.Observation, *browserprotocol.Failure) {
	selector.mu.Lock()
	defer selector.mu.Unlock()
	if selector.closed {
		return browserprotocol.Observation{}, browserprotocol.NewFailure(
			browserprotocol.ErrorRuntimeUnavailable,
			"Browser backend selector is closed",
			false,
		)
	}
	if selector.selected != nil {
		equivalentModeContinuation := selector.scope.isEquivalentModeContinuation(
			identity,
			action,
			selector.evidence.RequestedMode,
		)
		if (action.BackendMode == "" || equivalentModeContinuation) &&
			!selector.scope.matches(identity) {
			if equivalentModeContinuation {
				action.BackendMode = ""
			}
			observation, failure := selector.selected.Execute(ctx, identity, action)
			if failure != nil {
				if action.Kind == browserprotocol.ActionPreflight {
					if selector.selected.AbortStartup() != nil {
						return browserprotocol.Observation{}, browserprotocol.NewFailure(
							browserprotocol.ErrorRuntimeUnavailable,
							"rotated Browser backend preflight cleanup failed",
							false,
						)
					}
					selector.selected = nil
					selector.evidence = browserprotocol.BackendSelectionEvidence{}
					selector.scope = backendSelectionScope{}
					selector.publishOpsSnapshotLocked()
				}
				return browserprotocol.Observation{}, failure
			}
			selector.scope = newBackendSelectionScope(identity)
			if action.Kind == browserprotocol.ActionPreflight {
				evidence := selector.evidence
				if evidence.SelectedBackend == BackendOfficialChrome {
					if selectionFailure := bindProfileSelectionEvidence(selector.selected, &evidence); selectionFailure != nil {
						_ = selector.selected.AbortStartup()
						selector.selected = nil
						selector.evidence = browserprotocol.BackendSelectionEvidence{}
						selector.scope = backendSelectionScope{}
						return browserprotocol.Observation{}, selectionFailure
					}
					selector.evidence = evidence
				}
				observation.BackendSelection = &evidence
			}
			selector.publishOpsSnapshotLocked()
			return observation, nil
		}
		if action.BackendMode != "" {
			if action.Kind == browserprotocol.ActionPreflight &&
				selector.scope.isNewGeneration(identity) {
				if selector.selected.AbortStartup() != nil {
					return browserprotocol.Observation{}, browserprotocol.NewFailure(
						browserprotocol.ErrorRuntimeUnavailable,
						"previous Browser backend Session cleanup failed",
						false,
					)
				}
				selector.selected = nil
				selector.evidence = browserprotocol.BackendSelectionEvidence{}
				selector.scope = backendSelectionScope{}
				selector.publishOpsSnapshotLocked()
			} else {
				return browserprotocol.Observation{}, browserprotocol.NewFailure(
					browserprotocol.ErrorProtocolInvalid,
					"Browser backend is already selected",
					false,
				)
			}
		}
		if selector.selected != nil && !selector.scope.matches(identity) {
			return browserprotocol.Observation{}, browserprotocol.NewFailure(
				browserprotocol.ErrorIdentityMismatch,
				"Browser backend selection belongs to another Session",
				false,
			)
		}
		if selector.selected != nil {
			observation, failure := selector.selected.Execute(ctx, identity, action)
			if failure != nil || action.Kind != browserprotocol.ActionPreflight {
				return observation, failure
			}
			evidence := selector.evidence
			if evidence.SelectedBackend == BackendOfficialChrome {
				if selectionFailure := bindProfileSelectionEvidence(selector.selected, &evidence); selectionFailure != nil {
					return browserprotocol.Observation{}, selectionFailure
				}
				selector.evidence = evidence
			}
			observation.BackendSelection = &evidence
			selector.publishOpsSnapshotLocked()
			return observation, nil
		}
	}
	if action.Kind != browserprotocol.ActionPreflight {
		return browserprotocol.Observation{}, browserprotocol.NewFailure(
			browserprotocol.ErrorEngineUnavailable,
			"Browser backend must be selected by preflight",
			false,
		)
	}
	mode := action.BackendMode
	if mode == "" {
		mode = "isolated"
	}
	action.BackendMode = ""
	switch mode {
	case "isolated":
		return selector.selectBackend(
			ctx,
			identity,
			action,
			selector.options.Isolated,
			browserprotocol.BackendSelectionEvidence{
				RequestedMode:   mode,
				SelectedBackend: BackendIsolated,
			},
		)
	case ModeOpenLinkerNativeChrome, ModeOfficialChromeAlias:
		if selector.options.Official == nil {
			return browserprotocol.Observation{}, browserprotocol.NewFailure(
				browserprotocol.ErrorEngineUnavailable,
				"official Chrome Browser backend is unavailable",
				false,
			)
		}
		evidence := selector.options.OfficialEvidence
		evidence.RequestedMode = ModeOpenLinkerNativeChrome
		return selector.selectBackend(
			ctx,
			identity,
			action,
			selector.options.Official,
			evidence,
		)
	case "auto":
		return selector.selectAuto(ctx, identity, action)
	default:
		return browserprotocol.Observation{}, browserprotocol.NewFailure(
			browserprotocol.ErrorProtocolInvalid,
			"Browser backend mode is invalid",
			false,
		)
	}
}

func (selector *BackendSelector) selectAuto(
	ctx context.Context,
	identity browserprotocol.Identity,
	action browserprotocol.Action,
) (browserprotocol.Observation, *browserprotocol.Failure) {
	reason := "official_assets_unavailable"
	if selector.options.OfficialUnavailableReason != "" {
		reason = selector.options.OfficialUnavailableReason
	}
	if selector.options.Official != nil {
		observation, failure := selector.options.Official.Execute(ctx, identity, action)
		if failure == nil {
			evidence := selector.options.OfficialEvidence
			evidence.RequestedMode = "auto"
			evidence.SelectedBackend = BackendOfficialChrome
			evidence.FallbackReason = ""
			if failure := bindProfileSelectionEvidence(selector.options.Official, &evidence); failure != nil {
				_ = selector.abortOfficialStartup()
				return browserprotocol.Observation{}, failure
			}
			selector.selected = selector.options.Official
			selector.evidence = evidence
			selector.scope = newBackendSelectionScope(identity)
			selector.publishOpsSnapshotLocked()
			selection := selector.evidence
			observation.BackendSelection = &selection
			return observation, nil
		}
		reason = "official_chrome_start_failed"
		if classifier, ok := selector.options.Official.(StartupFailureClassifier); ok {
			if classified := classifier.StartupFallbackReason(failure); classified != "" {
				reason = classified
			}
		}
		if cleanupErr := selector.abortOfficialStartup(); cleanupErr != nil {
			return browserprotocol.Observation{}, browserprotocol.NewFailure(
				browserprotocol.ErrorRuntimeUnavailable,
				"official Chrome startup cleanup failed",
				false,
			)
		}
	}
	return selector.selectBackend(
		ctx,
		identity,
		action,
		selector.options.Isolated,
		browserprotocol.BackendSelectionEvidence{
			RequestedMode:   "auto",
			SelectedBackend: BackendIsolated,
			FallbackReason:  reason,
		},
	)
}

func (selector *BackendSelector) abortOfficialStartup() error {
	if selector.options.Official == nil {
		return nil
	}
	return selector.options.Official.AbortStartup()
}

func (selector *BackendSelector) selectBackend(
	ctx context.Context,
	identity browserprotocol.Identity,
	action browserprotocol.Action,
	backend BrowserBackend,
	evidence browserprotocol.BackendSelectionEvidence,
) (browserprotocol.Observation, *browserprotocol.Failure) {
	observation, failure := backend.Execute(ctx, identity, action)
	if failure != nil {
		if cleanupErr := backend.AbortStartup(); cleanupErr != nil {
			return browserprotocol.Observation{}, browserprotocol.NewFailure(
				browserprotocol.ErrorRuntimeUnavailable,
				"Browser backend startup cleanup failed",
				false,
			)
		}
		return browserprotocol.Observation{}, failure
	}
	if evidence.SelectedBackend == BackendOfficialChrome {
		if failure := bindProfileSelectionEvidence(backend, &evidence); failure != nil {
			_ = backend.AbortStartup()
			return browserprotocol.Observation{}, failure
		}
	}
	selector.selected = backend
	selector.evidence = evidence
	selector.scope = newBackendSelectionScope(identity)
	selector.publishOpsSnapshotLocked()
	selection := selector.evidence
	observation.BackendSelection = &selection
	return observation, nil
}

func bindProfileSelectionEvidence(
	backend BrowserBackend,
	evidence *browserprotocol.BackendSelectionEvidence,
) *browserprotocol.Failure {
	provider, ok := backend.(ProfileSelectionEvidenceProvider)
	if !ok || evidence == nil {
		return browserprotocol.NewFailure(
			browserprotocol.ErrorOutputInvalid,
			"official Chrome backend did not report Profile selection evidence",
			false,
		)
	}
	generation, recovered, ok := provider.ProfileSelectionEvidence()
	if !ok || generation == 0 ||
		(evidence.ProfileGeneration != 0 && evidence.ProfileGeneration != generation) {
		return browserprotocol.NewFailure(
			browserprotocol.ErrorOutputInvalid,
			"official Chrome Profile selection evidence did not match the locked assets",
			false,
		)
	}
	evidence.ProfileGeneration = generation
	evidence.SessionRecovered = recovered
	if failure := evidence.Validate(); failure != nil {
		return failure
	}
	return nil
}

func (selector *BackendSelector) ExecuteViewer(
	ctx context.Context,
	identity browserprotocol.Identity,
	operation browserprotocol.ViewerOperation,
	input *browserprotocol.ViewerInput,
) (*browserprotocol.ViewerFrame, *browserprotocol.Failure) {
	selector.mu.Lock()
	defer selector.mu.Unlock()
	if selector.closed {
		return nil, browserprotocol.NewFailure(
			browserprotocol.ErrorRuntimeUnavailable,
			"Browser backend selector is closed",
			false,
		)
	}
	if !selector.scope.matches(identity) {
		return nil, browserprotocol.NewFailure(
			browserprotocol.ErrorIdentityMismatch,
			"Browser backend selection belongs to another Session",
			false,
		)
	}
	viewer, ok := selector.selected.(ViewerEngine)
	if !ok {
		return nil, browserprotocol.NewFailure(
			browserprotocol.ErrorViewerUnavailable,
			"selected Browser backend does not support human control",
			false,
		)
	}
	return viewer.ExecuteViewer(ctx, identity, operation, input)
}

func (selector *BackendSelector) ObserveOps(
	ctx context.Context,
	runID string,
	operation browserprotocol.OpsObserverOperation,
) (browserprotocol.OpsObserverObservation, bool, *browserprotocol.OpsObserverError) {
	if selector == nil {
		return browserprotocol.OpsObserverObservation{}, false,
			browserprotocol.NewOpsObserverError(
				browserprotocol.OpsObserverRunNotActive,
				"requested Run is not active",
			)
	}
	snapshot := selector.ops.Load()
	if snapshot == nil || snapshot.runID != runID {
		return browserprotocol.OpsObserverObservation{}, false,
			browserprotocol.NewOpsObserverError(
				browserprotocol.OpsObserverRunNotActive,
				"requested Run is not active",
			)
	}
	observation, busy, observerErr := snapshot.observer.ObserveOps(ctx, runID, operation)
	if observerErr != nil || busy {
		return browserprotocol.OpsObserverObservation{}, busy, observerErr
	}
	observation.SelectedBackend = snapshot.evidence.SelectedBackend
	if snapshot.evidence.ProfileGeneration > 0 {
		observation.ProfileGeneration = snapshot.evidence.ProfileGeneration
	}
	return observation, false, nil
}

func (selector *BackendSelector) publishOpsSnapshotLocked() {
	if selector == nil {
		return
	}
	if selector.closed || selector.selected == nil || selector.scope.RunID == "" {
		selector.ops.Store(nil)
		return
	}
	observer, ok := selector.selected.(OpsObserverEngine)
	if !ok {
		selector.ops.Store(nil)
		return
	}
	selector.ops.Store(&backendOpsSnapshot{
		observer: observer,
		evidence: selector.evidence,
		runID:    selector.scope.RunID,
	})
}

func newBackendSelectionScope(identity browserprotocol.Identity) backendSelectionScope {
	return backendSelectionScope{
		RunID:            identity.RunID,
		AgentID:          identity.AgentID,
		PrincipalScopeID: identity.PrincipalScopeID,
		BrowserSessionID: identity.BrowserSessionID,
		SessionEpoch:     identity.SessionEpoch,
		AttachmentID:     identity.AttachmentID,
	}
}

func (scope backendSelectionScope) matches(identity browserprotocol.Identity) bool {
	return scope.sameGeneration(identity) &&
		scope.AttachmentID == identity.AttachmentID
}

func (scope backendSelectionScope) sameGeneration(
	identity browserprotocol.Identity,
) bool {
	return scope.AgentID == identity.AgentID &&
		scope.PrincipalScopeID == identity.PrincipalScopeID &&
		scope.BrowserSessionID == identity.BrowserSessionID &&
		scope.SessionEpoch == identity.SessionEpoch
}

func (scope backendSelectionScope) isEquivalentModeContinuation(
	identity browserprotocol.Identity,
	action browserprotocol.Action,
	lockedRequestedMode string,
) bool {
	return action.Kind == browserprotocol.ActionPreflight &&
		action.BackendMode != "" &&
		scope.sameGeneration(identity) &&
		scope.AttachmentID != identity.AttachmentID &&
		equivalentBackendMode(action.BackendMode, lockedRequestedMode)
}

func (scope backendSelectionScope) isNewGeneration(
	identity browserprotocol.Identity,
) bool {
	return !scope.sameGeneration(identity)
}

func equivalentBackendMode(left, right string) bool {
	leftMode, leftOK := canonicalBackendMode(left)
	rightMode, rightOK := canonicalBackendMode(right)
	return leftOK && rightOK && leftMode == rightMode
}

func canonicalBackendMode(mode string) (string, bool) {
	switch mode {
	case ModeOpenLinkerNativeChrome, ModeOfficialChromeAlias:
		return ModeOpenLinkerNativeChrome, true
	case "auto", "isolated":
		return mode, true
	default:
		return "", false
	}
}

func (selector *BackendSelector) Close() error {
	selector.mu.Lock()
	if selector.closed {
		selector.mu.Unlock()
		return nil
	}
	selector.closed = true
	selector.selected = nil
	selector.evidence = browserprotocol.BackendSelectionEvidence{}
	selector.scope = backendSelectionScope{}
	selector.publishOpsSnapshotLocked()
	selector.mu.Unlock()
	var officialErr error
	if selector.options.Official != nil {
		officialErr = selector.options.Official.Close()
	}
	return errors.Join(officialErr, selector.options.Isolated.Close())
}
