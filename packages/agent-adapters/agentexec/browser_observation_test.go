package agentexec

import (
	"encoding/json"
	"strings"
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
