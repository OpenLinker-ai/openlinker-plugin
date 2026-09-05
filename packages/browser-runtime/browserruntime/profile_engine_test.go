//go:build !windows

package browserruntime

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserprofile"
	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserprotocol"
)

type fixtureProfileProcess struct {
	directory        string
	environment      browserprotocol.EnvironmentEvidence
	preflightFailure *browserprotocol.Failure
}

func (process *fixtureProfileProcess) Execute(
	_ context.Context,
	_ browserprotocol.Identity,
	action browserprotocol.Action,
) (browserprotocol.Observation, *browserprotocol.Failure) {
	if action.Kind == browserprotocol.ActionPreflight &&
		process.preflightFailure != nil {
		return browserprotocol.Observation{}, process.preflightFailure
	}
	marker := filepath.Join(process.directory, "Default", "Cookies.fixture")
	if action.Kind == browserprotocol.ActionNavigate {
		if err := os.MkdirAll(filepath.Dir(marker), 0o700); err != nil {
			return browserprotocol.Observation{}, profileRuntimeFailure(err)
		}
		if err := os.WriteFile(marker, []byte("persistent-login-state"), 0o600); err != nil {
			return browserprotocol.Observation{}, profileRuntimeFailure(err)
		}
	}
	observation := browserprotocol.Observation{PageStateID: "fixture-page-state"}
	if action.Kind == browserprotocol.ActionPreflight {
		environment := process.environment
		observation.Environment = &environment
	}
	return observation, nil
}

func (*fixtureProfileProcess) Close() error {
	return nil
}

func (*fixtureProfileProcess) ObserveActiveOps(
	_ context.Context,
	_ browserprotocol.Identity,
	operation browserprotocol.OpsObserverOperation,
) (opsPageObservation, bool, *browserprotocol.OpsObserverError) {
	page := opsPageObservation{PageURL: "about:blank", PageTitle: "Blank"}
	if operation == browserprotocol.OpsObserverFrameOperation {
		page.Frame = &browserprotocol.ViewerFrame{
			MIMEType: "image/jpeg",
			Data:     []byte{0xff, 0xd8, 0xff, 0xd9},
			Width:    browserprotocol.BrowserViewportWidth,
			Height:   browserprotocol.BrowserViewportHeight,
		}
	}
	return page, false, nil
}

func TestProfileEngineEncryptsCheckpointAndIsolatesPrincipal(t *testing.T) {
	t.Parallel()
	state := t.TempDir()
	work := t.TempDir()
	engine := newFixtureProfileEngine(t, state, work)
	identityA := profileEngineIdentity("principal-a")
	identityB := profileEngineIdentity("principal-b")
	loadedA := false
	engine.processFactory = fixtureProfileFactory(&loadedA)

	if _, failure := engine.Execute(
		context.Background(),
		identityA,
		browserprotocol.Action{Kind: browserprotocol.ActionNavigate, URL: "https://example.com"},
	); failure != nil {
		t.Fatal(failure)
	}
	if _, failure := engine.Execute(
		context.Background(),
		identityA,
		browserprotocol.Action{Kind: browserprotocol.ActionCheckpoint},
	); failure != nil {
		t.Fatal(failure)
	}
	if loadedA {
		t.Fatal("first Browser Profile process unexpectedly loaded an existing marker")
	}
	if raw, err := os.ReadFile(filepath.Join(work, "active", "Default", "Cookies.fixture")); err != nil ||
		string(raw) != "persistent-login-state" {
		t.Fatalf("active Browser Profile marker = %q, %v", raw, err)
	}
	assertPersistentProfileDoesNotContain(t, state, "persistent-login-state")

	loadedAfterCheckpoint := false
	engine.processFactory = fixtureProfileFactory(&loadedAfterCheckpoint)
	if _, failure := engine.Execute(
		context.Background(),
		identityA,
		browserprotocol.Action{Kind: browserprotocol.ActionScreenshot},
	); failure != nil {
		t.Fatal(failure)
	}
	if !loadedAfterCheckpoint {
		t.Fatal("same Browser Session did not retain its decrypted Profile after checkpoint")
	}
	if _, failure := engine.Execute(
		context.Background(),
		identityA,
		browserprotocol.Action{Kind: browserprotocol.ActionClose},
	); failure != nil {
		t.Fatal(failure)
	}

	loadedOtherPrincipal := false
	engine.processFactory = fixtureProfileFactory(&loadedOtherPrincipal)
	if _, failure := engine.Execute(
		context.Background(),
		identityB,
		browserprotocol.Action{Kind: browserprotocol.ActionScreenshot},
	); failure != nil {
		t.Fatal(failure)
	}
	if loadedOtherPrincipal {
		t.Fatal("different principal loaded another principal's Browser Profile")
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}

	restarted := newFixtureProfileEngine(t, state, t.TempDir())
	defer restarted.Close()
	loadedAfterRestart := false
	restarted.processFactory = fixtureProfileFactory(&loadedAfterRestart)
	if _, failure := restarted.Execute(
		context.Background(),
		identityA,
		browserprotocol.Action{Kind: browserprotocol.ActionScreenshot},
	); failure != nil {
		t.Fatal(failure)
	}
	if !loadedAfterRestart {
		t.Fatal("encrypted Browser Profile did not survive a Runtime restart")
	}
}

func TestProfileEnginePreflightPersistsAndReopensNewProfileBeforeReady(
	t *testing.T,
) {
	t.Parallel()
	engine := newFixtureProfileEngine(t, t.TempDir(), t.TempDir())
	defer engine.Close()
	starts := 0
	engine.processFactory = func(options ProcessEngineOptions) (
		managedBrowserEngine,
		error,
	) {
		starts++
		return fixtureProfileFactory(new(bool))(options)
	}
	identity := profileEngineIdentity("principal-preflight")
	action := browserprotocol.Action{Kind: browserprotocol.ActionPreflight}
	if _, failure := engine.Execute(
		context.Background(),
		identity,
		action,
	); failure != nil {
		t.Fatal(failure)
	}
	if starts != 2 {
		t.Fatalf("new Profile process starts = %d, want persist and reopen", starts)
	}
	if engine.active == nil || !engine.active.exists || engine.active.process == nil {
		t.Fatalf("Profile was not durable and live after preflight: %#v", engine.active)
	}
	if _, failure := engine.Execute(
		context.Background(),
		identity,
		action,
	); failure != nil {
		t.Fatal(failure)
	}
	if starts != 2 {
		t.Fatalf("existing Profile was unnecessarily restarted: %d", starts)
	}
}

func TestProfileEngineOpsObservationNeverActivatesAndPreservesAuthority(t *testing.T) {
	t.Parallel()
	engine := newFixtureProfileEngine(t, t.TempDir(), t.TempDir())
	defer engine.Close()
	starts := 0
	engine.processFactory = func(options ProcessEngineOptions) (managedBrowserEngine, error) {
		starts++
		return fixtureProfileFactory(new(bool))(options)
	}
	identity := profileEngineIdentity("ops-observer-principal")
	if _, busy, observerErr := engine.ObserveOps(
		context.Background(), identity.RunID, browserprotocol.OpsObserverStatusOperation,
	); observerErr == nil || observerErr.Code != browserprotocol.OpsObserverRunNotActive || busy || starts != 0 {
		t.Fatalf("inactive observation busy=%v error=%v process starts=%d", busy, observerErr, starts)
	}
	if _, failure := engine.Execute(
		context.Background(), identity,
		browserprotocol.Action{Kind: browserprotocol.ActionPreflight},
	); failure != nil {
		t.Fatal(failure)
	}
	beforeOwner := engine.active.owner
	beforeIdentity := engine.active.identity
	engine.mu.Lock()
	observation, busy, observerErr := engine.ObserveOps(
		context.Background(), identity.RunID, browserprotocol.OpsObserverStatusOperation,
	)
	engine.mu.Unlock()
	if observerErr != nil || busy || observation.RunID != identity.RunID ||
		observation.PageURL != "about:blank" || observation.ProfileGeneration != 1 {
		t.Fatalf("active observation = %#v, busy=%v error=%v", observation, busy, observerErr)
	}
	if !reflect.DeepEqual(engine.active.owner, beforeOwner) ||
		engine.active.identity != beforeIdentity || starts != 2 {
		t.Fatalf("observation changed Profile authority or process count: owner=%#v starts=%d", engine.active.owner, starts)
	}
	if _, busy, observerErr = engine.ObserveOps(
		context.Background(), "55555555-5555-4555-8555-555555555555",
		browserprotocol.OpsObserverStatusOperation,
	); observerErr == nil || observerErr.Code != browserprotocol.OpsObserverRunNotActive || busy {
		t.Fatalf("wrong Run observation busy=%v error=%v", busy, observerErr)
	}
}

func TestProfileSelectionEvidenceComesFromCheckpointRestore(t *testing.T) {
	t.Parallel()
	state := t.TempDir()
	identity := profileEngineIdentity("principal-recovery-evidence")
	action := browserprotocol.Action{Kind: browserprotocol.ActionPreflight}

	first := newFixtureProfileEngine(t, state, t.TempDir())
	first.processFactory = fixtureProfileFactory(new(bool))
	if _, failure := first.Execute(context.Background(), identity, action); failure != nil {
		t.Fatal(failure)
	}
	generation, recovered, ok := first.ProfileSelectionEvidence()
	if !ok || generation != first.options.Environment.ProfileGeneration || recovered {
		t.Fatalf("new Profile evidence = generation %d recovered %v ok %v", generation, recovered, ok)
	}
	if _, failure := first.Execute(context.Background(), identity, browserprotocol.Action{
		Kind: browserprotocol.ActionNavigate,
		URL:  "https://example.com",
	}); failure != nil {
		t.Fatal(failure)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	restarted := newFixtureProfileEngine(t, state, t.TempDir())
	defer restarted.Close()
	loaded := false
	restarted.processFactory = fixtureProfileFactory(&loaded)
	identity.SessionEpoch++
	identity.AttachmentID = "55555555-5555-4555-8555-555555555555"
	if _, failure := restarted.Execute(context.Background(), identity, action); failure != nil {
		t.Fatal(failure)
	}
	generation, recovered, ok = restarted.ProfileSelectionEvidence()
	if !ok || generation != restarted.options.Environment.ProfileGeneration || !recovered || !loaded {
		t.Fatalf("restored Profile evidence = generation %d recovered %v ok %v loaded %v", generation, recovered, ok, loaded)
	}
}

func TestProfileEngineStartupAbortDiscardsOnlyUncommittedActiveState(
	t *testing.T,
) {
	t.Parallel()
	engine := newFixtureProfileEngine(t, t.TempDir(), t.TempDir())
	defer engine.Close()
	identity := profileEngineIdentity("principal-abort")
	engine.processFactory = fixtureProfileFactory(new(bool))
	if _, failure := engine.Execute(
		context.Background(),
		identity,
		browserprotocol.Action{
			Kind: browserprotocol.ActionNavigate,
			URL:  "https://example.com",
		},
	); failure != nil {
		t.Fatal(failure)
	}
	if err := engine.AbortStartup(); err != nil {
		t.Fatal(err)
	}
	if engine.active != nil {
		t.Fatalf("aborted Profile remained active: %#v", engine.active)
	}
	loaded := false
	engine.processFactory = fixtureProfileFactory(&loaded)
	if _, failure := engine.Execute(
		context.Background(),
		identity,
		browserprotocol.Action{Kind: browserprotocol.ActionScreenshot},
	); failure != nil {
		t.Fatal(failure)
	}
	if loaded {
		t.Fatal("startup abort checkpointed uncommitted Browser state")
	}
}

func TestProfileEngineUpgradeIsTransactionalAndDowngradeIsRejectedBeforeLaunch(
	t *testing.T,
) {
	t.Parallel()
	state := t.TempDir()
	baseEnvironment := fixtureProfileEnvironment()
	base := newFixtureProfileEngineWithEnvironment(
		t,
		state,
		t.TempDir(),
		baseEnvironment,
	)
	base.processFactory = fixtureProfileFactory(new(bool))
	identity := profileEngineIdentity("principal-upgrade")
	if _, failure := base.Execute(
		context.Background(),
		identity,
		browserprotocol.Action{
			Kind: browserprotocol.ActionNavigate,
			URL:  "https://example.com",
		},
	); failure != nil {
		t.Fatal(failure)
	}
	if _, failure := base.Execute(
		context.Background(),
		identity,
		browserprotocol.Action{Kind: browserprotocol.ActionClose},
	); failure != nil {
		t.Fatal(failure)
	}
	if err := base.Close(); err != nil {
		t.Fatal(err)
	}

	upgradeEnvironment := baseEnvironment
	upgradeEnvironment.Evidence.BrowserVersion = "150.0.1.0"
	upgradeEnvironment.Evidence.BrowserMajorVersion = 150
	failedUpgrade := newFixtureProfileEngineWithEnvironment(
		t,
		state,
		t.TempDir(),
		upgradeEnvironment,
	)
	failedUpgrade.processFactory = func(options ProcessEngineOptions) (
		managedBrowserEngine,
		error,
	) {
		process, err := fixtureProfileFactory(new(bool))(options)
		fixture := process.(*fixtureProfileProcess)
		fixture.environment = upgradeEnvironment.Evidence
		fixture.preflightFailure = browserprotocol.NewFailure(
			browserprotocol.ErrorRuntimeUnavailable,
			"fixture upgraded Browser failed",
			true,
		)
		return fixture, err
	}
	if _, failure := failedUpgrade.Execute(
		context.Background(),
		identity,
		browserprotocol.Action{Kind: browserprotocol.ActionPreflight},
	); failure == nil || failure.Code != browserprotocol.ErrorProfileEngineUpgrade {
		t.Fatalf("failed upgrade = %#v", failure)
	}
	if failedUpgrade.active != nil {
		t.Fatalf("failed upgrade retained working Profile: %#v", failedUpgrade.active)
	}
	if err := failedUpgrade.Close(); err != nil {
		t.Fatal(err)
	}

	rollback := newFixtureProfileEngineWithEnvironment(
		t,
		state,
		t.TempDir(),
		baseEnvironment,
	)
	loadedAfterFailedUpgrade := false
	rollback.processFactory = fixtureProfileFactory(&loadedAfterFailedUpgrade)
	if _, failure := rollback.Execute(
		context.Background(),
		identity,
		browserprotocol.Action{Kind: browserprotocol.ActionScreenshot},
	); failure != nil {
		t.Fatal(failure)
	}
	if !loadedAfterFailedUpgrade {
		t.Fatal("failed Browser upgrade did not preserve the prior checkpoint")
	}
	if err := rollback.Close(); err != nil {
		t.Fatal(err)
	}

	successfulUpgrade := newFixtureProfileEngineWithEnvironment(
		t,
		state,
		t.TempDir(),
		upgradeEnvironment,
	)
	successfulUpgrade.processFactory = func(options ProcessEngineOptions) (
		managedBrowserEngine,
		error,
	) {
		process, err := fixtureProfileFactory(new(bool))(options)
		process.(*fixtureProfileProcess).environment = upgradeEnvironment.Evidence
		return process, err
	}
	if _, failure := successfulUpgrade.Execute(
		context.Background(),
		identity,
		browserprotocol.Action{Kind: browserprotocol.ActionPreflight},
	); failure != nil {
		t.Fatal(failure)
	}
	if err := successfulUpgrade.Close(); err != nil {
		t.Fatal(err)
	}

	downgrade := newFixtureProfileEngineWithEnvironment(
		t,
		state,
		t.TempDir(),
		baseEnvironment,
	)
	processStarts := 0
	downgrade.processFactory = func(options ProcessEngineOptions) (
		managedBrowserEngine,
		error,
	) {
		processStarts++
		return fixtureProfileFactory(new(bool))(options)
	}
	if _, failure := downgrade.Execute(
		context.Background(),
		identity,
		browserprotocol.Action{Kind: browserprotocol.ActionPreflight},
	); failure == nil || failure.Code != browserprotocol.ErrorProfileEngineDowngrade {
		t.Fatalf("downgrade failure = %#v", failure)
	}
	if processStarts != 0 {
		t.Fatalf("downgrade launched Browser %d times", processStarts)
	}
	if err := downgrade.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyProfileAdoptionRequiresLockedPreflight(t *testing.T) {
	t.Parallel()
	state := t.TempDir()
	engine := newFixtureProfileEngine(t, state, t.TempDir())
	identity := profileEngineIdentity("principal-legacy")
	seedLegacyProfile(t, engine, identity)
	loaded := false
	engine.processFactory = fixtureProfileFactory(&loaded)

	if _, failure := engine.Execute(
		context.Background(),
		identity,
		browserprotocol.Action{Kind: browserprotocol.ActionScreenshot},
	); failure != nil {
		t.Fatal(failure)
	}
	if !loaded {
		t.Fatal("legacy Profile marker was not loaded")
	}
	if _, failure := engine.Execute(
		context.Background(),
		identity,
		browserprotocol.Action{Kind: browserprotocol.ActionCheckpoint},
	); failure == nil || failure.Code != browserprotocol.ErrorEngineUnavailable {
		t.Fatalf("legacy checkpoint before preflight = %#v", failure)
	}
	if engine.active != nil {
		t.Fatalf("unvalidated legacy working copy was retained: %#v", engine.active)
	}
	if _, failure := engine.Execute(
		context.Background(),
		identity,
		browserprotocol.Action{Kind: browserprotocol.ActionPreflight},
	); failure != nil {
		t.Fatal(failure)
	}
	if _, failure := engine.Execute(
		context.Background(),
		identity,
		browserprotocol.Action{Kind: browserprotocol.ActionCheckpoint},
	); failure != nil {
		t.Fatal(failure)
	}
	if _, err := loadProfileEnvironment(engine.workDirectory); err != nil {
		t.Fatalf("legacy Profile environment was not adopted: %v", err)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}

	restarted := newFixtureProfileEngine(t, state, t.TempDir())
	restarted.processFactory = fixtureProfileFactory(new(bool))
	if _, failure := restarted.Execute(
		context.Background(),
		identity,
		browserprotocol.Action{Kind: browserprotocol.ActionScreenshot},
	); failure != nil {
		t.Fatalf("adopted Profile did not reopen: %v", failure)
	}
	if err := restarted.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestProfileEngineReusesSessionButRefreshesAttachmentOwnership(t *testing.T) {
	t.Parallel()
	engine := newFixtureProfileEngine(t, t.TempDir(), t.TempDir())
	defer engine.Close()
	starts := 0
	engine.processFactory = func(options ProcessEngineOptions) (
		managedBrowserEngine,
		error,
	) {
		starts++
		return fixtureProfileFactory(new(bool))(options)
	}
	first := profileEngineIdentity("principal-reuse")
	if _, failure := engine.Execute(
		context.Background(),
		first,
		browserprotocol.Action{Kind: browserprotocol.ActionScreenshot},
	); failure != nil {
		t.Fatal(failure)
	}
	second := first
	second.RunID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	second.AttachmentID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	second.ControlEpoch++
	if _, failure := engine.Execute(
		context.Background(),
		second,
		browserprotocol.Action{Kind: browserprotocol.ActionScreenshot},
	); failure != nil {
		t.Fatal(failure)
	}
	if starts != 1 {
		t.Fatalf("same Session restarted Chromium %d times", starts)
	}
	if engine.active == nil || !sameActiveAttachment(engine.active.owner, second) {
		t.Fatalf("active ownership was not refreshed: %#v", engine.active)
	}
	activeProcess := engine.active.process
	if _, failure := engine.Execute(
		context.Background(),
		first,
		browserprotocol.Action{Kind: browserprotocol.ActionClose},
	); failure != nil {
		t.Fatal(failure)
	}
	if engine.active == nil || engine.active.process != activeProcess {
		t.Fatal("late old attachment close touched the current active Profile")
	}
	if _, failure := engine.Execute(
		context.Background(),
		second,
		browserprotocol.Action{Kind: browserprotocol.ActionClose},
	); failure != nil {
		t.Fatal(failure)
	}
	if engine.active != nil {
		t.Fatalf("current attachment close did not checkpoint Profile: %#v", engine.active)
	}
	if _, failure := engine.Execute(
		context.Background(),
		second,
		browserprotocol.Action{Kind: browserprotocol.ActionClose},
	); failure != nil {
		t.Fatal(failure)
	}
	if starts != 1 || engine.active != nil {
		t.Fatalf(
			"already closed attachment reloaded Profile: starts=%d active=%#v",
			starts,
			engine.active,
		)
	}
}

func TestProfileEngineQuarantinesCorruptEncryptedProfile(t *testing.T) {
	t.Parallel()
	state := t.TempDir()
	t.Cleanup(func() {
		_ = filepath.WalkDir(state, func(path string, entry os.DirEntry, _ error) error {
			if entry == nil {
				return nil
			}
			if entry.IsDir() {
				_ = os.Chmod(path, 0o700)
			} else {
				_ = os.Chmod(path, 0o600)
			}
			return nil
		})
	})
	engine := newFixtureProfileEngine(t, state, t.TempDir())
	loaded := false
	engine.processFactory = fixtureProfileFactory(&loaded)
	identity := profileEngineIdentity("principal-corrupt")
	if _, failure := engine.Execute(
		context.Background(),
		identity,
		browserprotocol.Action{Kind: browserprotocol.ActionNavigate, URL: "https://example.com"},
	); failure != nil {
		t.Fatal(failure)
	}
	if _, failure := engine.Execute(
		context.Background(),
		identity,
		browserprotocol.Action{Kind: browserprotocol.ActionClose},
	); failure != nil {
		t.Fatal(failure)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	payloads, err := filepath.Glob(filepath.Join(state, "encrypted", "profiles", "*", "checkpoints", "*", "payload.bin"))
	if err != nil || len(payloads) != 1 {
		t.Fatalf("encrypted payloads = %#v, %v", payloads, err)
	}
	raw, err := os.ReadFile(payloads[0])
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)-1] ^= 0xff
	if err := os.WriteFile(payloads[0], raw, 0o600); err != nil {
		t.Fatal(err)
	}

	restarted := newFixtureProfileEngine(t, state, t.TempDir())
	defer restarted.Close()
	restarted.processFactory = fixtureProfileFactory(new(bool))
	if _, failure := restarted.Execute(
		context.Background(),
		identity,
		browserprotocol.Action{Kind: browserprotocol.ActionScreenshot},
	); failure == nil || failure.Code != browserprotocol.ErrorProfileCorrupt {
		t.Fatalf("corrupt Browser Profile failure = %#v", failure)
	}
	quarantine, err := os.ReadDir(filepath.Join(state, "encrypted", "quarantine"))
	if err != nil || len(quarantine) != 1 {
		t.Fatalf("Browser Profile quarantine = %#v, %v", quarantine, err)
	}
}

func TestProfileEngineQuarantinesMalformedAuthenticatedPageContinuation(t *testing.T) {
	t.Parallel()
	state := t.TempDir()
	t.Cleanup(func() {
		_ = filepath.WalkDir(state, func(path string, entry os.DirEntry, _ error) error {
			if entry == nil {
				return nil
			}
			if entry.IsDir() {
				_ = os.Chmod(path, 0o700)
			} else {
				_ = os.Chmod(path, 0o600)
			}
			return nil
		})
	})
	work := t.TempDir()
	engine := newFixtureProfileEngine(t, state, work)
	engine.processFactory = fixtureProfileFactory(new(bool))
	identity := profileEngineIdentity("principal-page-state")
	if _, failure := engine.Execute(
		context.Background(),
		identity,
		browserprotocol.Action{Kind: browserprotocol.ActionNavigate, URL: "https://example.com"},
	); failure != nil {
		t.Fatal(failure)
	}
	if _, failure := engine.Execute(
		context.Background(),
		identity,
		browserprotocol.Action{Kind: browserprotocol.ActionCheckpoint},
	); failure != nil {
		t.Fatal(failure)
	}
	continuationDirectory := filepath.Join(work, "active", pageContinuationDirectory)
	if err := os.MkdirAll(continuationDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{
		"contract_id": pageContinuationContractID,
		"entries": []map[string]any{{
			"session_key": strings.Repeat("a", 64),
			"url":         "http://127.0.0.1/",
			"updated_at":  "2026-07-26T00:00:00.000Z",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(continuationDirectory, pageContinuationFile),
		raw,
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if _, failure := engine.Execute(
		context.Background(),
		identity,
		browserprotocol.Action{Kind: browserprotocol.ActionClose},
	); failure == nil || failure.Code != browserprotocol.ErrorProfileCorrupt {
		t.Fatalf("malformed page continuation failure = %#v", failure)
	}
	quarantine, err := os.ReadDir(filepath.Join(state, "encrypted", "quarantine"))
	if err != nil || len(quarantine) != 1 {
		t.Fatalf("Browser Profile quarantine = %#v, %v", quarantine, err)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestProfileEngineQuarantinesMalformedAuthenticatedOriginBudgetBeforeProcessStart(
	t *testing.T,
) {
	t.Parallel()
	state := t.TempDir()
	t.Cleanup(func() {
		_ = filepath.WalkDir(state, func(path string, entry os.DirEntry, _ error) error {
			if entry == nil {
				return nil
			}
			if entry.IsDir() {
				_ = os.Chmod(path, 0o700)
			} else {
				_ = os.Chmod(path, 0o600)
			}
			return nil
		})
	})
	engine := newFixtureProfileEngine(t, state, t.TempDir())
	identity := profileEngineIdentity("principal-origin-budget-load")
	seedProfileWithOriginBudget(
		t,
		engine,
		identity,
		`{"contract_id":"wrong","entries":[]}`,
	)
	processStarts := 0
	engine.processFactory = func(options ProcessEngineOptions) (
		managedBrowserEngine,
		error,
	) {
		processStarts++
		return fixtureProfileFactory(new(bool))(options)
	}

	if _, failure := engine.Execute(
		context.Background(),
		identity,
		browserprotocol.Action{Kind: browserprotocol.ActionScreenshot},
	); failure == nil || failure.Code != browserprotocol.ErrorProfileCorrupt {
		t.Fatalf("malformed origin-budget failure = %#v", failure)
	}
	if processStarts != 0 {
		t.Fatalf("malformed origin budget started Browser %d times", processStarts)
	}
	quarantine, err := os.ReadDir(filepath.Join(state, "encrypted", "quarantine"))
	if err != nil || len(quarantine) != 1 {
		t.Fatalf("Browser Profile quarantine = %#v, %v", quarantine, err)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestProfileEngineQuarantinesOriginBudgetCorruptedBeforeCheckpoint(
	t *testing.T,
) {
	t.Parallel()
	state := t.TempDir()
	t.Cleanup(func() {
		_ = filepath.WalkDir(state, func(path string, entry os.DirEntry, _ error) error {
			if entry == nil {
				return nil
			}
			if entry.IsDir() {
				_ = os.Chmod(path, 0o700)
			} else {
				_ = os.Chmod(path, 0o600)
			}
			return nil
		})
	})
	work := t.TempDir()
	engine := newFixtureProfileEngine(t, state, work)
	engine.processFactory = fixtureProfileFactory(new(bool))
	identity := profileEngineIdentity("principal-origin-budget-checkpoint")
	if _, failure := engine.Execute(
		context.Background(),
		identity,
		browserprotocol.Action{Kind: browserprotocol.ActionPreflight},
	); failure != nil {
		t.Fatal(failure)
	}
	writeOriginBudgetFixture(
		t,
		filepath.Join(work, "active"),
		`{"contract_id":"wrong","entries":[]}`,
		0o600,
	)
	if _, failure := engine.Execute(
		context.Background(),
		identity,
		browserprotocol.Action{Kind: browserprotocol.ActionClose},
	); failure == nil || failure.Code != browserprotocol.ErrorProfileCorrupt {
		t.Fatalf("checkpoint origin-budget failure = %#v", failure)
	}
	quarantine, err := os.ReadDir(filepath.Join(state, "encrypted", "quarantine"))
	if err != nil || len(quarantine) != 1 {
		t.Fatalf("Browser Profile quarantine = %#v, %v", quarantine, err)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestProfileArchiveRejectsTraversal(t *testing.T) {
	t.Parallel()
	var raw bytes.Buffer
	writer := tar.NewWriter(&raw)
	if err := writer.WriteHeader(&tar.Header{
		Name:     "../outside",
		Typeflag: tar.TypeReg,
		Mode:     0o600,
		Size:     1,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "profile")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := extractProfileArchive(bytes.NewReader(raw.Bytes()), root); err == nil {
		t.Fatal("traversing Browser Profile archive was accepted")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "outside")); !os.IsNotExist(err) {
		t.Fatalf("Browser Profile archive escaped its root: %v", err)
	}
}

func newFixtureProfileEngine(t *testing.T, state, work string) *ProfileEngine {
	return newFixtureProfileEngineWithEnvironment(
		t,
		state,
		work,
		fixtureProfileEnvironment(),
	)
}

func seedLegacyProfile(
	t *testing.T,
	engine *ProfileEngine,
	identity browserprotocol.Identity,
) {
	t.Helper()
	directory := t.TempDir()
	marker := filepath.Join(directory, "Default", "Cookies.fixture")
	if err := os.MkdirAll(filepath.Dir(marker), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, []byte("legacy-login-state"), 0o600); err != nil {
		t.Fatal(err)
	}
	archive, err := os.CreateTemp(t.TempDir(), "legacy-profile-")
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	if err := writeProfileArchive(directory, archive); err != nil {
		t.Fatal(err)
	}
	if _, err := archive.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	profileIdentity := browserprofile.Identity{
		AgentID:           identity.AgentID,
		PrincipalScopeID:  identity.PrincipalScopeID,
		ProfileSlot:       "default",
		ProfileGeneration: engine.options.Environment.ProfileGeneration,
	}
	if err := engine.store.Create(profileIdentity, engine.rootKey, archive); err != nil {
		t.Fatal(err)
	}
}

func seedProfileWithOriginBudget(
	t *testing.T,
	engine *ProfileEngine,
	identity browserprotocol.Identity,
	raw string,
) {
	t.Helper()
	directory := t.TempDir()
	marker := filepath.Join(directory, "Default", "Cookies.fixture")
	if err := os.MkdirAll(filepath.Dir(marker), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, []byte("authenticated-state"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeOriginBudgetFixture(t, directory, raw, 0o600)
	archive, err := os.CreateTemp(t.TempDir(), "origin-budget-profile-")
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	if err := writeProfileArchive(directory, archive); err != nil {
		t.Fatal(err)
	}
	if _, err := archive.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	profileIdentity := browserprofile.Identity{
		AgentID:           identity.AgentID,
		PrincipalScopeID:  identity.PrincipalScopeID,
		ProfileSlot:       "default",
		ProfileGeneration: engine.options.Environment.ProfileGeneration,
	}
	if err := engine.store.Create(profileIdentity, engine.rootKey, archive); err != nil {
		t.Fatal(err)
	}
}

func newFixtureProfileEngineWithEnvironment(
	t *testing.T,
	state string,
	work string,
	environment ProfileEnvironment,
) *ProfileEngine {
	t.Helper()
	engine, err := NewProfileEngine(ProfileEngineOptions{
		Process: ProcessEngineOptions{
			Command: []string{"/fixture/browser-engine"},
			Environment: []string{
				"NO_PROXY=",
				"OPENLINKER_BROWSER_EGRESS_PROXY=http://egress.test:3128",
				"OPENLINKER_BROWSER_PROFILE_DIR=" + filepath.Join(work, "active"),
			},
		},
		Environment: environment,
		StoreRoot:   filepath.Join(state, "encrypted"),
		WorkRoot:    work,
		RootKeyFile: filepath.Join(state, "profile-root-key"),
	})
	if err != nil {
		t.Fatal(err)
	}
	return engine
}

func fixtureProfileFactory(loaded *bool) func(ProcessEngineOptions) (managedBrowserEngine, error) {
	return func(options ProcessEngineOptions) (managedBrowserEngine, error) {
		directory := ""
		for _, value := range options.Environment {
			if strings.HasPrefix(value, "OPENLINKER_BROWSER_PROFILE_DIR=") {
				directory = strings.TrimPrefix(value, "OPENLINKER_BROWSER_PROFILE_DIR=")
			}
		}
		if _, err := os.Stat(filepath.Join(directory, "Default", "Cookies.fixture")); err == nil {
			*loaded = true
		}
		return &fixtureProfileProcess{
			directory:   directory,
			environment: fixtureProfileEnvironment().Evidence,
		}, nil
	}
}

func profileEngineIdentity(principal string) browserprotocol.Identity {
	return browserprotocol.Identity{
		RunID:                              "11111111-1111-4111-8111-111111111111",
		AgentID:                            "22222222-2222-4222-8222-222222222222",
		PrincipalScopeID:                   principal,
		BrowserSessionID:                   "33333333-3333-4333-8333-333333333333",
		SessionEpoch:                       1,
		AttachmentID:                       "44444444-4444-4444-8444-444444444444",
		ControlEpoch:                       1,
		Controller:                         browserprotocol.ControllerAgent,
		BrowserInteractionPolicy:           "restricted",
		BrowserInteractionPolicyGeneration: 1,
		BrowserMutationOrigins:             []string{},
		BrowserMutationOriginsSHA256:       browserprotocol.RestrictedMutationOriginsSHA256,
	}
}

func assertPersistentProfileDoesNotContain(t *testing.T, root, value string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(raw, []byte(value)) {
			t.Fatalf("persistent Browser Profile file %s contains plaintext state", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// samplingObserverStub drives ObserveOps through the exact interleavings the
// action_in_flight evidence must distinguish. The capture callback runs between
// the two Engine samples, mirroring a real frame capture.
type samplingObserverStub struct {
	samples   [][2]any
	next      int
	reportsIn bool
	onCapture func()
}

func (stub *samplingObserverStub) ObserveActiveOps(
	context.Context,
	browserprotocol.Identity,
	browserprotocol.OpsObserverOperation,
) (opsPageObservation, bool, *browserprotocol.OpsObserverError) {
	if stub.onCapture != nil {
		stub.onCapture()
	}
	return opsPageObservation{PageURL: "https://example.com/", PageTitle: "t"}, false, nil
}

func (stub *samplingObserverStub) ActionInFlightSample() (uint64, bool) {
	sample := stub.samples[stub.next]
	if stub.next < len(stub.samples)-1 {
		stub.next++
	}
	return uint64(sample[0].(int)), sample[1].(bool)
}

type plainObserverStub struct{}

func (plainObserverStub) ObserveActiveOps(
	context.Context,
	browserprotocol.Identity,
	browserprotocol.OpsObserverOperation,
) (opsPageObservation, bool, *browserprotocol.OpsObserverError) {
	return opsPageObservation{PageURL: "https://example.com/", PageTitle: "t"}, false, nil
}

func TestProfileEngineObserveOpsReportsActionContinuity(t *testing.T) {
	t.Parallel()
	identity := validRuntimeRequest().Identity
	identity.Controller = browserprotocol.ControllerAgent

	for _, testCase := range []struct {
		name     string
		observer activeOpsObserverEngine
		want     *bool
	}{
		{
			name: "one action spans the capture",
			observer: &samplingObserverStub{
				samples: [][2]any{{7, true}, {7, true}},
			},
			want: processBoolPointer(true),
		},
		{
			name: "a different action started mid capture",
			observer: &samplingObserverStub{
				samples: [][2]any{{7, true}, {8, true}},
			},
			want: processBoolPointer(false),
		},
		{
			name: "engine was idle for the capture",
			observer: &samplingObserverStub{
				samples: [][2]any{{7, false}, {7, false}},
			},
			want: processBoolPointer(false),
		},
		{
			name:     "engine cannot report the evidence",
			observer: plainObserverStub{},
			want:     nil,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			engine := &ProfileEngine{}
			engine.ops.Store(&profileOpsSnapshot{
				identity:          identity,
				profileGeneration: 3,
				observer:          testCase.observer,
			})
			observation, busy, observerErr := engine.ObserveOps(
				context.Background(),
				identity.RunID,
				browserprotocol.OpsObserverStatusOperation,
			)
			if busy || observerErr != nil {
				t.Fatalf("busy=%v err=%v", busy, observerErr)
			}
			switch {
			case testCase.want == nil && observation.ActionInFlight != nil:
				t.Fatalf("expected no evidence, got %v", *observation.ActionInFlight)
			case testCase.want != nil && observation.ActionInFlight == nil:
				t.Fatal("expected evidence, got none")
			case testCase.want != nil && *observation.ActionInFlight != *testCase.want:
				t.Fatalf("action_in_flight = %v, want %v", *observation.ActionInFlight, *testCase.want)
			}
		})
	}
}

func TestProfileEngineObserveOpsRejectsActionStartedAfterCapture(t *testing.T) {
	t.Parallel()
	identity := validRuntimeRequest().Identity
	identity.Controller = browserprotocol.ControllerAgent

	// Idle when the capture began, busy by the time it finished: the frame did
	// not span an action, so a single post-capture sample would have lied.
	observer := &samplingObserverStub{samples: [][2]any{{7, false}, {8, true}}}
	engine := &ProfileEngine{}
	engine.ops.Store(&profileOpsSnapshot{
		identity:          identity,
		profileGeneration: 3,
		observer:          observer,
	})
	observation, _, observerErr := engine.ObserveOps(
		context.Background(),
		identity.RunID,
		browserprotocol.OpsObserverStatusOperation,
	)
	if observerErr != nil {
		t.Fatal(observerErr)
	}
	if observation.ActionInFlight == nil || *observation.ActionInFlight {
		t.Fatal("a frame captured before the action started must not claim continuity")
	}
}
