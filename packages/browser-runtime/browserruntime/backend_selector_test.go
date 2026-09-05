//go:build !windows

package browserruntime

import (
	"context"
	"testing"
	"time"

	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserprotocol"
)

type selectorTestBackend struct {
	failure           *browserprotocol.Failure
	fallbackReason    string
	calls             []browserprotocol.Action
	closed            bool
	aborted           bool
	profileGeneration uint64
	sessionRecovered  bool
	opsCalls          int
}

func (backend *selectorTestBackend) ObserveOps(
	_ context.Context,
	runID string,
	_ browserprotocol.OpsObserverOperation,
) (browserprotocol.OpsObserverObservation, bool, *browserprotocol.OpsObserverError) {
	backend.opsCalls++
	return browserprotocol.OpsObserverObservation{RunID: runID}, false, nil
}

func (backend *selectorTestBackend) ProfileSelectionEvidence() (uint64, bool, bool) {
	generation := backend.profileGeneration
	if generation == 0 {
		generation = 7
	}
	return generation, backend.sessionRecovered, true
}

func (backend *selectorTestBackend) Execute(
	_ context.Context,
	_ browserprotocol.Identity,
	action browserprotocol.Action,
) (browserprotocol.Observation, *browserprotocol.Failure) {
	backend.calls = append(backend.calls, action)
	if backend.failure != nil {
		return browserprotocol.Observation{}, backend.failure
	}
	return browserprotocol.Observation{PageStateID: "test-state"}, nil
}

func (backend *selectorTestBackend) StartupFallbackReason(
	*browserprotocol.Failure,
) string {
	return backend.fallbackReason
}

func (backend *selectorTestBackend) Close() error {
	backend.closed = true
	return nil
}

func (backend *selectorTestBackend) AbortStartup() error {
	backend.aborted = true
	return nil
}

func TestBackendSelectorAutoPrefersOfficialAndLocksSelection(t *testing.T) {
	official := &selectorTestBackend{}
	isolated := &selectorTestBackend{}
	selector, err := NewBackendSelector(BackendSelectorOptions{
		Official:         official,
		OfficialEvidence: validOfficialSelectionEvidence(),
		Isolated:         isolated,
	})
	if err != nil {
		t.Fatal(err)
	}
	observation, failure := selector.Execute(
		context.Background(),
		browserprotocol.Identity{},
		browserprotocol.Action{
			Kind:        browserprotocol.ActionPreflight,
			BackendMode: "auto",
		},
	)
	if failure != nil {
		t.Fatal(failure)
	}
	if observation.BackendSelection == nil ||
		observation.BackendSelection.SelectedBackend != BackendOfficialChrome ||
		observation.BackendSelection.RequestedMode != "auto" ||
		len(official.calls) != 1 || len(isolated.calls) != 0 ||
		official.calls[0].BackendMode != "" {
		t.Fatalf("selection = %#v, official=%#v isolated=%#v", observation.BackendSelection, official.calls, isolated.calls)
	}
	if _, failure := selector.Execute(
		context.Background(),
		browserprotocol.Identity{},
		browserprotocol.Action{Kind: browserprotocol.ActionWait, DurationMS: selectorIntPointer(1)},
	); failure != nil || len(official.calls) != 2 {
		t.Fatalf("locked official execution failed: %v", failure)
	}
	if _, failure := selector.Execute(
		context.Background(),
		browserprotocol.Identity{},
		browserprotocol.Action{Kind: browserprotocol.ActionPreflight, BackendMode: "isolated"},
	); failure == nil || failure.Code != browserprotocol.ErrorProtocolInvalid {
		t.Fatalf("backend reselection failure = %#v", failure)
	}
}

func TestBackendSelectorOpsObserverIsRunBoundAndUsesPublishedSelection(t *testing.T) {
	official := &selectorTestBackend{}
	selector, err := NewBackendSelector(BackendSelectorOptions{
		Official: official, OfficialEvidence: validOfficialSelectionEvidence(),
		Isolated: &selectorTestBackend{},
	})
	if err != nil {
		t.Fatal(err)
	}
	identity := browserprotocol.Identity{
		RunID: "11111111-1111-4111-8111-111111111111",
	}
	if _, failure := selector.Execute(
		context.Background(), identity,
		browserprotocol.Action{Kind: browserprotocol.ActionPreflight, BackendMode: ModeOpenLinkerNativeChrome},
	); failure != nil {
		t.Fatal(failure)
	}
	selector.mu.Lock()
	started := time.Now()
	observation, busy, observerErr := selector.ObserveOps(
		context.Background(), identity.RunID, browserprotocol.OpsObserverStatusOperation,
	)
	selector.mu.Unlock()
	if observerErr != nil || busy || official.opsCalls != 1 ||
		observation.SelectedBackend != BackendOfficialChrome || observation.ProfileGeneration != 7 ||
		time.Since(started) > 100*time.Millisecond {
		t.Fatalf("published action observation = %#v, busy=%v error=%v calls=%d", observation, busy, observerErr, official.opsCalls)
	}
	if _, busy, observerErr = selector.ObserveOps(
		context.Background(), "22222222-2222-4222-8222-222222222222",
		browserprotocol.OpsObserverStatusOperation,
	); observerErr == nil || observerErr.Code != browserprotocol.OpsObserverRunNotActive || busy || official.opsCalls != 1 {
		t.Fatalf("wrong Run observation busy=%v error=%v calls=%d", busy, observerErr, official.opsCalls)
	}
	observation, busy, observerErr = selector.ObserveOps(
		context.Background(), identity.RunID, browserprotocol.OpsObserverStatusOperation,
	)
	if observerErr != nil || busy || official.opsCalls != 2 ||
		observation.SelectedBackend != BackendOfficialChrome || observation.ProfileGeneration != 7 {
		t.Fatalf("Run observation = %#v, busy=%v error=%v calls=%d", observation, busy, observerErr, official.opsCalls)
	}
}

func TestBackendSelectorKeepsSelectionAcrossCloseAndContinuation(t *testing.T) {
	official := &selectorTestBackend{}
	isolated := &selectorTestBackend{}
	selector, err := NewBackendSelector(BackendSelectorOptions{
		Official:         official,
		OfficialEvidence: validOfficialSelectionEvidence(),
		Isolated:         isolated,
	})
	if err != nil {
		t.Fatal(err)
	}
	identity := browserprotocol.Identity{
		BrowserSessionID: "session",
		SessionEpoch:     1,
		AttachmentID:     "first",
	}
	if _, failure := selector.Execute(
		context.Background(),
		identity,
		browserprotocol.Action{Kind: browserprotocol.ActionPreflight, BackendMode: "auto"},
	); failure != nil {
		t.Fatal(failure)
	}
	if _, failure := selector.Execute(
		context.Background(),
		identity,
		browserprotocol.Action{Kind: browserprotocol.ActionClose},
	); failure != nil {
		t.Fatal(failure)
	}
	continued := identity
	continued.AttachmentID = "second"
	observation, failure := selector.Execute(
		context.Background(),
		continued,
		browserprotocol.Action{Kind: browserprotocol.ActionWait, DurationMS: selectorIntPointer(1)},
	)
	if failure != nil {
		t.Fatal(failure)
	}
	if observation.BackendSelection != nil ||
		len(official.calls) != 3 || len(isolated.calls) != 0 {
		t.Fatalf(
			"continuation = %#v, official=%d isolated=%d",
			observation.BackendSelection,
			len(official.calls),
			len(isolated.calls),
		)
	}
}

func TestBackendSelectorAcceptsEquivalentModeOnSameGenerationNewAttachment(
	t *testing.T,
) {
	for _, continuationMode := range []string{
		ModeOpenLinkerNativeChrome,
		ModeOfficialChromeAlias,
	} {
		t.Run(continuationMode, func(t *testing.T) {
			official := &selectorTestBackend{}
			selector, err := NewBackendSelector(BackendSelectorOptions{
				Official:         official,
				OfficialEvidence: validOfficialSelectionEvidence(),
				Isolated:         &selectorTestBackend{},
			})
			if err != nil {
				t.Fatal(err)
			}
			first := browserprotocol.Identity{
				AgentID:          "agent",
				PrincipalScopeID: "principal",
				BrowserSessionID: "session",
				SessionEpoch:     1,
				AttachmentID:     "first",
			}
			if _, failure := selector.Execute(
				context.Background(),
				first,
				browserprotocol.Action{
					Kind:        browserprotocol.ActionPreflight,
					BackendMode: ModeOpenLinkerNativeChrome,
				},
			); failure != nil {
				t.Fatal(failure)
			}
			if _, failure := selector.Execute(
				context.Background(),
				first,
				browserprotocol.Action{Kind: browserprotocol.ActionClose},
			); failure != nil {
				t.Fatal(failure)
			}
			continued := first
			continued.AttachmentID = "second"
			observation, failure := selector.Execute(
				context.Background(),
				continued,
				browserprotocol.Action{
					Kind:        browserprotocol.ActionPreflight,
					BackendMode: continuationMode,
				},
			)
			if failure != nil {
				t.Fatal(failure)
			}
			if observation.BackendSelection == nil ||
				observation.BackendSelection.RequestedMode != ModeOpenLinkerNativeChrome ||
				observation.BackendSelection.SelectedBackend != BackendOfficialChrome ||
				official.aborted || len(official.calls) != 3 ||
				official.calls[2].BackendMode != "" ||
				!selector.scope.matches(continued) {
				t.Fatalf(
					"continuation = %#v, aborted=%v calls=%#v scope=%#v",
					observation.BackendSelection,
					official.aborted,
					official.calls,
					selector.scope,
				)
			}
		})
	}
}

func TestBackendSelectorRejectsModeDriftOnSameGenerationNewAttachment(
	t *testing.T,
) {
	official := &selectorTestBackend{}
	selector, err := NewBackendSelector(BackendSelectorOptions{
		Official:         official,
		OfficialEvidence: validOfficialSelectionEvidence(),
		Isolated:         &selectorTestBackend{},
	})
	if err != nil {
		t.Fatal(err)
	}
	first := browserprotocol.Identity{
		AgentID:          "agent",
		PrincipalScopeID: "principal",
		BrowserSessionID: "session",
		SessionEpoch:     1,
		AttachmentID:     "first",
	}
	if _, failure := selector.Execute(
		context.Background(),
		first,
		browserprotocol.Action{
			Kind:        browserprotocol.ActionPreflight,
			BackendMode: ModeOpenLinkerNativeChrome,
		},
	); failure != nil {
		t.Fatal(failure)
	}
	drifted := first
	drifted.AttachmentID = "second"
	if _, failure := selector.Execute(
		context.Background(),
		drifted,
		browserprotocol.Action{
			Kind:        browserprotocol.ActionPreflight,
			BackendMode: "isolated",
		},
	); failure == nil || failure.Code != browserprotocol.ErrorProtocolInvalid ||
		len(official.calls) != 1 || !selector.scope.matches(first) {
		t.Fatalf(
			"mode drift failure=%#v calls=%#v scope=%#v",
			failure,
			official.calls,
			selector.scope,
		)
	}
}

func TestBackendSelectorClearsSelectionWhenEquivalentModeContinuationFails(
	t *testing.T,
) {
	official := &selectorTestBackend{}
	selector, err := NewBackendSelector(BackendSelectorOptions{
		Official:         official,
		OfficialEvidence: validOfficialSelectionEvidence(),
		Isolated:         &selectorTestBackend{},
	})
	if err != nil {
		t.Fatal(err)
	}
	first := browserprotocol.Identity{
		AgentID:          "agent",
		PrincipalScopeID: "principal",
		BrowserSessionID: "session",
		SessionEpoch:     1,
		AttachmentID:     "first",
	}
	if _, failure := selector.Execute(
		context.Background(),
		first,
		browserprotocol.Action{
			Kind:        browserprotocol.ActionPreflight,
			BackendMode: ModeOpenLinkerNativeChrome,
		},
	); failure != nil {
		t.Fatal(failure)
	}
	official.failure = browserprotocol.NewFailure(
		browserprotocol.ErrorEngineUnavailable,
		"continued attachment failed",
		false,
	)
	continued := first
	continued.AttachmentID = "second"
	if _, failure := selector.Execute(
		context.Background(),
		continued,
		browserprotocol.Action{
			Kind:        browserprotocol.ActionPreflight,
			BackendMode: ModeOpenLinkerNativeChrome,
		},
	); failure == nil || failure.Code != browserprotocol.ErrorEngineUnavailable ||
		!official.aborted || selector.selected != nil ||
		selector.evidence != (browserprotocol.BackendSelectionEvidence{}) ||
		selector.scope != (backendSelectionScope{}) {
		t.Fatalf(
			"continuation failure=%#v aborted=%v selected=%#v evidence=%#v scope=%#v",
			failure,
			official.aborted,
			selector.selected,
			selector.evidence,
			selector.scope,
		)
	}
}

func TestBackendSelectorDiscardsAStaleGenerationBeforeNewPreflight(t *testing.T) {
	official := &selectorTestBackend{}
	isolated := &selectorTestBackend{}
	selector, err := NewBackendSelector(BackendSelectorOptions{
		Official:         official,
		OfficialEvidence: validOfficialSelectionEvidence(),
		Isolated:         isolated,
	})
	if err != nil {
		t.Fatal(err)
	}
	first := browserprotocol.Identity{AttachmentID: "first"}
	if _, failure := selector.Execute(
		context.Background(),
		first,
		browserprotocol.Action{Kind: browserprotocol.ActionPreflight, BackendMode: "auto"},
	); failure != nil {
		t.Fatal(failure)
	}
	second := first
	second.AttachmentID = "second"
	second.SessionEpoch = 1
	observation, failure := selector.Execute(
		context.Background(),
		second,
		browserprotocol.Action{Kind: browserprotocol.ActionPreflight, BackendMode: "isolated"},
	)
	if failure != nil {
		t.Fatal(failure)
	}
	if !official.aborted || observation.BackendSelection == nil ||
		observation.BackendSelection.SelectedBackend != BackendIsolated {
		t.Fatalf(
			"recovered selection = %#v, official aborted=%v",
			observation.BackendSelection,
			official.aborted,
		)
	}
}

func TestBackendSelectorKeepsLockedBackendForEmptyModeAcrossNewGeneration(
	t *testing.T,
) {
	official := &selectorTestBackend{}
	selector, err := NewBackendSelector(BackendSelectorOptions{
		Official:         official,
		OfficialEvidence: validOfficialSelectionEvidence(),
		Isolated:         &selectorTestBackend{},
	})
	if err != nil {
		t.Fatal(err)
	}
	first := browserprotocol.Identity{AttachmentID: "first", SessionEpoch: 1}
	if _, failure := selector.Execute(
		context.Background(),
		first,
		browserprotocol.Action{Kind: browserprotocol.ActionPreflight, BackendMode: "auto"},
	); failure != nil {
		t.Fatal(failure)
	}
	second := first
	second.AttachmentID = "second"
	second.SessionEpoch++
	observation, failure := selector.Execute(
		context.Background(),
		second,
		browserprotocol.Action{Kind: browserprotocol.ActionPreflight},
	)
	if failure != nil {
		t.Fatal(failure)
	}
	if observation.BackendSelection == nil ||
		observation.BackendSelection.SelectedBackend != BackendOfficialChrome ||
		official.aborted || len(official.calls) != 2 ||
		!selector.scope.matches(second) {
		t.Fatalf(
			"rotated selection = %#v, aborted=%v calls=%d",
			observation.BackendSelection,
			official.aborted,
			len(official.calls),
		)
	}
}

func TestBackendSelectorAutoFallsBackBeforeSelectionOnly(t *testing.T) {
	official := &selectorTestBackend{
		failure: browserprotocol.NewFailure(
			browserprotocol.ErrorEngineUnavailable,
			"extension did not load",
			false,
		),
		fallbackReason: "official_extension_unavailable",
	}
	isolated := &selectorTestBackend{}
	selector, err := NewBackendSelector(BackendSelectorOptions{
		Official:         official,
		OfficialEvidence: validOfficialSelectionEvidence(),
		Isolated:         isolated,
	})
	if err != nil {
		t.Fatal(err)
	}
	observation, failure := selector.Execute(
		context.Background(),
		browserprotocol.Identity{},
		browserprotocol.Action{Kind: browserprotocol.ActionPreflight, BackendMode: "auto"},
	)
	if failure != nil {
		t.Fatal(failure)
	}
	if observation.BackendSelection == nil ||
		observation.BackendSelection.SelectedBackend != BackendIsolated ||
		observation.BackendSelection.FallbackReason != "official_extension_unavailable" ||
		len(official.calls) != 1 || len(isolated.calls) != 1 {
		t.Fatalf("fallback selection = %#v", observation.BackendSelection)
	}
	if !official.aborted || official.closed {
		t.Fatal("failed official startup was not discarded before fallback")
	}
	if _, failure := selector.Execute(
		context.Background(),
		browserprotocol.Identity{},
		browserprotocol.Action{Kind: browserprotocol.ActionNavigate, URL: "https://example.com"},
	); failure != nil || len(official.calls) != 1 || len(isolated.calls) != 2 {
		t.Fatalf("post-selection execution switched backend: %v", failure)
	}
}

func TestBackendSelectorStrictOfficialReturnsCompleteSelectionEvidence(t *testing.T) {
	official := &selectorTestBackend{}
	selector, err := NewBackendSelector(BackendSelectorOptions{
		Official:         official,
		OfficialEvidence: validOfficialSelectionEvidence(),
		Isolated:         &selectorTestBackend{},
	})
	if err != nil {
		t.Fatal(err)
	}
	observation, failure := selector.Execute(
		context.Background(),
		browserprotocol.Identity{},
		browserprotocol.Action{
			Kind:        browserprotocol.ActionPreflight,
			BackendMode: "official-chrome",
		},
	)
	if failure != nil {
		t.Fatal(failure)
	}
	if observation.BackendSelection == nil ||
		observation.BackendSelection.RequestedMode != ModeOpenLinkerNativeChrome ||
		observation.BackendSelection.SelectedBackend != BackendOfficialChrome ||
		observation.BackendSelection.Validate() != nil {
		t.Fatalf("strict official evidence = %#v", observation.BackendSelection)
	}
}

func TestBackendSelectorStrictOfficialNeverFallsBack(t *testing.T) {
	isolated := &selectorTestBackend{}
	selector, err := NewBackendSelector(BackendSelectorOptions{Isolated: isolated})
	if err != nil {
		t.Fatal(err)
	}
	if _, failure := selector.Execute(
		context.Background(),
		browserprotocol.Identity{},
		browserprotocol.Action{
			Kind:        browserprotocol.ActionPreflight,
			BackendMode: "official-chrome",
		},
	); failure == nil || failure.Code != browserprotocol.ErrorEngineUnavailable ||
		len(isolated.calls) != 0 {
		t.Fatalf("strict official failure = %#v, isolated calls=%d", failure, len(isolated.calls))
	}
}

func TestBackendSelectorStrictOfficialDiscardsFailedStartup(t *testing.T) {
	official := &selectorTestBackend{
		failure: browserprotocol.NewFailure(
			browserprotocol.ErrorEngineUnavailable,
			"Chrome failed to start",
			false,
		),
	}
	isolated := &selectorTestBackend{}
	selector, err := NewBackendSelector(BackendSelectorOptions{
		Official:         official,
		OfficialEvidence: validOfficialSelectionEvidence(),
		Isolated:         isolated,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, failure := selector.Execute(
		context.Background(),
		browserprotocol.Identity{},
		browserprotocol.Action{
			Kind:        browserprotocol.ActionPreflight,
			BackendMode: "official-chrome",
		},
	); failure == nil || !official.aborted || len(isolated.calls) != 0 {
		t.Fatalf(
			"strict failure = %#v, aborted=%v, isolated calls=%d",
			failure,
			official.aborted,
			len(isolated.calls),
		)
	}
}

func validOfficialSelectionEvidence() browserprotocol.BackendSelectionEvidence {
	return browserprotocol.BackendSelectionEvidence{
		AssetManifestSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ExtensionID:         "abcdefghijklmnopabcdefghijklmnop",
		ExtensionVersion:    "1.2.3.4",
		NativeHostProtocol:  "openlinker.native-chrome.v2",
		ProfileGeneration:   7,
	}
}

func selectorIntPointer(value int) *int { return &value }
