package agentexec

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	openlinker "github.com/OpenLinker-ai/openlinker-go"
	"github.com/OpenLinker-ai/openlinker-plugin/packages/agent-adapters/browserextension"
	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserprotocol"
)

func runtimeAttemptIdentityFor(runID, attemptID string) openlinker.RuntimeAttemptIdentity {
	return openlinker.RuntimeAttemptIdentity{
		RunID:            runID,
		AttemptID:        attemptID,
		LeaseID:          "33333333-3333-4333-8333-333333333333",
		FencingToken:     7,
		NodeID:           "44444444-4444-4444-8444-444444444444",
		AgentID:          "55555555-5555-4555-8555-555555555555",
		WorkerID:         "99999999-9999-4999-8999-999999999999",
		RuntimeSessionID: "66666666-6666-4666-8666-666666666666",
	}
}

func observationCommand(action browserextension.ObserverBridgeAction) browserextension.ObserverBridgeCommand {
	now := time.Now().UTC()
	return browserextension.ObserverBridgeCommand{
		AttemptIdentity: runtimeAttemptIdentityFor(
			"11111111-1111-4111-8111-111111111111",
			"22222222-2222-4222-8222-222222222222",
		),
		SessionEpoch:         4,
		BrowserSessionSHA256: strings.Repeat("a", 64),
		AttachmentSHA256:     strings.Repeat("b", 64),
		CommandID:            "44444444-4444-4444-8444-444444444444",
		Action:               action,
		LeaseID:              "55555555-5555-4555-8555-555555555555",
		LeaseExpiresAt:       now.Add(5 * time.Minute),
		DeadlineAt:           now.Add(time.Minute),
		FrameIntervalMS:      browserextension.ObserverBridgeDefaultFrameIntervalMS,
	}
}

// A frame captured after the Runtime moved on belongs to a different Run. The
// comparison has to cover every field the command named, so each one is drifted
// independently.
// The command names an Attempt in hashed form; the Worker rehashes its own
// identity to check. Every field must participate, or Core could start an
// observation against an Attempt this Runtime has already left.
func TestCommandMustNameTheLocalAttempt(t *testing.T) {
	t.Parallel()
	local := browserprotocol.Identity{
		RunID:            "11111111-1111-4111-8111-111111111111",
		SessionEpoch:     4,
		BrowserSessionID: "browser-session-a",
		AttachmentID:     "attachment-a",
	}
	command := observationCommand(browserextension.ObserverBridgeStart)
	command.BrowserSessionSHA256 = browserIdentityEvidenceSHA256(
		browserSessionEvidenceDomain, local.BrowserSessionID,
	)
	command.AttachmentSHA256 = browserIdentityEvidenceSHA256(
		browserAttachmentEvidenceDomain, local.AttachmentID,
	)
	if !commandNamesLocalAttempt(command, local) {
		t.Fatal("a matching command was rejected")
	}
	for name, mutate := range map[string]func(*browserprotocol.Identity){
		"run":     func(i *browserprotocol.Identity) { i.RunID = "66666666-6666-4666-8666-666666666666" },
		"epoch":   func(i *browserprotocol.Identity) { i.SessionEpoch++ },
		"session": func(i *browserprotocol.Identity) { i.BrowserSessionID = "browser-session-b" },
		"attach":  func(i *browserprotocol.Identity) { i.AttachmentID = "attachment-b" },
	} {
		t.Run(name, func(t *testing.T) {
			drifted := local
			mutate(&drifted)
			if commandNamesLocalAttempt(command, drifted) {
				t.Fatalf("%s drift was accepted", name)
			}
		})
	}
}

// A command naming another Attempt must be ignored outright rather than starting
// an observation bound to the wrong Run.
func TestObservationRejectsForeignAttemptCommands(t *testing.T) {
	t.Parallel()
	observation := newBrowserObservation(nil, nil)
	command := observationCommand(browserextension.ObserverBridgeStart)
	payload, err := json.Marshal(command)
	if err != nil {
		t.Fatal(err)
	}
	observation.handleCommand(t.Context(), payload, runtimeAttemptIdentityFor(
		"77777777-7777-4777-8777-777777777777",
		command.AttemptIdentity.AttemptID,
	))
	observation.mu.Lock()
	leaseID := observation.leaseID
	observation.mu.Unlock()
	if leaseID != "" {
		t.Fatal("a command for another Run started an observation")
	}
}

// stop must be safe before anything started and must clear the lease, so a
// Run-terminal or disconnect path can always call it.
func TestObservationStopIsIdempotent(t *testing.T) {
	t.Parallel()
	observation := newBrowserObservation(nil, nil)
	observation.stop()
	observation.mu.Lock()
	observation.leaseID = "55555555-5555-4555-8555-555555555555"
	observation.commandID = "44444444-4444-4444-8444-444444444444"
	observation.mu.Unlock()
	observation.stop()
	observation.stop()
	observation.mu.Lock()
	defer observation.mu.Unlock()
	if observation.leaseID != "" || observation.commandID != "" || observation.cancel != nil {
		t.Fatal("stop left observation state behind")
	}
}

// Core retries the exact one-way start until its lifecycle handshake settles.
// The Worker must recognize that delivery replay without replacing the stream
// or reporting that the same observer conflicts with itself.
func TestObservationStartReplayIsIdempotent(t *testing.T) {
	t.Parallel()
	observation := newBrowserObservation(nil, nil)
	command := observationCommand(browserextension.ObserverBridgeStart)
	observation.mu.Lock()
	observation.leaseID = command.LeaseID
	observation.commandID = command.CommandID
	observation.mu.Unlock()

	observation.start(t.Context(), command)
	observation.mu.Lock()
	defer observation.mu.Unlock()
	if observation.leaseID != command.LeaseID ||
		observation.commandID != command.CommandID {
		t.Fatal("an identical start replay changed the active observation")
	}
}

// Malformed or invalid commands must not start anything; the bridge fails closed
// rather than observing with defaults it invented.
func TestObservationIgnoresInvalidCommands(t *testing.T) {
	t.Parallel()
	identity := runtimeAttemptIdentityFor(
		"11111111-1111-4111-8111-111111111111",
		"22222222-2222-4222-8222-222222222222",
	)
	for name, payload := range map[string][]byte{
		"not json":     []byte("{"),
		"empty object": []byte("{}"),
		"bad interval": observationPayload(t, func(c *browserextension.ObserverBridgeCommand) { c.FrameIntervalMS = 1 }),
		"no deadline":  observationPayload(t, func(c *browserextension.ObserverBridgeCommand) { c.DeadlineAt = time.Time{} }),
		"bad action":   observationPayload(t, func(c *browserextension.ObserverBridgeCommand) { c.Action = "observe" }),
	} {
		t.Run(name, func(t *testing.T) {
			observation := newBrowserObservation(nil, nil)
			observation.handleCommand(t.Context(), payload, identity)
			observation.mu.Lock()
			defer observation.mu.Unlock()
			if observation.leaseID != "" {
				t.Fatalf("%s started an observation", name)
			}
		})
	}
}

func observationPayload(
	t *testing.T,
	mutate func(*browserextension.ObserverBridgeCommand),
) []byte {
	t.Helper()
	command := observationCommand(browserextension.ObserverBridgeStart)
	mutate(&command)
	payload, err := json.Marshal(command)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

// The frame carries the identity the Runtime backfilled at capture time, which
// is stronger evidence than the Worker's own snapshot: it describes the Attempt
// the pixels actually came from.
// The capture is compared against the Worker's own live identity, using the bare
// digest the Runtime returns. An attachment can rotate between the snapshot and
// the capture, so the digest has to participate.
func TestObservationRejectsFramesFromAnotherAttempt(t *testing.T) {
	t.Parallel()
	local := browserprotocol.Identity{
		RunID:        "11111111-1111-4111-8111-111111111111",
		SessionEpoch: 4,
		AttachmentID: "attachment-a",
	}
	matching := browserprotocol.OpsObserverObservation{
		RunID:            local.RunID,
		SessionEpoch:     local.SessionEpoch,
		AttachmentSHA256: capturedAttachmentDigest(local),
	}
	if capturedFrameIsForeign(local, matching) {
		t.Fatal("a matching capture was treated as foreign")
	}
	for name, mutate := range map[string]func(*browserprotocol.OpsObserverObservation){
		"run":   func(o *browserprotocol.OpsObserverObservation) { o.RunID = "66666666-6666-4666-8666-666666666666" },
		"epoch": func(o *browserprotocol.OpsObserverObservation) { o.SessionEpoch++ },
		"attachment": func(o *browserprotocol.OpsObserverObservation) {
			rotated := local
			rotated.AttachmentID = "attachment-rotated"
			o.AttachmentSHA256 = capturedAttachmentDigest(rotated)
		},
	} {
		t.Run(name, func(t *testing.T) {
			drifted := matching
			mutate(&drifted)
			if !capturedFrameIsForeign(local, drifted) {
				t.Fatalf("a capture with a drifted %s was accepted", name)
			}
		})
	}
}

// Core authorizes observation from the ready lifecycle, which is intentionally
// published before the provider's first Browser action. Engine inactivity is
// therefore a wait state while the Worker's Attempt identity is still live, not
// proof that the Run ended. The identity checks in stream remain the terminal
// authority; protocol/internal failures must still close the lease.
func TestObservationWaitsForTheFirstBrowserAction(t *testing.T) {
	t.Parallel()
	for _, code := range []browserprotocol.OpsObserverErrorCode{
		browserprotocol.OpsObserverRunNotActive,
		browserprotocol.OpsObserverBusyError,
	} {
		failure := browserprotocol.NewOpsObserverError(code, "transient")
		if !observerEngineErrorIsTransient(failure) {
			t.Fatalf("%s ended an otherwise live Attempt observation", code)
		}
	}
	for _, code := range []browserprotocol.OpsObserverErrorCode{
		browserprotocol.OpsObserverInternalError,
		browserprotocol.OpsObserverProtocolError,
		browserprotocol.OpsObserverDisabled,
	} {
		failure := browserprotocol.NewOpsObserverError(code, "terminal")
		if observerEngineErrorIsTransient(failure) {
			t.Fatalf("%s was treated as a transient Engine state", code)
		}
	}
	if observerEngineErrorIsTransient(nil) {
		t.Fatal("nil observer failure was treated as transient")
	}
}

// observationCaptureStub answers the Engine calls a capture makes, in order. It
// exists so the final-capture rules can be tested without a Browser socket.
type observationCaptureStub struct {
	mu        sync.Mutex
	calls     int
	responses []observationCaptureStubReply
}

type observationCaptureStubReply struct {
	response browserprotocol.OpsObserverResponse
	failure  *browserprotocol.OpsObserverError
}

func (stub *observationCaptureStub) Observe(
	context.Context,
	browserprotocol.OpsObserverOperation,
) (browserprotocol.OpsObserverResponse, *browserprotocol.OpsObserverError) {
	stub.mu.Lock()
	defer stub.mu.Unlock()
	stub.calls++
	if len(stub.responses) == 0 {
		return browserprotocol.OpsObserverResponse{}, browserprotocol.NewOpsObserverError(
			browserprotocol.OpsObserverRunNotActive,
			"not bound",
		)
	}
	reply := stub.responses[0]
	if len(stub.responses) > 1 {
		stub.responses = stub.responses[1:]
	}
	return reply.response, reply.failure
}

func (stub *observationCaptureStub) observed() int {
	stub.mu.Lock()
	defer stub.mu.Unlock()
	return stub.calls
}

// observationLocalAttempt is a lease whose identity the test command names.
func observationLocalAttempt(
	command *browserextension.ObserverBridgeCommand,
) *browserRunLease {
	identity := browserprotocol.Identity{
		RunID:            command.AttemptIdentity.RunID,
		SessionEpoch:     command.SessionEpoch,
		BrowserSessionID: "browser-session-a",
		AttachmentID:     "attachment-a",
	}
	command.BrowserSessionSHA256 = browserIdentityEvidenceSHA256(
		browserSessionEvidenceDomain, identity.BrowserSessionID,
	)
	command.AttachmentSHA256 = browserIdentityEvidenceSHA256(
		browserAttachmentEvidenceDomain, identity.AttachmentID,
	)
	return &browserRunLease{identity: identity}
}

// A single attempt is not enough for the last frame of a round: the Agent's final
// action is usually still settling, and the Engine answers that with a transient
// busy or not-yet-bound. The final capture has to keep asking inside its window,
// and it must still give up rather than hold the attachment open.
func TestObservationFinalCaptureRetriesWithinItsWindow(t *testing.T) {
	t.Parallel()
	command := observationCommand(browserextension.ObserverBridgeStart)
	observation := newBrowserObservation(nil, nil)
	observation.lease = observationLocalAttempt(&command)
	observation.finalCaptureWindow = 120 * time.Millisecond
	stub := &observationCaptureStub{}

	if outcome := observation.captureFinal(t.Context(), command, stub); outcome != observationCaptureRetry {
		t.Fatalf("outcome = %v, want a retry that gave up inside its window", outcome)
	}
	if stub.observed() < 2 {
		t.Fatalf("the Engine was asked %d times; a single attempt is the bug this fixes", stub.observed())
	}
}

// A failure that is not the Engine waiting has to end the observation on the
// first answer, exactly as the interval capture does. Retrying an internal or
// protocol failure would hold a broken stream open through teardown.
func TestObservationFinalCaptureDoesNotRetryAHardFailure(t *testing.T) {
	t.Parallel()
	command := observationCommand(browserextension.ObserverBridgeStart)
	observation := newBrowserObservation(nil, nil)
	observation.lease = observationLocalAttempt(&command)
	observation.finalCaptureWindow = time.Second
	stub := &observationCaptureStub{responses: []observationCaptureStubReply{{
		failure: browserprotocol.NewOpsObserverError(
			browserprotocol.OpsObserverInternalError, "broken",
		),
	}}}

	if outcome := observation.captureFinal(t.Context(), command, stub); outcome != observationCaptureEnded {
		t.Fatalf("outcome = %v, want the stream to end", outcome)
	}
	if stub.observed() != 1 {
		t.Fatalf("a hard failure was retried %d times", stub.observed())
	}
}

// Teardown asks the streaming goroutine for the frame and waits for it, because
// the attachment closes immediately afterwards. Every way that can fail has to
// stay bounded: no observation, and a stream that is not at its select.
func TestObservationFinalFrameRequestIsBoundedAndRendezvous(t *testing.T) {
	t.Parallel()
	command := observationCommand(browserextension.ObserverBridgeStart)

	idle := newBrowserObservation(nil, nil)
	idle.finalCaptureDeadline = time.Hour
	done := make(chan struct{})
	go func() {
		idle.captureFinalFrame(t.Context())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a Run nobody observed waited for a final frame anyway")
	}

	unresponsive := newBrowserObservation(nil, nil)
	unresponsive.finalCaptureDeadline = 50 * time.Millisecond
	unresponsive.mu.Lock()
	unresponsive.leaseID = command.LeaseID
	unresponsive.mu.Unlock()
	start := time.Now()
	unresponsive.captureFinalFrame(t.Context())
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("teardown waited %s on a stream that was not listening", elapsed)
	}

	// And the rendezvous itself: the stream goroutine receives the request and
	// teardown returns only once that capture has settled.
	observation := newBrowserObservation(nil, nil)
	observation.lease = observationLocalAttempt(&command)
	observation.finalCaptureWindow = 50 * time.Millisecond
	observation.mu.Lock()
	observation.leaseID = command.LeaseID
	observation.mu.Unlock()
	stub := &observationCaptureStub{}
	captured := make(chan observationCaptureOutcome, 1)
	go func() {
		ack := <-observation.final
		captured <- observation.captureFinal(t.Context(), command, stub)
		close(ack)
	}()
	observation.captureFinalFrame(t.Context())
	select {
	case outcome := <-captured:
		if outcome != observationCaptureRetry {
			t.Fatalf("outcome = %v", outcome)
		}
	default:
		t.Fatal("teardown returned before the final capture settled")
	}
	if stub.observed() == 0 {
		t.Fatal("teardown never reached the Engine")
	}
}

// Core cannot tell the round's last frame from a mid-round one by the order events
// reach it: teardown cuts this very stream, so the ending usually arrives as an
// error or a stop before the Run is terminal. The teardown capture therefore says
// so on the frame, and an interval capture must not.
//
// But only for a Core that declared it accepts the marker. An older Core rejects
// the unknown field as a validation failure, which on the Runtime WebSocket closes
// the whole connection -- at the end of every observed round, just before the
// result is reported. For that Core the frame is still delivered, unmarked.
func TestObservationMarksOnlyTheTeardownCaptureForACoreThatAcceptsIt(t *testing.T) {
	t.Parallel()
	for name, expect := range map[string]struct {
		accepts bool
		marked  []bool
	}{
		"Core declares the marker": {true, []bool{false, true}},
		"Core predates the marker": {false, []bool{false, false}},
	} {
		t.Run(name, func(t *testing.T) {
			command := observationCommand(browserextension.ObserverBridgeStart)
			command.AcceptsFinalFrame = expect.accepts
			observation := newBrowserObservation(nil, nil)
			observation.lease = observationLocalAttempt(&command)
			observation.finalCaptureWindow = 50 * time.Millisecond

			var marked []bool
			observation.mu.Lock()
			observation.leaseID = command.LeaseID
			observation.mu.Unlock()
			// Delivery is replaced so the marker can be read at the capture
			// boundary without a Runtime extension channel.
			observation.publish = func(
				_ context.Context,
				_ browserextension.ObserverBridgeCommand,
				event browserextension.ObserverBridgeEvent,
			) bool {
				marked = append(marked, event.FinalFrame)
				return true
			}
			frame := browserprotocol.OpsObserverResponse{
				Observation: &browserprotocol.OpsObserverObservation{
					RunID:            command.AttemptIdentity.RunID,
					SessionEpoch:     command.SessionEpoch,
					AttachmentSHA256: capturedAttachmentDigest(observation.lease.identity),
					Frame: &browserprotocol.ViewerFrame{
						MIMEType: "image/jpeg",
						Data:     []byte{0xff, 0xd8, 0xff, 0xd9},
						Width:    1280,
						Height:   720,
					},
				},
			}
			stub := &observationCaptureStub{
				responses: []observationCaptureStubReply{{response: frame}},
			}

			if outcome := observation.capture(t.Context(), command, stub); outcome != observationCaptureDelivered {
				t.Fatalf("interval capture outcome = %v", outcome)
			}
			// The teardown capture is made and delivered either way: it is the
			// frame the round ended on. Only the marker depends on the Core.
			if outcome := observation.captureFinal(t.Context(), command, stub); outcome != observationCaptureDelivered {
				t.Fatalf("final capture outcome = %v", outcome)
			}
			if len(marked) != len(expect.marked) ||
				marked[0] != expect.marked[0] || marked[1] != expect.marked[1] {
				t.Fatalf("final-frame markers = %v, want %v", marked, expect.marked)
			}
		})
	}
}
