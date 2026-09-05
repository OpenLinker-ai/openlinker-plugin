package browserextension

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	openlinker "github.com/OpenLinker-ai/openlinker-go"
	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserprotocol"
)

func validViewerFrame() *browserprotocol.ViewerFrame {
	return &browserprotocol.ViewerFrame{
		MIMEType: "image/jpeg",
		Data:     []byte{0xff, 0xd8, 0xff, 0xd9},
		Width:    browserprotocol.BrowserViewportWidth,
		Height:   browserprotocol.BrowserViewportHeight,
	}
}

func bridgeIdentity() ObserverBridgeIdentity {
	return ObserverBridgeIdentity{
		SessionEpoch:         3,
		BrowserSessionSHA256: strings.Repeat("a", 64),
		AttachmentSHA256:     strings.Repeat("b", 64),
	}
}

func bridgeAttemptIdentity() openlinker.RuntimeAttemptIdentity {
	return openlinker.RuntimeAttemptIdentity{
		RunID:            "11111111-1111-4111-8111-111111111111",
		AttemptID:        "22222222-2222-4222-8222-222222222222",
		LeaseID:          "33333333-3333-4333-8333-333333333333",
		FencingToken:     7,
		NodeID:           "44444444-4444-4444-8444-444444444444",
		AgentID:          "55555555-5555-4555-8555-555555555555",
		WorkerID:         "99999999-9999-4999-8999-999999999999",
		RuntimeSessionID: "66666666-6666-4666-8666-666666666666",
	}
}

func bridgeEvent(kind ObserverBridgeEventKind) ObserverBridgeEvent {
	captured := time.Now().UTC()
	event := ObserverBridgeEvent{
		AttemptIdentity:      bridgeAttemptIdentity(),
		SessionEpoch:         bridgeIdentity().SessionEpoch,
		BrowserSessionSHA256: bridgeIdentity().BrowserSessionSHA256,
		AttachmentSHA256:     bridgeIdentity().AttachmentSHA256,
		CommandID:            "77777777-7777-4777-8777-777777777777",
		LeaseID:              "88888888-8888-4888-8888-888888888888",
		EventSeq:             1,
		Kind:                 kind,
	}
	switch kind {
	case ObserverBridgeFrame:
		event.CapturedAt = &captured
		event.Frame = validViewerFrame()
	case ObserverBridgeError:
		event.ErrorCode = string(browserprotocol.OpsObserverProtocolError)
	}
	return event
}

// Any identity drift means the Runtime moved on between the command and the
// capture, so the frame belongs to a different Run and must not be attributed to
// this observation.
func TestObserverBridgeIdentityRejectsEveryFieldDrift(t *testing.T) {
	t.Parallel()
	base := bridgeIdentity()
	for name, mutate := range map[string]func(*ObserverBridgeIdentity){
		"epoch":      func(i *ObserverBridgeIdentity) { i.SessionEpoch = base.SessionEpoch + 1 },
		"attachment": func(i *ObserverBridgeIdentity) { i.AttachmentSHA256 = strings.Repeat("c", 64) },
		"session":    func(i *ObserverBridgeIdentity) { i.BrowserSessionSHA256 = strings.Repeat("d", 64) },
	} {
		t.Run(name, func(t *testing.T) {
			drifted := base
			mutate(&drifted)
			if base.Equal(drifted) {
				t.Fatalf("%s drift was treated as the same identity", name)
			}
		})
	}
	if !base.Equal(bridgeIdentity()) {
		t.Fatal("an unchanged identity must compare equal")
	}
}

func TestObserverBridgeCommandValidation(t *testing.T) {
	t.Parallel()
	valid := ObserverBridgeCommand{
		AttemptIdentity:      bridgeAttemptIdentity(),
		SessionEpoch:         bridgeIdentity().SessionEpoch,
		BrowserSessionSHA256: bridgeIdentity().BrowserSessionSHA256,
		AttachmentSHA256:     bridgeIdentity().AttachmentSHA256,
		CommandID:            "77777777-7777-4777-8777-777777777777",
		Action:               ObserverBridgeStart,
		LeaseID:              "88888888-8888-4888-8888-888888888888",
		LeaseExpiresAt:       time.Now().UTC().Add(time.Minute),
		DeadlineAt:           time.Now().UTC().Add(time.Minute),
		FrameIntervalMS:      ObserverBridgeDefaultFrameIntervalMS,
	}
	if failure := valid.Validate(); failure != nil {
		t.Fatalf("valid start command rejected: %v", failure)
	}

	unbounded := valid
	unbounded.DeadlineAt = time.Time{}
	if unbounded.Validate() == nil {
		t.Fatal("a start without a deadline must be refused")
	}
	for _, interval := range []int{
		ObserverBridgeMinFrameIntervalMS - 1,
		ObserverBridgeMaxFrameIntervalMS + 1,
	} {
		outOfRange := valid
		outOfRange.FrameIntervalMS = interval
		if outOfRange.Validate() == nil {
			t.Fatalf("frame interval %d must be refused", interval)
		}
	}

	// Stop carries no schedule, so it must not inherit the start requirements.
	stop := valid
	stop.Action = ObserverBridgeStop
	stop.LeaseExpiresAt = time.Time{}
	stop.DeadlineAt = time.Time{}
	stop.FrameIntervalMS = 0
	if failure := stop.Validate(); failure != nil {
		t.Fatalf("stop command rejected: %v", failure)
	}
}

// The SDK owns the reserved attempt_identity field and strictly decodes it as
// RuntimeAttemptIdentity before the extension handler sees the command. Browser
// evidence therefore has to live beside it, not replace it with a lookalike.
func TestObserverBridgeCommandKeepsSDKAttemptIdentityAtReservedField(t *testing.T) {
	t.Parallel()
	command := ObserverBridgeCommand{
		AttemptIdentity:      bridgeAttemptIdentity(),
		SessionEpoch:         bridgeIdentity().SessionEpoch,
		BrowserSessionSHA256: bridgeIdentity().BrowserSessionSHA256,
		AttachmentSHA256:     bridgeIdentity().AttachmentSHA256,
		CommandID:            "77777777-7777-4777-8777-777777777777",
		Action:               ObserverBridgeStart,
		LeaseID:              "88888888-8888-4888-8888-888888888888",
		LeaseExpiresAt:       time.Now().UTC().Add(time.Minute),
		DeadlineAt:           time.Now().UTC().Add(time.Minute),
		FrameIntervalMS:      ObserverBridgeDefaultFrameIntervalMS,
	}
	payload, err := json.Marshal(command)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(payload, &object); err != nil {
		t.Fatal(err)
	}
	var decoded openlinker.RuntimeAttemptIdentity
	decoder := json.NewDecoder(bytes.NewReader(object["attempt_identity"]))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		t.Fatalf("SDK attempt identity rejected: %v", err)
	}
	if decoded != command.AttemptIdentity {
		t.Fatalf("SDK attempt identity changed: %#v", decoded)
	}
}

func TestObserverBridgeEventValidationPerKind(t *testing.T) {
	t.Parallel()
	for _, kind := range []ObserverBridgeEventKind{
		ObserverBridgeStarted, ObserverBridgeFrame, ObserverBridgeStopped, ObserverBridgeError,
	} {
		if failure := bridgeEvent(kind).Validate(); failure != nil {
			t.Fatalf("%s event rejected: %v", kind, failure)
		}
	}

	// A lifecycle event must stay empty; otherwise a frame could ride a kind the
	// consumer does not inspect for content.
	loaded := bridgeEvent(ObserverBridgeStarted)
	loaded.Frame = validViewerFrame()
	if loaded.Validate() == nil {
		t.Fatal("a started event carrying a frame must be refused")
	}

	incomplete := bridgeEvent(ObserverBridgeFrame)
	incomplete.CapturedAt = nil
	if incomplete.Validate() == nil {
		t.Fatal("a frame event without a capture time must be refused")
	}

	silent := bridgeEvent(ObserverBridgeError)
	silent.ErrorCode = ""
	if silent.Validate() == nil {
		t.Fatal("an error event without an error must be refused")
	}
}

// The window is a single unacknowledged event, so an ack that does not name the
// exact lease and sequence must not settle it.
func TestObserverBridgeAckMustNameTheExactEvent(t *testing.T) {
	t.Parallel()
	event := bridgeEvent(ObserverBridgeFrame)
	exact := ObserverBridgeEventAck{
		AttemptIdentity:      event.AttemptIdentity,
		SessionEpoch:         event.SessionEpoch,
		BrowserSessionSHA256: event.BrowserSessionSHA256,
		AttachmentSHA256:     event.AttachmentSHA256,
		LeaseID:              event.LeaseID,
		EventSeq:             event.EventSeq,
	}
	if !exact.Matches(event) {
		t.Fatal("the exact ack did not settle its event")
	}
	for name, mutate := range map[string]func(*ObserverBridgeEventAck){
		"stale sequence": func(a *ObserverBridgeEventAck) { a.EventSeq = event.EventSeq + 1 },
		"other lease":    func(a *ObserverBridgeEventAck) { a.LeaseID = "99999999-9999-4999-8999-999999999999" },
		"other attempt":  func(a *ObserverBridgeEventAck) { a.AttemptIdentity.AttemptID = "99999999-9999-4999-8999-999999999999" },
		"other browser":  func(a *ObserverBridgeEventAck) { a.SessionEpoch++ },
	} {
		t.Run(name, func(t *testing.T) {
			ack := exact
			mutate(&ack)
			if ack.Matches(event) {
				t.Fatalf("%s settled an event it does not name", name)
			}
		})
	}
}
