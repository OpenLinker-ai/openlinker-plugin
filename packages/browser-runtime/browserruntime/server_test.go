//go:build !windows

package browserruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserclient"
	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserprotocol"
)

func TestFileLeaseLoadsCurrentOwnerOnlyAttachment(t *testing.T) {
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "active-lease.json")
	identity := validRuntimeRequest().Identity
	raw, err := json.Marshal(browserclient.Lease{
		ContractID: browserclient.LeaseContractID,
		ExpiresAt:  now.Add(time.Minute),
		Identity:   identity,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	validator := FileLease{Path: path, Now: func() time.Time { return now }}
	if failure := validator.Validate(identity); failure != nil {
		t.Fatalf("valid active lease failed: %v", failure)
	}
	stale := identity
	stale.ControlEpoch--
	if failure := validator.Validate(stale); failure == nil ||
		failure.Code != browserprotocol.ErrorStaleControlEpoch {
		t.Fatalf("stale epoch failure = %#v", failure)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if failure := validator.Validate(identity); failure == nil ||
		failure.Code != browserprotocol.ErrorRuntimeUnavailable {
		t.Fatalf("insecure lease failure = %#v", failure)
	}
}

func TestFileLeaseRevocationSurvivesNewConnectionsAndAllowsNewAttachment(t *testing.T) {
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "active-lease.json")
	identity := validRuntimeRequest().Identity
	writeActiveLeaseFixture(t, path, identity, now.Add(time.Minute))
	lease := FileLease{Path: path, Now: func() time.Time { return now }}
	engine := &fakeEngine{}
	server, socketPath, cancel, done := startTestServer(t, engine, ServerOptions{
		Lease: lease,
	})
	defer stopTestServer(t, server, cancel, done)

	closeRequest := validRuntimeRequest()
	closeRequest.Action = browserprotocol.Action{Kind: browserprotocol.ActionClose}
	closeResponse := sendRequest(t, socketPath, closeRequest)
	if closeResponse.Status != "ok" {
		t.Fatalf("close response = %#v", closeResponse)
	}
	repeatedClose := closeRequest
	repeatedClose.RequestID = "66666666-6666-4666-8666-666666666666"
	repeatedResponse := sendRequest(t, socketPath, repeatedClose)
	if repeatedResponse.Status != "ok" ||
		repeatedResponse.Observation == nil ||
		!strings.HasPrefix(repeatedResponse.Observation.PageStateID, "closed-") {
		t.Fatalf("repeated close response = %#v", repeatedResponse)
	}
	late := validRuntimeRequest()
	late.RequestID = "88888888-8888-4888-8888-888888888888"
	lateResponse := sendRequest(t, socketPath, late)
	if lateResponse.Status != "error" ||
		lateResponse.Error == nil ||
		lateResponse.Error.Code != browserprotocol.ErrorIdentityMismatch {
		t.Fatalf("late response = %#v", lateResponse)
	}
	if engine.calls.Load() != 2 {
		t.Fatalf(
			"engine calls = %d, want initial and idempotent close paths",
			engine.calls.Load(),
		)
	}
	restartedLease := FileLease{Path: path, Now: func() time.Time { return now }}
	if failure := restartedLease.Validate(identity); failure == nil ||
		failure.Code != browserprotocol.ErrorIdentityMismatch {
		t.Fatalf("restarted Runtime accepted revoked attachment: %#v", failure)
	}
	if failure := lease.Revoke(identity); failure != nil {
		t.Fatalf("idempotent revoke failure = %v", failure)
	}

	replacement := identity
	replacement.RunID = "99999999-9999-4999-8999-999999999999"
	replacement.AttachmentID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	replacement.ControlEpoch++
	writeActiveLeaseFixture(t, path, replacement, now.Add(time.Minute))
	if failure := lease.Validate(replacement); failure != nil {
		t.Fatalf("new attachment failure = %v", failure)
	}
	raw, err := os.ReadFile(filepath.Join(directory, revocationFileName))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{
		identity.RunID,
		identity.AgentID,
		identity.PrincipalScopeID,
		identity.AttachmentID,
	} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("revocation state leaked identity %q: %s", secret, raw)
		}
	}
}

func TestRequiredChallengeFencesBeforeInternalClose(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	directory := shortTempDir(t)
	path := filepath.Join(directory, "active-lease.json")
	identity := validRuntimeRequest().Identity
	writeActiveLeaseFixture(t, path, identity, now.Add(time.Minute))
	lease := FileLease{Path: path, Now: func() time.Time { return now }}
	var closeCalls atomic.Int64
	engine := &fakeEngine{
		execute: func(
			_ context.Context,
			requestIdentity browserprotocol.Identity,
			action browserprotocol.Action,
		) (browserprotocol.Observation, *browserprotocol.Failure) {
			if action.Kind == browserprotocol.ActionClose {
				if failure := lease.Validate(requestIdentity); failure == nil {
					t.Error("required-challenge cleanup ran before durable fencing")
				}
				closeCalls.Add(1)
				return closedAttachmentObservation(requestIdentity), nil
			}
			failure := browserprotocol.NewFailure(
				browserprotocol.ErrorChallengeRequired,
				"fixture challenge requires human control",
				false,
			)
			failure.SiteOutcome = browserprotocol.ErrorChallengeRequired
			failure.ClassifierRulesVersion =
				browserprotocol.ChallengeClassifierRulesVersion
			return browserprotocol.Observation{}, failure
		},
	}
	server, socketPath, cancel, done := startTestServer(t, engine, ServerOptions{
		Lease: lease,
	})
	defer stopTestServer(t, server, cancel, done)
	response := sendRequest(t, socketPath, validRuntimeRequest())
	if response.Status != "error" ||
		response.Error == nil ||
		response.Error.Code != browserprotocol.ErrorChallengeRequired {
		t.Fatalf("required challenge response = %#v", response)
	}
	if closeCalls.Load() != 1 {
		t.Fatalf("required challenge close calls = %d", closeCalls.Load())
	}
	if failure := lease.Validate(identity); failure == nil ||
		failure.Code != browserprotocol.ErrorIdentityMismatch {
		t.Fatalf("required challenge did not persist fence: %#v", failure)
	}
}

func TestFileLeaseRevocationCapacityEvictsOldestAndRetainsNewClosure(
	t *testing.T,
) {
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "active-lease.json")
	identity := validRuntimeRequest().Identity
	writeActiveLeaseFixture(t, path, identity, now.Add(time.Minute))
	lease := FileLease{Path: path, Now: func() time.Time { return now }}
	index := revocationIndex{
		ContractID: revocationContractID,
		Entries:    make([]revocationEntry, maxRevocations),
	}
	for position := range index.Entries {
		index.Entries[position] = revocationEntry{
			IdentityHash: fmt.Sprintf("%064x", position+1),
			RevokedAt:    now.Format(time.RFC3339Nano),
			ExpiresAt:    now.Add(time.Hour).Format(time.RFC3339Nano),
		}
	}
	if err := lease.writeRevocations(index); err != nil {
		t.Fatal(err)
	}

	if failure := lease.Revoke(identity); failure != nil {
		t.Fatalf("capacity revoke failure = %v", failure)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("active lease remains after durable revoke: %v", err)
	}
	retained, err := lease.readRevocations(now)
	if err != nil {
		t.Fatal(err)
	}
	if len(retained.Entries) != maxRevocations {
		t.Fatalf("revocation entries = %d, want %d", len(retained.Entries), maxRevocations)
	}
	for _, entry := range retained.Entries {
		if entry.IdentityHash == fmt.Sprintf("%064x", 1) {
			t.Fatal("oldest revocation entry was not evicted")
		}
	}
	revoked, failure := lease.IsRevoked(identity)
	if failure != nil || !revoked {
		t.Fatalf("new revocation was not retained: revoked=%t failure=%v", revoked, failure)
	}
}

func TestFileLeaseRevocationReclaimsEntriesAfterTheirLeaseExpires(t *testing.T) {
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "active-lease.json")
	identity := validRuntimeRequest().Identity
	writeActiveLeaseFixture(t, path, identity, now.Add(time.Minute))
	lease := FileLease{Path: path, Now: func() time.Time { return now }}
	index := revocationIndex{
		ContractID: revocationContractID,
		Entries:    make([]revocationEntry, maxRevocations),
	}
	for position := range index.Entries {
		index.Entries[position] = revocationEntry{
			IdentityHash: fmt.Sprintf("%064x", position+1),
			RevokedAt:    now.Add(-2 * time.Minute).Format(time.RFC3339Nano),
			ExpiresAt:    now.Add(time.Hour).Format(time.RFC3339Nano),
		}
	}
	index.Entries[0].ExpiresAt = now.Add(-time.Minute).Format(time.RFC3339Nano)
	if err := lease.writeRevocations(index); err != nil {
		t.Fatal(err)
	}

	if failure := lease.Revoke(identity); failure != nil {
		t.Fatalf("revoke after expired entry = %v", failure)
	}
	retained, err := lease.readRevocations(now)
	if err != nil {
		t.Fatal(err)
	}
	if len(retained.Entries) != maxRevocations {
		t.Fatalf("revocation entries = %d, want %d", len(retained.Entries), maxRevocations)
	}
	revoked, failure := lease.IsRevoked(identity)
	if failure != nil || !revoked {
		t.Fatalf("new revocation was not durable: revoked=%t failure=%v", revoked, failure)
	}
}

func TestCloseFencesAConcurrentLateConnectionBeforeEngineExecution(t *testing.T) {
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "active-lease.json")
	identity := validRuntimeRequest().Identity
	writeActiveLeaseFixture(t, path, identity, now.Add(time.Minute))
	started, release := make(chan struct{}), make(chan struct{})
	engine := &fakeEngine{run: func(context.Context) (
		browserprotocol.Observation,
		*browserprotocol.Failure,
	) {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			return browserprotocol.Observation{}, browserprotocol.NewFailure(
				browserprotocol.ErrorInternal,
				"active lease remained when Engine close started",
				false,
			)
		}
		close(started)
		<-release
		return browserprotocol.Observation{
			PageStateID: "closed-state",
		}, nil
	}}
	server, socketPath, cancel, done := startTestServer(t, engine, ServerOptions{
		Lease: FileLease{Path: path, Now: func() time.Time { return now }},
	})
	defer stopTestServer(t, server, cancel, done)

	closeRequest := validRuntimeRequest()
	closeRequest.Action = browserprotocol.Action{Kind: browserprotocol.ActionClose}
	closeResponse := make(chan browserprotocol.Response, 1)
	go func() {
		closeResponse <- sendRequest(t, socketPath, closeRequest)
	}()
	<-started
	lateRequest := validRuntimeRequest()
	lateRequest.RequestID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	lateResponse := make(chan browserprotocol.Response, 1)
	go func() {
		lateResponse <- sendRequest(t, socketPath, lateRequest)
	}()
	close(release)

	if response := <-closeResponse; response.Status != "ok" {
		t.Fatalf("close response = %#v", response)
	}
	response := <-lateResponse
	if response.Error == nil ||
		response.Error.Code != browserprotocol.ErrorIdentityMismatch {
		t.Fatalf("late response = %#v", response)
	}
	if engine.calls.Load() != 1 {
		t.Fatalf("concurrent late action reached engine: %d calls", engine.calls.Load())
	}
}

func TestCloseRevokeFailureDoesNotExecuteEngine(t *testing.T) {
	t.Parallel()
	lease := &fixtureRevokingLease{
		revokeFailure: browserprotocol.NewFailure(
			browserprotocol.ErrorRuntimeUnavailable,
			"fixture revoke failed",
			true,
		),
	}
	engine := &fakeEngine{}
	server, socketPath, cancel, done := startTestServer(t, engine, ServerOptions{
		Lease: lease,
	})
	defer stopTestServer(t, server, cancel, done)

	request := validRuntimeRequest()
	request.Action = browserprotocol.Action{Kind: browserprotocol.ActionClose}
	response := sendRequest(t, socketPath, request)
	if response.Error == nil ||
		response.Error.Code != browserprotocol.ErrorRuntimeUnavailable {
		t.Fatalf("revoke failure response = %#v", response)
	}
	if engine.calls.Load() != 0 {
		t.Fatalf("Engine executed before durable revoke: %d calls", engine.calls.Load())
	}
	if lease.isRevoked() {
		t.Fatal("failed revoke changed the durable fence")
	}
}

func TestAlreadyRevokedCloseRetriesEngineCheckpoint(t *testing.T) {
	t.Parallel()
	lease := &fixtureRevokingLease{}
	var closeCalls atomic.Int64
	engine := &fakeEngine{
		execute: func(
			_ context.Context,
			identity browserprotocol.Identity,
			action browserprotocol.Action,
		) (browserprotocol.Observation, *browserprotocol.Failure) {
			if action.Kind != browserprotocol.ActionClose {
				t.Fatalf("unexpected action = %q", action.Kind)
			}
			if !lease.isRevoked() {
				t.Fatal("Engine close started before durable revoke")
			}
			if closeCalls.Add(1) == 1 {
				return browserprotocol.Observation{}, browserprotocol.NewFailure(
					browserprotocol.ErrorRuntimeUnavailable,
					"fixture checkpoint failed",
					true,
				)
			}
			return closedAttachmentObservation(identity), nil
		},
	}
	server, socketPath, cancel, done := startTestServer(t, engine, ServerOptions{
		Lease: lease,
	})
	defer stopTestServer(t, server, cancel, done)

	first := validRuntimeRequest()
	first.Action = browserprotocol.Action{Kind: browserprotocol.ActionClose}
	firstResponse := sendRequest(t, socketPath, first)
	if firstResponse.Error == nil ||
		firstResponse.Error.Code != browserprotocol.ErrorRuntimeUnavailable {
		t.Fatalf("first close response = %#v", firstResponse)
	}
	second := first
	second.RequestID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	secondResponse := sendRequest(t, socketPath, second)
	if secondResponse.Status != "ok" || secondResponse.Observation == nil {
		t.Fatalf("second close response = %#v", secondResponse)
	}
	if closeCalls.Load() != 2 || lease.revokeCalls.Load() != 1 {
		t.Fatalf(
			"close calls = %d, revoke calls = %d",
			closeCalls.Load(),
			lease.revokeCalls.Load(),
		)
	}
}

type fixtureRevokingLease struct {
	mu            sync.Mutex
	revoked       bool
	revokeCalls   atomic.Int64
	revokeFailure *browserprotocol.Failure
}

func (lease *fixtureRevokingLease) Validate(
	browserprotocol.Identity,
) *browserprotocol.Failure {
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.revoked {
		return browserprotocol.NewFailure(
			browserprotocol.ErrorIdentityMismatch,
			"fixture attachment is revoked",
			false,
		)
	}
	return nil
}

func (lease *fixtureRevokingLease) IsRevoked(
	browserprotocol.Identity,
) (bool, *browserprotocol.Failure) {
	return lease.isRevoked(), nil
}

func (lease *fixtureRevokingLease) isRevoked() bool {
	lease.mu.Lock()
	defer lease.mu.Unlock()
	return lease.revoked
}

func (lease *fixtureRevokingLease) Revoke(
	browserprotocol.Identity,
) *browserprotocol.Failure {
	lease.revokeCalls.Add(1)
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.revokeFailure != nil {
		return lease.revokeFailure
	}
	lease.revoked = true
	return nil
}

func writeActiveLeaseFixture(
	t *testing.T,
	path string,
	identity browserprotocol.Identity,
	expiresAt time.Time,
) {
	t.Helper()
	raw, err := json.Marshal(browserclient.Lease{
		ContractID: browserclient.LeaseContractID,
		ExpiresAt:  expiresAt,
		Identity:   identity,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

type fakeEngine struct {
	calls   atomic.Int64
	run     func(context.Context) (browserprotocol.Observation, *browserprotocol.Failure)
	execute func(
		context.Context,
		browserprotocol.Identity,
		browserprotocol.Action,
	) (browserprotocol.Observation, *browserprotocol.Failure)
}

func (engine *fakeEngine) Execute(
	ctx context.Context,
	identity browserprotocol.Identity,
	action browserprotocol.Action,
) (browserprotocol.Observation, *browserprotocol.Failure) {
	engine.calls.Add(1)
	if engine.execute != nil {
		return engine.execute(ctx, identity, action)
	}
	if engine.run != nil {
		return engine.run(ctx)
	}
	if action.Kind == browserprotocol.ActionClose {
		return closedAttachmentObservation(identity), nil
	}
	return browserprotocol.Observation{
		PageStateID: "state-1",
		Viewport: &browserprotocol.Viewport{
			Width:  browserprotocol.BrowserViewportWidth,
			Height: browserprotocol.BrowserViewportHeight,
		},
		NavigationGeneration: 1,
		AXTree:               json.RawMessage(`{"role":"document"}`),
		Origin:               "https://example.com",
		Title:                "Example",
	}, nil
}

func TestServerBoundsBlockedClicksAcrossNavigationAndAttachmentRotation(
	t *testing.T,
) {
	t.Parallel()
	server, err := NewServer(ServerOptions{
		SocketPath:        filepath.Join(shortTempDir(t), "browser.sock"),
		ChannelCredential: strings.Repeat("a", 64),
		Lease:             NoActiveLease{},
		Engine:            NotReadyEngine{},
	})
	if err != nil {
		t.Fatal(err)
	}
	identity := validRuntimeRequest().Identity
	click := browserprotocol.Action{Kind: browserprotocol.ActionClick}
	for attempt := 1; attempt <= maxBlockedClicksPerNavigation; attempt++ {
		requestID := fmt.Sprintf("00000000-0000-4000-8000-%012d", attempt)
		if failure := server.reserveRequest(identity, requestID, click); failure != nil {
			t.Fatalf("reserve blocked click %d: %v", attempt, failure)
		}
		blocked := browserprotocol.NewFailure(
			browserprotocol.ErrorHighImpactActionBlocked,
			"blocked",
			false,
		)
		blocked.TargetCategory = browserprotocol.TargetCategoryButton
		blocked.PageStateID = "state-1"
		blocked.NavigationGeneration = 7
		blocked.EngineInstanceID = 41
		blocked = server.recordBlockedClick(identity, blocked)
		if blocked.Code != browserprotocol.ErrorHighImpactActionBlocked ||
			blocked.BlockedClickNavigationAttemptsRemaining == nil ||
			*blocked.BlockedClickNavigationAttemptsRemaining !=
				maxBlockedClicksPerNavigation-attempt {
			t.Fatalf("blocked click %d = %#v", attempt, blocked)
		}
	}
	decreased := browserprotocol.Observation{
		PageStateID: "state-decreased",
		Viewport: &browserprotocol.Viewport{
			Width:  browserprotocol.BrowserViewportWidth,
			Height: browserprotocol.BrowserViewportHeight,
		},
		NavigationGeneration: 6,
		EngineInstanceID:     41,
	}
	if failure := server.recordEngineObservation(identity, decreased); failure == nil ||
		failure.Code != browserprotocol.ErrorOutputInvalid {
		t.Fatalf("decreasing generation failure = %#v", failure)
	}
	fourth := server.reserveRequest(
		identity,
		"00000000-0000-4000-8000-000000000004",
		click,
	)
	if fourth == nil || fourth.Code != browserprotocol.ErrorClickRetryExhausted {
		t.Fatalf("fourth blocked click = %#v", fourth)
	}
	if failure := browserprotocol.ValidateFailure(fourth); failure != nil {
		t.Fatalf("fourth blocked click wire evidence = %v", failure)
	}

	rotated := identity
	rotated.SessionEpoch++
	rotated.ControlEpoch++
	rotated.AttachmentID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	if failure := server.reserveRequest(
		rotated,
		"00000000-0000-4000-8000-000000000005",
		click,
	); failure != nil {
		t.Fatalf("Engine/attachment rotation did not reset navigation budget: %v", failure)
	}
	rotatedFailure := browserprotocol.NewFailure(
		browserprotocol.ErrorHighImpactActionBlocked,
		"blocked",
		false,
	)
	rotatedFailure.TargetCategory = browserprotocol.TargetCategoryCustom
	rotatedFailure.PageStateID = "state-2"
	rotatedFailure.NavigationGeneration = 1
	rotatedFailure.EngineInstanceID = 42
	rotatedFailure = server.recordBlockedClick(rotated, rotatedFailure)
	if rotatedFailure.BlockedClickRunAttemptsRemaining == nil ||
		*rotatedFailure.BlockedClickRunAttemptsRemaining !=
			maxBlockedClicksPerRun-4 {
		t.Fatalf("Run budget was reset by rotation: %#v", rotatedFailure)
	}

	server.seenMu.Lock()
	server.blockedClickRunCount = maxBlockedClicksPerRun
	server.seenMu.Unlock()
	runExhausted := server.reserveRequest(
		rotated,
		"00000000-0000-4000-8000-000000000006",
		click,
	)
	if runExhausted == nil ||
		runExhausted.Code != browserprotocol.ErrorClickRetryExhausted {
		t.Fatalf("Run-wide click budget = %#v", runExhausted)
	}
	nextRun := rotated
	nextRun.RunID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	nextRun.AttachmentID = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	if failure := server.reserveRequest(
		nextRun,
		"00000000-0000-4000-8000-000000000007",
		click,
	); failure != nil {
		t.Fatalf("new Run did not receive a fresh budget: %v", failure)
	}
}

func TestServerBoundsCloseRetriesAndSeenMemory(t *testing.T) {
	t.Parallel()
	server, err := NewServer(ServerOptions{
		SocketPath:        filepath.Join(shortTempDir(t), "browser.sock"),
		ChannelCredential: strings.Repeat("a", 64),
		Lease:             NoActiveLease{},
		Engine:            NotReadyEngine{},
	})
	if err != nil {
		t.Fatal(err)
	}
	identity := validRuntimeRequest().Identity
	server.seenMu.Lock()
	server.actionCount = server.options.MaxRequests
	server.seenMu.Unlock()
	closeAction := browserprotocol.Action{Kind: browserprotocol.ActionClose}
	for attempt := 1; attempt <= maxCloseAttemptsPerAttachment; attempt++ {
		requestID := fmt.Sprintf("10000000-0000-4000-8000-%012d", attempt)
		if failure := server.reserveRequest(
			identity,
			requestID,
			closeAction,
		); failure != nil {
			t.Fatalf("close attempt %d: %v", attempt, failure)
		}
	}
	fourth := server.reserveRequest(
		identity,
		"10000000-0000-4000-8000-000000000004",
		closeAction,
	)
	if fourth == nil || fourth.Code != browserprotocol.ErrorCloseRetryExhausted {
		t.Fatalf("fourth close = %#v", fourth)
	}
	for attempt := 5; attempt < 20; attempt++ {
		_ = server.reserveRequest(
			identity,
			fmt.Sprintf("10000000-0000-4000-8000-%012d", attempt),
			closeAction,
		)
	}
	server.seenMu.Lock()
	seenCount := len(server.seen)
	server.seenMu.Unlock()
	if seenCount != maxCloseAttemptsPerAttachment {
		t.Fatalf("close replay memory = %d, want %d", seenCount, maxCloseAttemptsPerAttachment)
	}
	nonClose := server.reserveRequest(
		identity,
		"20000000-0000-4000-8000-000000000001",
		browserprotocol.Action{Kind: browserprotocol.ActionScreenshot},
	)
	if nonClose == nil || nonClose.Code != browserprotocol.ErrorCloseRetryExhausted {
		t.Fatalf("terminal attachment accepted non-close action: %#v", nonClose)
	}
}

func TestServerUsesProtectedUnixSocketAndExecutesAuthorizedAction(t *testing.T) {
	t.Parallel()
	engine := &fakeEngine{}
	server, socketPath, cancel, done := startTestServer(t, engine, ServerOptions{})
	defer stopTestServer(t, server, cancel, done)

	info, err := os.Lstat(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("socket mode = %v, want Unix socket", info.Mode())
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("socket permissions = %o, want 0600", info.Mode().Perm())
	}
	server.mu.Lock()
	network := server.listener.Addr().Network()
	server.mu.Unlock()
	if network != "unix" {
		t.Fatalf("listener network = %q, want unix", network)
	}

	response := sendRequest(t, socketPath, validRuntimeRequest())
	if response.Status != "ok" || response.Observation == nil || response.Observation.PageStateID != "state-1" {
		t.Fatalf("response = %#v", response)
	}
	if engine.calls.Load() != 1 {
		t.Fatalf("engine calls = %d, want 1", engine.calls.Load())
	}
}

func TestServerRejectsCredentialIdentityAndStaleEpochBeforeEngine(t *testing.T) {
	t.Parallel()
	engine := &fakeEngine{}
	expected := validRuntimeRequest().Identity
	expected.ControlEpoch = 2
	server, socketPath, cancel, done := startTestServer(t, engine, ServerOptions{
		Lease: StaticLease{Identity: expected},
	})
	defer stopTestServer(t, server, cancel, done)

	cases := []struct {
		name string
		edit func(*browserprotocol.Request)
		code browserprotocol.ErrorCode
	}{
		{
			name: "credential",
			edit: func(request *browserprotocol.Request) {
				request.ChannelCredential = strings.Repeat("b", 64)
			},
			code: browserprotocol.ErrorUnauthorized,
		},
		{
			name: "cross run",
			edit: func(request *browserprotocol.Request) {
				request.Identity.RunID = "99999999-9999-4999-8999-999999999999"
			},
			code: browserprotocol.ErrorIdentityMismatch,
		},
		{
			name: "stale epoch",
			edit: func(request *browserprotocol.Request) {
				request.Identity.ControlEpoch = 1
			},
			code: browserprotocol.ErrorStaleControlEpoch,
		},
	}
	for _, test := range cases {
		test := test
		t.Run(test.name, func(t *testing.T) {
			request := validRuntimeRequest()
			test.edit(&request)
			response := sendRequest(t, socketPath, request)
			if response.Error == nil || response.Error.Code != test.code {
				t.Fatalf("response = %#v, want %s", response, test.code)
			}
		})
	}
	if engine.calls.Load() != 0 {
		t.Fatalf("engine calls = %d, want 0", engine.calls.Load())
	}
}

func TestServerRejectsUnknownFieldsAndOversizedRequests(t *testing.T) {
	t.Parallel()
	engine := &fakeEngine{}
	server, socketPath, cancel, done := startTestServer(t, engine, ServerOptions{MaxRequestBytes: 1024})
	defer stopTestServer(t, server, cancel, done)

	request := validRuntimeRequest()
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw[:len(raw)-1], []byte(`,"cdp_command":"Runtime.evaluate"}`)...)
	response := sendRaw(t, socketPath, raw)
	if response.Error == nil || response.Error.Code != browserprotocol.ErrorProtocolInvalid {
		t.Fatalf("unknown-field response = %#v", response)
	}

	large := append([]byte(`{"padding":"`), []byte(strings.Repeat("x", 2048))...)
	large = append(large, []byte(`"}`)...)
	response = sendRaw(t, socketPath, large)
	if response.Error == nil || response.Error.Code != browserprotocol.ErrorRequestTooLarge {
		t.Fatalf("oversized response = %#v", response)
	}
	if engine.calls.Load() != 0 {
		t.Fatalf("engine calls = %d, want 0", engine.calls.Load())
	}
}

func TestServerCancelsEngineAtRequestDeadline(t *testing.T) {
	t.Parallel()
	engine := &fakeEngine{
		run: func(ctx context.Context) (browserprotocol.Observation, *browserprotocol.Failure) {
			<-ctx.Done()
			return browserprotocol.Observation{}, nil
		},
	}
	server, socketPath, cancel, done := startTestServer(t, engine, ServerOptions{})
	defer stopTestServer(t, server, cancel, done)

	request := validRuntimeRequest()
	request.Deadline = time.Now().UTC().Add(50 * time.Millisecond)
	response := sendRequest(t, socketPath, request)
	if response.Error == nil || response.Error.Code != browserprotocol.ErrorDeadlineExceeded {
		t.Fatalf("response = %#v", response)
	}
}

func TestServerRejectsRequestReplayAndActionLimitWithoutReexecution(t *testing.T) {
	t.Parallel()
	engine := &fakeEngine{}
	server, socketPath, cancel, done := startTestServer(t, engine, ServerOptions{MaxRequests: 1})
	defer stopTestServer(t, server, cancel, done)

	request := validRuntimeRequest()
	if response := sendRequest(t, socketPath, request); response.Status != "ok" {
		t.Fatalf("first response = %#v", response)
	}
	response := sendRequest(t, socketPath, request)
	if response.Error == nil || response.Error.Code != browserprotocol.ErrorRequestReplayed {
		t.Fatalf("replay response = %#v", response)
	}
	request.RequestID = "88888888-8888-4888-8888-888888888888"
	response = sendRequest(t, socketPath, request)
	if response.Error == nil || response.Error.Code != browserprotocol.ErrorActionLimitExceeded {
		t.Fatalf("limit response = %#v", response)
	}
	if engine.calls.Load() != 1 {
		t.Fatalf("engine calls = %d, want 1", engine.calls.Load())
	}
}

func TestReplayGuardResetsForNewAttachmentIdentity(t *testing.T) {
	t.Parallel()
	server, err := NewServer(ServerOptions{
		SocketPath:        filepath.Join(shortTempDir(t), "browser.sock"),
		ChannelCredential: strings.Repeat("a", 64),
		Lease:             NoActiveLease{},
		Engine:            NotReadyEngine{},
		MaxRequests:       1,
	})
	if err != nil {
		t.Fatal(err)
	}
	identity := validRuntimeRequest().Identity
	requestID := "77777777-7777-4777-8777-777777777777"
	if failure := server.reserveRequest(
		identity,
		requestID,
		browserprotocol.Action{Kind: browserprotocol.ActionScreenshot},
	); failure != nil {
		t.Fatal(failure)
	}
	if failure := server.reserveRequest(
		identity,
		requestID,
		browserprotocol.Action{Kind: browserprotocol.ActionScreenshot},
	); failure == nil ||
		failure.Code != browserprotocol.ErrorRequestReplayed {
		t.Fatalf("replay failure = %#v", failure)
	}
	identity.AttachmentID = "66666666-6666-4666-8666-666666666666"
	if failure := server.reserveRequest(
		identity,
		requestID,
		browserprotocol.Action{Kind: browserprotocol.ActionScreenshot},
	); failure != nil {
		t.Fatalf("new attachment failure = %v", failure)
	}
}

func TestBatchConsumesThePerAttachmentActionBudget(t *testing.T) {
	t.Parallel()
	server, err := NewServer(ServerOptions{
		SocketPath:        filepath.Join(shortTempDir(t), "browser.sock"),
		ChannelCredential: strings.Repeat("a", 64),
		Lease:             NoActiveLease{},
		Engine:            NotReadyEngine{},
		MaxRequests:       3,
	})
	if err != nil {
		t.Fatal(err)
	}
	identity := validRuntimeRequest().Identity
	batch := browserprotocol.Action{
		Kind: browserprotocol.ActionBatch,
		Actions: []browserprotocol.Action{
			{Kind: browserprotocol.ActionScreenshot},
			{Kind: browserprotocol.ActionScreenshot},
		},
	}
	if failure := server.reserveRequest(
		identity,
		"77777777-7777-4777-8777-777777777777",
		batch,
	); failure != nil {
		t.Fatal(failure)
	}
	if failure := server.reserveRequest(
		identity,
		"88888888-8888-4888-8888-888888888888",
		browserprotocol.Action{Kind: browserprotocol.ActionScreenshot},
	); failure != nil {
		t.Fatal(failure)
	}
	if failure := server.reserveRequest(
		identity,
		"99999999-9999-4999-8999-999999999999",
		browserprotocol.Action{Kind: browserprotocol.ActionScreenshot},
	); failure == nil ||
		failure.Code != browserprotocol.ErrorActionLimitExceeded {
		t.Fatalf("action budget failure = %#v", failure)
	}
}

func TestServerReplacesOversizedEncodedResponseWithStableError(t *testing.T) {
	t.Parallel()
	engine := &fakeEngine{
		run: func(context.Context) (browserprotocol.Observation, *browserprotocol.Failure) {
			return browserprotocol.Observation{
				PageStateID: "state-1",
				Viewport: &browserprotocol.Viewport{
					Width:  browserprotocol.BrowserViewportWidth,
					Height: browserprotocol.BrowserViewportHeight,
				},
				NavigationGeneration: 1,
				Title:                strings.Repeat("x", 1500),
			}, nil
		},
	}
	server, socketPath, cancel, done := startTestServer(t, engine, ServerOptions{MaxResponseBytes: 1024})
	defer stopTestServer(t, server, cancel, done)

	response := sendRequest(t, socketPath, validRuntimeRequest())
	if response.Error == nil || response.Error.Code != browserprotocol.ErrorOutputTooLarge {
		t.Fatalf("response = %#v", response)
	}
}

func TestServerReturnsStableNotReadyWithoutActiveLease(t *testing.T) {
	t.Parallel()
	server, socketPath, cancel, done := startTestServer(t, NotReadyEngine{}, ServerOptions{
		Lease: NoActiveLease{},
	})
	defer stopTestServer(t, server, cancel, done)

	response := sendRequest(t, socketPath, validRuntimeRequest())
	if response.Error == nil || response.Error.Code != browserprotocol.ErrorRuntimeUnavailable {
		t.Fatalf("response = %#v", response)
	}
}

func TestServerReplacesOnlyStaleUnixSockets(t *testing.T) {
	t.Parallel()
	dir := shortTempDir(t)
	path := filepath.Join(dir, "browser.sock")
	if err := os.WriteFile(path, []byte("not a socket"), 0o600); err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(ServerOptions{
		SocketPath:        path,
		ChannelCredential: strings.Repeat("a", 64),
		Lease:             NoActiveLease{},
		Engine:            NotReadyEngine{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Open(); err == nil {
		t.Fatal("Open() succeeded with a regular file at the socket path")
	}

	stalePath := filepath.Join(dir, "stale.sock")
	stale, err := net.ListenUnix("unix", &net.UnixAddr{Name: stalePath, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	stale.SetUnlinkOnClose(false)
	if err := stale.Close(); err != nil {
		t.Fatal(err)
	}
	staleDeadline := time.Now().Add(time.Second)
	for {
		connection, dialErr := net.DialTimeout("unix", stalePath, 10*time.Millisecond)
		if dialErr != nil {
			break
		}
		_ = connection.Close()
		if time.Now().After(staleDeadline) {
			t.Fatal("closed Unix listener continued accepting connections")
		}
		time.Sleep(time.Millisecond)
	}
	staleServer, err := NewServer(ServerOptions{
		SocketPath:        stalePath,
		ChannelCredential: strings.Repeat("a", 64),
		Lease:             NoActiveLease{},
		Engine:            NotReadyEngine{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := staleServer.Open(); err != nil {
		t.Fatalf("Open() stale socket error = %v", err)
	}
	if err := staleServer.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestServerDoesNotReplaceActiveUnixSocket(t *testing.T) {
	t.Parallel()
	engine := &fakeEngine{}
	server, socketPath, cancel, done := startTestServer(t, engine, ServerOptions{})
	defer stopTestServer(t, server, cancel, done)

	second, err := NewServer(ServerOptions{
		SocketPath:        socketPath,
		ChannelCredential: strings.Repeat("a", 64),
		Lease:             NoActiveLease{},
		Engine:            NotReadyEngine{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Open(); err == nil {
		t.Fatal("second Open() replaced an active Unix socket")
	}
	response := sendRequest(t, socketPath, validRuntimeRequest())
	if response.Status != "ok" {
		t.Fatalf("original server response = %#v", response)
	}
}

func startTestServer(
	t *testing.T,
	engine Engine,
	overrides ServerOptions,
) (*Server, string, context.CancelFunc, <-chan error) {
	t.Helper()
	dir := shortTempDir(t)
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	socketPath := filepath.Join(dir, "browser.sock")
	identity := validRuntimeRequest().Identity
	options := ServerOptions{
		SocketPath:        socketPath,
		ChannelCredential: strings.Repeat("a", 64),
		Lease:             StaticLease{Identity: identity},
		Engine:            engine,
	}
	if overrides.Lease != nil {
		options.Lease = overrides.Lease
	}
	if overrides.MaxRequestBytes > 0 {
		options.MaxRequestBytes = overrides.MaxRequestBytes
	}
	if overrides.MaxResponseBytes > 0 {
		options.MaxResponseBytes = overrides.MaxResponseBytes
	}
	if overrides.MaxRequests > 0 {
		options.MaxRequests = overrides.MaxRequests
	}
	server, err := NewServer(options)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Open(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- server.Serve(ctx)
	}()
	return server, socketPath, cancel, done
}

func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "olbr-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func stopTestServer(t *testing.T, server *Server, cancel context.CancelFunc, done <-chan error) {
	t.Helper()
	cancel()
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("browser server did not stop")
	}
}

func sendRequest(t *testing.T, socketPath string, request browserprotocol.Request) browserprotocol.Response {
	t.Helper()
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	return sendRaw(t, socketPath, raw)
}

func sendRaw(t *testing.T, socketPath string, raw []byte) browserprotocol.Response {
	t.Helper()
	connection, err := net.DialTimeout("unix", socketPath, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if _, err := connection.Write(append(raw, '\n')); err != nil {
		t.Fatal(err)
	}
	if unixConnection, ok := connection.(*net.UnixConn); ok {
		_ = unixConnection.CloseWrite()
	}
	_ = connection.SetReadDeadline(time.Now().Add(2 * time.Second))
	var response browserprotocol.Response
	if err := json.NewDecoder(connection).Decode(&response); err != nil {
		t.Fatal(err)
	}
	return response
}

func validRuntimeRequest() browserprotocol.Request {
	return browserprotocol.Request{
		ContractID:        browserprotocol.ContractID,
		ChannelCredential: strings.Repeat("a", 64),
		RequestID:         "77777777-7777-4777-8777-777777777777",
		Deadline:          time.Now().UTC().Add(5 * time.Second),
		Identity: browserprotocol.Identity{
			RunID:                              "11111111-1111-4111-8111-111111111111",
			AgentID:                            "22222222-2222-4222-8222-222222222222",
			PrincipalScopeID:                   "scope_333333333333",
			BrowserSessionID:                   "44444444-4444-4444-8444-444444444444",
			SessionEpoch:                       1,
			AttachmentID:                       "55555555-5555-4555-8555-555555555555",
			ControlEpoch:                       1,
			Controller:                         browserprotocol.ControllerAgent,
			BrowserInteractionPolicy:           "restricted",
			BrowserInteractionPolicyGeneration: 1,
			BrowserMutationOrigins:             []string{},
			BrowserMutationOriginsSHA256:       browserprotocol.RestrictedMutationOriginsSHA256,
		},
		Action: browserprotocol.Action{Kind: browserprotocol.ActionScreenshot},
	}
}
