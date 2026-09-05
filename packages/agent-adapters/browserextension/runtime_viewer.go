package browserextension

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"time"

	openlinker "github.com/OpenLinker-ai/openlinker-go"
	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserprotocol"
)

const (
	RuntimeViewerCommandMessage  openlinker.RuntimeMessageType = "browser.viewer.command"
	RuntimeViewerFrameMessage    openlinker.RuntimeMessageType = "browser.viewer.frame"
	RuntimeViewerFrameAckMessage openlinker.RuntimeMessageType = "browser.viewer.frame.ack"
)

var RuntimeViewerExtensionRoute = openlinker.RuntimeExtensionRoute{
	CommandType: RuntimeViewerCommandMessage,
	RequestType: RuntimeViewerFrameMessage,
	ReplyType:   RuntimeViewerFrameAckMessage,
}

type RuntimeViewerAction string

const (
	RuntimeViewerClaim     RuntimeViewerAction = "claim"
	RuntimeViewerRelease   RuntimeViewerAction = "release"
	RuntimeViewerResume    RuntimeViewerAction = "resume"
	RuntimeViewerTerminate RuntimeViewerAction = "terminate"
	RuntimeViewerInput     RuntimeViewerAction = "input"
)

type RuntimeViewerCommand struct {
	AttemptIdentity      openlinker.RuntimeAttemptIdentity `json:"attempt_identity"`
	Action               RuntimeViewerAction               `json:"action"`
	BrowserSessionID     string                            `json:"browser_session_id"`
	SessionEpoch         uint64                            `json:"session_epoch"`
	AttachmentID         string                            `json:"attachment_id"`
	PreviousControlEpoch uint64                            `json:"previous_control_epoch"`
	ControlEpoch         uint64                            `json:"control_epoch"`
	Input                *browserprotocol.ViewerInput      `json:"input,omitempty"`
	DeadlineAt           time.Time                         `json:"deadline_at"`
}

type RuntimeViewerFrame struct {
	AttemptIdentity  openlinker.RuntimeAttemptIdentity `json:"attempt_identity"`
	BrowserSessionID string                            `json:"browser_session_id"`
	SessionEpoch     uint64                            `json:"session_epoch"`
	AttachmentID     string                            `json:"attachment_id"`
	ControlEpoch     uint64                            `json:"control_epoch"`
	FrameSeq         uint64                            `json:"frame_seq"`
	MIMEType         string                            `json:"mime_type"`
	Data             []byte                            `json:"data"`
	Width            int                               `json:"width"`
	Height           int                               `json:"height"`
}

type RuntimeViewerFrameAck struct {
	AttemptIdentity openlinker.RuntimeAttemptIdentity `json:"attempt_identity"`
	ControlEpoch    uint64                            `json:"control_epoch"`
	FrameSeq        uint64                            `json:"frame_seq"`
}

func DecodeRuntimeViewerCommand(raw []byte) (RuntimeViewerCommand, error) {
	var command RuntimeViewerCommand
	if err := decodeStrictRuntimeViewerJSON(raw, &command); err != nil {
		return RuntimeViewerCommand{}, err
	}
	if err := command.Validate(); err != nil {
		return RuntimeViewerCommand{}, err
	}
	return command, nil
}

func (command RuntimeViewerCommand) Validate() error {
	if !validRuntimeAttemptIdentity(command.AttemptIdentity) ||
		!browserprotocol.IsValidUUID(command.BrowserSessionID) ||
		!browserprotocol.IsValidUUID(command.AttachmentID) ||
		command.SessionEpoch == 0 ||
		command.PreviousControlEpoch == 0 ||
		command.ControlEpoch == 0 ||
		command.ControlEpoch < command.PreviousControlEpoch ||
		command.DeadlineAt.IsZero() {
		return errors.New("browser Viewer command identity is invalid")
	}
	switch command.Action {
	case RuntimeViewerClaim,
		RuntimeViewerRelease,
		RuntimeViewerResume,
		RuntimeViewerTerminate:
		if command.Input != nil ||
			command.ControlEpoch != command.PreviousControlEpoch+1 {
			return errors.New("browser Viewer control transition is invalid")
		}
	case RuntimeViewerInput:
		if command.Input == nil ||
			command.ControlEpoch != command.PreviousControlEpoch {
			return errors.New("browser Viewer input transition is invalid")
		}
		if failure := command.Input.Validate(); failure != nil {
			return failure
		}
	default:
		return errors.New("browser Viewer command action is invalid")
	}
	return nil
}

func EncodeRuntimeViewerFrame(frame RuntimeViewerFrame) ([]byte, error) {
	if err := frame.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(frame)
}

func (frame RuntimeViewerFrame) Validate() error {
	if !validRuntimeAttemptIdentity(frame.AttemptIdentity) ||
		!browserprotocol.IsValidUUID(frame.BrowserSessionID) ||
		!browserprotocol.IsValidUUID(frame.AttachmentID) ||
		frame.SessionEpoch == 0 ||
		frame.ControlEpoch == 0 ||
		frame.FrameSeq == 0 {
		return errors.New("browser Viewer frame identity is invalid")
	}
	if failure := (browserprotocol.ViewerFrame{
		MIMEType: frame.MIMEType,
		Data:     frame.Data,
		Width:    frame.Width,
		Height:   frame.Height,
	}).Validate(); failure != nil {
		return failure
	}
	return nil
}

func DecodeRuntimeViewerFrameAck(raw []byte) (RuntimeViewerFrameAck, error) {
	var ack RuntimeViewerFrameAck
	if err := decodeStrictRuntimeViewerJSON(raw, &ack); err != nil {
		return RuntimeViewerFrameAck{}, err
	}
	if !validRuntimeAttemptIdentity(ack.AttemptIdentity) ||
		ack.ControlEpoch == 0 ||
		ack.FrameSeq == 0 {
		return RuntimeViewerFrameAck{}, errors.New(
			"browser Viewer frame acknowledgement is invalid",
		)
	}
	return ack, nil
}

func validRuntimeAttemptIdentity(
	identity openlinker.RuntimeAttemptIdentity,
) bool {
	return browserprotocol.IsValidUUID(identity.RunID) &&
		browserprotocol.IsValidUUID(identity.AttemptID) &&
		browserprotocol.IsValidUUID(identity.LeaseID) &&
		identity.FencingToken > 0 &&
		browserprotocol.IsValidUUID(identity.NodeID) &&
		browserprotocol.IsValidUUID(identity.AgentID) &&
		browserprotocol.IsValidUUID(identity.WorkerID) &&
		browserprotocol.IsValidUUID(identity.RuntimeSessionID)
}

func decodeStrictRuntimeViewerJSON(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("unexpected trailing browser Viewer JSON value")
		}
		return err
	}
	return nil
}
