package browserextension

import (
	"encoding/json"
	"testing"
	"time"

	openlinker "github.com/OpenLinker-ai/openlinker-go"
	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserprotocol"
)

func TestRuntimeViewerProtocolIsOwnedAndStrictlyValidatedByPlugin(t *testing.T) {
	command := validRuntimeViewerCommand()
	raw, err := json.Marshal(command)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeRuntimeViewerCommand(raw)
	if err != nil {
		t.Fatalf("decode valid Runtime Viewer command: %v", err)
	}
	if decoded.Action != RuntimeViewerClaim {
		t.Fatalf("decoded action = %q", decoded.Action)
	}

	var object map[string]any
	if err = json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	object["page_controlled"] = "must fail closed"
	raw, err = json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = DecodeRuntimeViewerCommand(raw); err == nil {
		t.Fatal("Runtime Viewer command accepted an unknown field")
	}

	command.AttemptIdentity.RunID = ""
	raw, err = json.Marshal(command)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = DecodeRuntimeViewerCommand(raw); err == nil {
		t.Fatal("Runtime Viewer command accepted an invalid Attempt identity")
	}
}

func TestRuntimeViewerFrameAckMustMatchPluginOwnedFrameIdentity(t *testing.T) {
	command := validRuntimeViewerCommand()
	frame := RuntimeViewerFrame{
		AttemptIdentity:  command.AttemptIdentity,
		BrowserSessionID: command.BrowserSessionID,
		SessionEpoch:     command.SessionEpoch,
		AttachmentID:     command.AttachmentID,
		ControlEpoch:     command.ControlEpoch,
		FrameSeq:         1,
		MIMEType:         "image/jpeg",
		Data:             []byte("jpeg"),
		Width:            browserprotocol.BrowserViewportWidth,
		Height:           browserprotocol.BrowserViewportHeight,
	}
	raw, err := EncodeRuntimeViewerFrame(frame)
	if err != nil {
		t.Fatal(err)
	}
	var encoded map[string]any
	if err = json.Unmarshal(raw, &encoded); err != nil {
		t.Fatal(err)
	}
	if encoded["mime_type"] != "image/jpeg" {
		t.Fatalf("encoded Runtime Viewer frame = %#v", encoded)
	}

	ackRaw, err := json.Marshal(RuntimeViewerFrameAck{
		AttemptIdentity: frame.AttemptIdentity,
		ControlEpoch:    frame.ControlEpoch,
		FrameSeq:        frame.FrameSeq,
	})
	if err != nil {
		t.Fatal(err)
	}
	ack, err := DecodeRuntimeViewerFrameAck(ackRaw)
	if err != nil {
		t.Fatal(err)
	}
	if ack.AttemptIdentity != frame.AttemptIdentity ||
		ack.ControlEpoch != frame.ControlEpoch ||
		ack.FrameSeq != frame.FrameSeq {
		t.Fatalf("Runtime Viewer frame ACK = %#v", ack)
	}
}

func validRuntimeViewerCommand() RuntimeViewerCommand {
	return RuntimeViewerCommand{
		AttemptIdentity: openlinker.RuntimeAttemptIdentity{
			RunID:            "11111111-1111-4111-8111-111111111111",
			AttemptID:        "22222222-2222-4222-8222-222222222222",
			LeaseID:          "33333333-3333-4333-8333-333333333333",
			FencingToken:     1,
			NodeID:           "44444444-4444-4444-8444-444444444444",
			AgentID:          "55555555-5555-4555-8555-555555555555",
			WorkerID:         "66666666-6666-4666-8666-666666666666",
			RuntimeSessionID: "77777777-7777-4777-8777-777777777777",
		},
		Action:               RuntimeViewerClaim,
		BrowserSessionID:     "88888888-8888-4888-8888-888888888888",
		SessionEpoch:         1,
		AttachmentID:         "99999999-9999-4999-8999-999999999999",
		PreviousControlEpoch: 1,
		ControlEpoch:         2,
		DeadlineAt:           time.Now().Add(time.Minute).UTC(),
	}
}
