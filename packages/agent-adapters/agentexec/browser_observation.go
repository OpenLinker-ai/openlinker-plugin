package agentexec

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	openlinker "github.com/OpenLinker-ai/openlinker-go"
	"github.com/OpenLinker-ai/openlinker-plugin/packages/agent-adapters/browserextension"
	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserclient"
	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserprotocol"
)

const (
	observationSocketEnvironment     = "OPENLINKER_BROWSER_OBSERVER_SOCKET"
	observationCredentialEnvironment = "OPENLINKER_BROWSER_OBSERVER_CREDENTIAL_FILE"
	defaultObservationSocket         = "/browser-control/openlinker.browser.observer.sock"
	defaultObservationCredential     = "/browser-control/observer-credential"
)

func observationSocketPath() string {
	if value := strings.TrimSpace(os.Getenv(observationSocketEnvironment)); value != "" {
		return value
	}
	return defaultObservationSocket
}

func observationCredentialPath() string {
	if value := strings.TrimSpace(os.Getenv(observationCredentialEnvironment)); value != "" {
		return value
	}
	return defaultObservationCredential
}

// browserObservation serves authenticated read-only observation.
//
// It keeps its own lease, lifecycle and state rather than sharing the
// human-control structures: observation and takeover are separate capabilities
// with separate authorization, and merging their state here is how "being able
// to watch" quietly becomes "being able to drive".
type browserObservation struct {
	lease      *browserRunLease
	extensions *openlinker.RuntimeExtensions

	mu        sync.Mutex
	leaseID   string
	commandID string
	cancel    context.CancelFunc
	eventSeq  uint64
}

func newBrowserObservation(
	lease *browserRunLease,
	extensions *openlinker.RuntimeExtensions,
) *browserObservation {
	return &browserObservation{lease: lease, extensions: extensions}
}

func (observation *browserObservation) handleCommand(
	parent context.Context,
	raw json.RawMessage,
	attemptIdentity openlinker.RuntimeAttemptIdentity,
) {
	if observation == nil {
		return
	}
	var command browserextension.ObserverBridgeCommand
	if err := json.Unmarshal(raw, &command); err != nil {
		return
	}
	if failure := command.Validate(); failure != nil {
		return
	}
	if command.AttemptIdentity != attemptIdentity {
		return
	}
	switch command.Action {
	case browserextension.ObserverBridgeStop:
		observation.stopLease(command.LeaseID)
	case browserextension.ObserverBridgeStart:
		observation.start(parent, command)
	}
}

func (observation *browserObservation) start(
	parent context.Context,
	command browserextension.ObserverBridgeCommand,
) {
	observation.mu.Lock()
	if observation.leaseID != "" {
		replay := observation.leaseID == command.LeaseID &&
			observation.commandID == command.CommandID
		observation.mu.Unlock()
		if replay {
			// Core can repeat the exact one-way command until it receives the
			// started event. The replay belongs to the stream already opening;
			// treating it as another observer would turn recovery into conflict.
			return
		}
		// Reported without the lease guard: emitEvent only publishes for the
		// lease it owns, so routing a busy refusal through it would drop the
		// very message telling Core the start failed, leaving a phantom active
		// record until its TTL.
		observation.emitUnguarded(command, browserprotocol.NewOpsObserverError(
			browserprotocol.OpsObserverAlreadyActive,
			"an observation is already active",
		))
		return
	}
	ctx, cancel := context.WithDeadline(parent, command.LeaseExpiresAt)
	observation.leaseID = command.LeaseID
	observation.commandID = command.CommandID
	observation.cancel = cancel
	observation.mu.Unlock()

	go observation.stream(ctx, command)
}

// stop ends the current observation. stopLease ends only the named one: a
// stream that exits late must not clear a lease a newer start already took, or
// the new observation would be torn down by its predecessor's teardown.
func (observation *browserObservation) stop() {
	observation.stopLease("")
}

func (observation *browserObservation) stopLease(leaseID string) {
	observation.mu.Lock()
	if leaseID != "" && observation.leaseID != leaseID {
		observation.mu.Unlock()
		return
	}
	cancel := observation.cancel
	observation.cancel = nil
	observation.leaseID = ""
	observation.commandID = ""
	observation.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// stream captures at the requested interval until the lease ends. Every frame is
// checked against the identity the command named: if the Runtime has moved to
// another Attempt in between, the frame belongs to a different Run and reporting
// it here would attribute another user's page to this observation.
func (observation *browserObservation) stream(
	ctx context.Context,
	command browserextension.ObserverBridgeCommand,
) {
	defer observation.stopLease(command.LeaseID)

	// Claiming the bridge lease is what makes the local Viewer and this
	// observation contend for the one Runtime lease. A refusal here means someone
	// is already watching, and it has to reach the caller rather than look like a
	// silent stall.
	stream, err := browserclient.NewOpsObserverStream(ctx, browserclient.OpsObserverConfig{
		SocketPath:     observationSocketPath(),
		CredentialFile: observationCredentialPath(),
		RunID:          command.AttemptIdentity.RunID,
		TTL:            time.Until(command.LeaseExpiresAt),
	})
	if err != nil {
		observation.emitError(command, browserprotocol.NewOpsObserverError(
			browserprotocol.OpsObserverDisabled,
			"the observation bridge is unavailable",
		))
		return
	}
	defer stream.Close()

	if !observation.emitEvent(ctx, command, browserextension.ObserverBridgeEvent{
		Kind: browserextension.ObserverBridgeStarted,
	}) {
		return
	}

	ticker := time.NewTicker(time.Duration(command.FrameIntervalMS) * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			// A best-effort stopped event; the lease is already closing either
			// way, so a failure to deliver it must not block teardown.
			observation.emitEvent(context.Background(), command, browserextension.ObserverBridgeEvent{
				Kind: browserextension.ObserverBridgeStopped,
			})
			return
		case <-ticker.C:
			identity, err := observation.lease.identitySnapshot()
			if err != nil {
				observation.emitError(command, browserprotocol.NewOpsObserverError(
					browserprotocol.OpsObserverRunNotActive,
					"the observed Run is no longer active",
				))
				return
			}
			if !commandNamesLocalAttempt(command, identity) {
				observation.emitError(command, browserprotocol.NewOpsObserverError(
					browserprotocol.OpsObserverRunNotActive,
					"the Runtime moved to another Attempt",
				))
				return
			}
			response, observeErr := stream.Observe(
				ctx,
				browserprotocol.OpsObserverFrameOperation,
			)
			if observeErr != nil {
				// Both answers are transient while the Worker's own Attempt
				// identity above remains live. Busy means the Engine is occupied
				// authorizing the observer. Run-not-active means the provider has
				// not entered its first Browser action yet, or is between actions;
				// a lease opened from the ready lifecycle must wait for that action
				// rather than close on its first 500ms tick. Attempt termination and
				// rotation still fail closed through identitySnapshot and
				// commandNamesLocalAttempt before this call.
				if observerEngineErrorIsTransient(observeErr) {
					continue
				}
				observation.emitError(command, observeErr)
				return
			}
			if response.Observation == nil || response.Observation.Frame == nil {
				continue
			}
			// The Runtime backfilled its own identity, so compare that rather
			// than the local snapshot: it is the identity the frame was actually
			// captured under.
			if capturedFrameIsForeign(identity, *response.Observation) {
				observation.emitError(command, browserprotocol.NewOpsObserverError(
					browserprotocol.OpsObserverRunNotActive,
					"the captured frame belongs to another Attempt",
				))
				return
			}
			captured := response.Observation.CapturedAt.UTC()
			if captured.IsZero() {
				captured = time.Now().UTC()
			}
			if !observation.emitEvent(ctx, command, browserextension.ObserverBridgeEvent{
				Kind:       browserextension.ObserverBridgeFrame,
				CapturedAt: &captured,
				Frame:      response.Observation.Frame,
			}) {
				return
			}
		}
	}
}

func observerEngineErrorIsTransient(observerErr *browserprotocol.OpsObserverError) bool {
	if observerErr == nil {
		return false
	}
	return observerErr.Code == browserprotocol.OpsObserverBusyError ||
		observerErr.Code == browserprotocol.OpsObserverRunNotActive
}

// capturedFrameIsForeign compares the identity the Runtime backfilled at capture
// time against the one the command named. That evidence is stronger than the
// Worker's own snapshot because it describes the Attempt the pixels came from.
// capturedFrameIsForeign compares the capture against the Worker's own live
// identity. The Runtime can rotate an attachment between the snapshot and the
// capture, so without the attachment digest a frame from the new one would be
// reported under the old.
func capturedFrameIsForeign(
	local browserprotocol.Identity,
	observed browserprotocol.OpsObserverObservation,
) bool {
	return observed.RunID != local.RunID ||
		observed.SessionEpoch != local.SessionEpoch ||
		observed.AttachmentSHA256 != capturedAttachmentDigest(local)
}

// capturedAttachmentDigest mirrors the Runtime's opsIdentitySHA256, which hashes
// the raw attachment with no domain prefix. The Worker holds the raw value
// locally, so it can derive this even though Core never sees it.
func capturedAttachmentDigest(identity browserprotocol.Identity) string {
	digest := sha256.Sum256([]byte(identity.AttachmentID))
	return hex.EncodeToString(digest[:])
}

// commandNamesLocalAttempt checks that the Attempt Core asked about is the one
// this Worker currently holds.
//
// Core knows only the domain-separated hashes the ready lifecycle event
// publishes, so the comparison is done in that space: the Worker rehashes its
// own identity the same way. This is the check that stops Core from starting an
// observation against an Attempt this Runtime has already moved on from.
func commandNamesLocalAttempt(
	command browserextension.ObserverBridgeCommand,
	local browserprotocol.Identity,
) bool {
	expected := browserextension.ObserverBridgeIdentity{
		SessionEpoch:         command.SessionEpoch,
		BrowserSessionSHA256: command.BrowserSessionSHA256,
		AttachmentSHA256:     command.AttachmentSHA256,
	}
	return command.AttemptIdentity.RunID == local.RunID &&
		expected.SessionEpoch == local.SessionEpoch &&
		expected.BrowserSessionSHA256 == browserIdentityEvidenceSHA256(
			browserSessionEvidenceDomain,
			local.BrowserSessionID,
		) &&
		expected.AttachmentSHA256 == browserIdentityEvidenceSHA256(
			browserAttachmentEvidenceDomain,
			local.AttachmentID,
		)
}

// emitUnguarded publishes an event that must reach Core even when this handler
// does not own the lease, which is the case for a busy refusal.
func (observation *browserObservation) emitUnguarded(
	command browserextension.ObserverBridgeCommand,
	failure *browserprotocol.OpsObserverError,
) {
	ctx, cancel := context.WithTimeout(context.Background(), browserprotocol.MaxOpsObserverDeadline)
	defer cancel()
	event := browserextension.ObserverBridgeEvent{
		AttemptIdentity:      command.AttemptIdentity,
		SessionEpoch:         command.SessionEpoch,
		BrowserSessionSHA256: command.BrowserSessionSHA256,
		AttachmentSHA256:     command.AttachmentSHA256,
		CommandID:            command.CommandID,
		LeaseID:              command.LeaseID,
		EventSeq:             1,
		Kind:                 browserextension.ObserverBridgeError,
		ErrorCode:            string(failure.Code),
	}
	if event.Validate() != nil {
		return
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return
	}
	_, _ = observation.extensions.Publish(ctx, openlinker.RuntimeExtensionRequest{
		Type:    browserextension.ObserverBridgeEventType,
		Payload: payload,
	})
}

// emitEvent publishes one event and waits for its ack. The window is a single
// unacknowledged event, so this blocks until the ack arrives or the lease ends;
// an ack timeout stops the lease rather than leaving a paused state nobody owns.
func (observation *browserObservation) emitEvent(
	ctx context.Context,
	command browserextension.ObserverBridgeCommand,
	event browserextension.ObserverBridgeEvent,
) bool {
	observation.mu.Lock()
	if observation.leaseID != command.LeaseID {
		observation.mu.Unlock()
		return false
	}
	observation.eventSeq++
	event.EventSeq = observation.eventSeq
	observation.mu.Unlock()

	event.AttemptIdentity = command.AttemptIdentity
	event.SessionEpoch = command.SessionEpoch
	event.BrowserSessionSHA256 = command.BrowserSessionSHA256
	event.AttachmentSHA256 = command.AttachmentSHA256
	event.CommandID = command.CommandID
	event.LeaseID = command.LeaseID
	if failure := event.Validate(); failure != nil {
		return false
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return false
	}
	reply, err := observation.extensions.Publish(ctx, openlinker.RuntimeExtensionRequest{
		Type:    browserextension.ObserverBridgeEventType,
		Payload: payload,
	})
	if err != nil {
		writeObservationDeliveryDiagnostic(event, err)
		return false
	}
	if reply == nil {
		writeObservationDeliveryDiagnostic(event, fmt.Errorf("missing acknowledgement"))
		return false
	}
	var ack browserextension.ObserverBridgeEventAck
	if err := json.Unmarshal(reply.Payload, &ack); err != nil {
		writeObservationDeliveryDiagnostic(event, fmt.Errorf("decode acknowledgement: %w", err))
		return false
	}
	if !ack.Matches(event) {
		writeObservationDeliveryDiagnostic(event, fmt.Errorf("acknowledgement identity mismatch"))
		return false
	}
	return true
}

func writeObservationDeliveryDiagnostic(
	event browserextension.ObserverBridgeEvent,
	err error,
) {
	frameBytes := 0
	if event.Frame != nil {
		frameBytes = len(event.Frame.Data)
	}
	_, _ = fmt.Fprintf(
		os.Stderr,
		"browser observation event delivery failed: kind=%s event_seq=%d frame_bytes=%d: %v\n",
		event.Kind,
		event.EventSeq,
		frameBytes,
		err,
	)
}

func (observation *browserObservation) emitError(
	command browserextension.ObserverBridgeCommand,
	failure *browserprotocol.OpsObserverError,
) {
	if failure == nil {
		failure = browserprotocol.NewOpsObserverError(
			browserprotocol.OpsObserverInternalError,
			"observation failed",
		)
	}
	ctx, cancel := context.WithTimeout(context.Background(), browserprotocol.MaxOpsObserverDeadline)
	defer cancel()
	observation.emitEvent(ctx, command, browserextension.ObserverBridgeEvent{
		Kind:      browserextension.ObserverBridgeError,
		ErrorCode: string(failure.Code),
	})
}
