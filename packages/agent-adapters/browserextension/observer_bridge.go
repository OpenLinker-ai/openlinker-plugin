package browserextension

import (
	"strings"
	"time"

	openlinker "github.com/OpenLinker-ai/openlinker-go"
	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserprotocol"
)

// The authenticated observation extension. A Runtime command has no reply
// channel, so every outcome the caller needs -- started, a frame, a normal stop
// and any error -- travels back on the event stream instead.
const (
	ObserverBridgeFeature = "browser_authenticated_observation.v1"

	ObserverBridgeCommandType  openlinker.RuntimeMessageType = "browser.observer.command"
	ObserverBridgeEventType    openlinker.RuntimeMessageType = "browser.observer.event"
	ObserverBridgeEventAckType openlinker.RuntimeMessageType = "browser.observer.event.ack"
)

type ObserverBridgeAction string

const (
	ObserverBridgeStart ObserverBridgeAction = "start"
	ObserverBridgeStop  ObserverBridgeAction = "stop"
)

type ObserverBridgeEventKind string

const (
	ObserverBridgeStarted ObserverBridgeEventKind = "started"
	ObserverBridgeFrame   ObserverBridgeEventKind = "frame"
	ObserverBridgeStopped ObserverBridgeEventKind = "stopped"
	ObserverBridgeError   ObserverBridgeEventKind = "error"
)

const (
	// A single unacknowledged event. A wider window would need a rule for the
	// half-drained state after a failure; one event means an unacknowledged
	// event is always the last one sent.
	ObserverBridgeWindow = 1
	// Matches the interval the existing local Viewer already renders at.
	ObserverBridgeDefaultFrameIntervalMS = 500
	ObserverBridgeMinFrameIntervalMS     = 100
	ObserverBridgeMaxFrameIntervalMS     = 5_000
)

// ObserverBridgeIdentity carries hashed Browser identity rather than raw IDs.
//
// Core only ever learns the hashed form: the ready lifecycle event publishes
// browser_session_sha256 and browser_attachment_sha256, never the underlying
// UUIDs. Sending raw IDs here would require Core to know something it is
// deliberately not told, and the Worker can verify a hash against its own local
// identity just as strictly.
type ObserverBridgeIdentity struct {
	SessionEpoch         uint64 `json:"session_epoch"`
	BrowserSessionSHA256 string `json:"browser_session_sha256"`
	AttachmentSHA256     string `json:"browser_attachment_sha256"`
}

func (identity ObserverBridgeIdentity) Equal(other ObserverBridgeIdentity) bool {
	return identity == other
}

func (identity ObserverBridgeIdentity) validate() bool {
	return identity.SessionEpoch > 0 &&
		browserprotocol.IsValidSHA256Hex(identity.BrowserSessionSHA256) &&
		browserprotocol.IsValidSHA256Hex(identity.AttachmentSHA256)
}

type ObserverBridgeCommand struct {
	AttemptIdentity      openlinker.RuntimeAttemptIdentity `json:"attempt_identity"`
	SessionEpoch         uint64                            `json:"session_epoch"`
	BrowserSessionSHA256 string                            `json:"browser_session_sha256"`
	AttachmentSHA256     string                            `json:"browser_attachment_sha256"`
	CommandID            string                            `json:"command_id"`
	Action               ObserverBridgeAction              `json:"action"`
	LeaseID              string                            `json:"lease_id"`
	LeaseExpiresAt       time.Time                         `json:"lease_expires_at"`
	DeadlineAt           time.Time                         `json:"deadline_at"`
	FrameIntervalMS      int                               `json:"frame_interval_ms"`
}

type ObserverBridgeEvent struct {
	AttemptIdentity      openlinker.RuntimeAttemptIdentity `json:"attempt_identity"`
	SessionEpoch         uint64                            `json:"session_epoch"`
	BrowserSessionSHA256 string                            `json:"browser_session_sha256"`
	AttachmentSHA256     string                            `json:"browser_attachment_sha256"`
	CommandID            string                            `json:"command_id"`
	LeaseID              string                            `json:"lease_id"`
	EventSeq             uint64                            `json:"event_seq"`
	Kind                 ObserverBridgeEventKind           `json:"kind"`
	CapturedAt           *time.Time                        `json:"captured_at,omitempty"`
	Frame                *browserprotocol.ViewerFrame      `json:"frame,omitempty"`
	// Only the code crosses the wire. The message is free text that could carry
	// local detail, and Core records the code as an end reason anyway.
	ErrorCode string `json:"error_code,omitempty"`
}

type ObserverBridgeEventAck struct {
	AttemptIdentity      openlinker.RuntimeAttemptIdentity `json:"attempt_identity"`
	SessionEpoch         uint64                            `json:"session_epoch"`
	BrowserSessionSHA256 string                            `json:"browser_session_sha256"`
	AttachmentSHA256     string                            `json:"browser_attachment_sha256"`
	LeaseID              string                            `json:"lease_id"`
	EventSeq             uint64                            `json:"event_seq"`
}

func (command ObserverBridgeCommand) browserIdentity() ObserverBridgeIdentity {
	return ObserverBridgeIdentity{
		SessionEpoch:         command.SessionEpoch,
		BrowserSessionSHA256: command.BrowserSessionSHA256,
		AttachmentSHA256:     command.AttachmentSHA256,
	}
}

func (event ObserverBridgeEvent) browserIdentity() ObserverBridgeIdentity {
	return ObserverBridgeIdentity{
		SessionEpoch:         event.SessionEpoch,
		BrowserSessionSHA256: event.BrowserSessionSHA256,
		AttachmentSHA256:     event.AttachmentSHA256,
	}
}

func (ack ObserverBridgeEventAck) browserIdentity() ObserverBridgeIdentity {
	return ObserverBridgeIdentity{
		SessionEpoch:         ack.SessionEpoch,
		BrowserSessionSHA256: ack.BrowserSessionSHA256,
		AttachmentSHA256:     ack.AttachmentSHA256,
	}
}

func (command ObserverBridgeCommand) Validate() *browserprotocol.OpsObserverError {
	if !validRuntimeAttemptIdentity(command.AttemptIdentity) ||
		!command.browserIdentity().validate() || !browserprotocol.IsValidUUID(command.CommandID) ||
		!browserprotocol.IsValidUUID(command.LeaseID) {
		return browserprotocol.NewOpsObserverError(browserprotocol.OpsObserverProtocolError, "Observer command identity is invalid")
	}
	switch command.Action {
	case ObserverBridgeStart:
		if command.LeaseExpiresAt.IsZero() || command.DeadlineAt.IsZero() {
			return browserprotocol.NewOpsObserverError(browserprotocol.OpsObserverProtocolError, "Observer start requires bounded deadlines")
		}
		if command.FrameIntervalMS < ObserverBridgeMinFrameIntervalMS ||
			command.FrameIntervalMS > ObserverBridgeMaxFrameIntervalMS {
			return browserprotocol.NewOpsObserverError(browserprotocol.OpsObserverProtocolError, "Observer frame interval is out of range")
		}
	case ObserverBridgeStop:
	default:
		return browserprotocol.NewOpsObserverError(browserprotocol.OpsObserverProtocolError, "Observer command action is invalid")
	}
	return nil
}

func (event ObserverBridgeEvent) Validate() *browserprotocol.OpsObserverError {
	if !validRuntimeAttemptIdentity(event.AttemptIdentity) ||
		!event.browserIdentity().validate() || !browserprotocol.IsValidUUID(event.CommandID) ||
		!browserprotocol.IsValidUUID(event.LeaseID) || event.EventSeq == 0 {
		return browserprotocol.NewOpsObserverError(browserprotocol.OpsObserverProtocolError, "Observer event identity is invalid")
	}
	switch event.Kind {
	case ObserverBridgeStarted, ObserverBridgeStopped:
		if event.Frame != nil || event.ErrorCode != "" {
			return browserprotocol.NewOpsObserverError(browserprotocol.OpsObserverProtocolError, "Observer lifecycle event carries a payload")
		}
	case ObserverBridgeFrame:
		if event.Frame == nil || event.CapturedAt == nil || event.CapturedAt.IsZero() {
			return browserprotocol.NewOpsObserverError(browserprotocol.OpsObserverProtocolError, "Observer frame event is incomplete")
		}
		if failure := event.Frame.Validate(); failure != nil {
			return browserprotocol.NewOpsObserverError(browserprotocol.OpsObserverInternalError, "Observer frame is invalid")
		}
	case ObserverBridgeError:
		if strings.TrimSpace(event.ErrorCode) == "" {
			return browserprotocol.NewOpsObserverError(browserprotocol.OpsObserverProtocolError, "Observer error event has no code")
		}
	default:
		return browserprotocol.NewOpsObserverError(browserprotocol.OpsObserverProtocolError, "Observer event kind is invalid")
	}
	return nil
}

// Matches reports whether an ack settles the event it claims to. The lease and
// sequence both have to line up, so an ack for a previous lease cannot advance
// the current window.
func (ack ObserverBridgeEventAck) Matches(event ObserverBridgeEvent) bool {
	return ack.LeaseID == event.LeaseID && ack.EventSeq == event.EventSeq &&
		ack.AttemptIdentity == event.AttemptIdentity &&
		ack.browserIdentity().Equal(event.browserIdentity())
}

// ObserverBridgeExtensionRoute registers the observation extension. The command
// is a one-way push, so outcomes ride the event request/reply pair rather than a
// command reply that does not exist.
var ObserverBridgeExtensionRoute = openlinker.RuntimeExtensionRoute{
	CommandType: ObserverBridgeCommandType,
	RequestType: ObserverBridgeEventType,
	ReplyType:   ObserverBridgeEventAckType,
}
