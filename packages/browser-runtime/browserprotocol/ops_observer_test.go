package browserprotocol

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestOpsObserverRequestIsStrictAndReadOnly(t *testing.T) {
	now := time.Date(2026, 8, 17, 1, 2, 3, 0, time.UTC)
	request := validOpsObserverRequest(now)
	if failure := request.Validate(now); failure != nil {
		t.Fatal(failure)
	}
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"input", "action", "url", "script", "controller", "control_epoch",
		"browser_session_id", "attachment_id", "interaction_policy",
	} {
		if _, ok := fields[forbidden]; ok {
			t.Fatalf("forbidden Ops Observer field %q is present", forbidden)
		}
	}
	fields["input"] = map[string]any{"kind": "keyboard", "text": "secret"}
	malformed, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeOpsObserverRequest(malformed); err == nil {
		t.Fatal("unknown input field was accepted")
	}
}

func TestOpsObserverRequestBoundsDeadlineAndLease(t *testing.T) {
	now := time.Date(2026, 8, 17, 1, 2, 3, 0, time.UTC)
	for name, mutate := range map[string]func(*OpsObserverRequest){
		"operation": func(request *OpsObserverRequest) { request.Operation = "input" },
		"deadline": func(request *OpsObserverRequest) {
			request.Deadline = now.Add(MaxOpsObserverDeadline + time.Nanosecond)
		},
		"expiry": func(request *OpsObserverRequest) {
			request.LeaseExpiresAt = now.Add(MaxOpsObserverTTL + time.Nanosecond)
		},
		"run": func(request *OpsObserverRequest) { request.RunID = "not-a-run" },
	} {
		t.Run(name, func(t *testing.T) {
			request := validOpsObserverRequest(now)
			mutate(&request)
			if failure := request.Validate(now); failure == nil {
				t.Fatal("invalid Ops Observer request was accepted")
			}
		})
	}
}

func TestOpsObserverObservationRejectsUnredactedURLAndInvalidFrame(t *testing.T) {
	observation := validOpsObservation()
	if failure := observation.Validate(OpsObserverStatusOperation); failure != nil {
		t.Fatal(failure)
	}
	observation.PageURL = "https://example.com/path?token=secret"
	if failure := observation.Validate(OpsObserverStatusOperation); failure == nil {
		t.Fatal("query-bearing Ops Observer URL was accepted")
	}
	observation = validOpsObservation()
	observation.Frame = &ViewerFrame{
		MIMEType: "image/png",
		Data:     []byte("not-a-jpeg"),
		Width:    BrowserViewportWidth,
		Height:   BrowserViewportHeight,
	}
	if failure := observation.Validate(OpsObserverFrameOperation); failure == nil {
		t.Fatal("invalid Ops Observer frame was accepted")
	}
	observation = validOpsObservation()
	observation.ControlEpoch = 0
	if failure := observation.Validate(OpsObserverStatusOperation); failure == nil {
		t.Fatal("zero control epoch was accepted")
	}
}

func TestOpsObserverResponseIsStrict(t *testing.T) {
	requestID := "11111111-1111-4111-8111-111111111111"
	response := OpsObserverSuccessResponse(requestID, validOpsObservation())
	raw, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeOpsObserverResponse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Status != "ok" || decoded.Observation == nil {
		t.Fatalf("decoded response = %#v", decoded)
	}
	if _, err := DecodeOpsObserverResponse(append(raw, []byte(" {}")...)); err == nil {
		t.Fatal("trailing Ops Observer response value was accepted")
	}
}

func TestOpsObserverProbeResponseIsContentFreeAndStrict(t *testing.T) {
	requestID := "11111111-1111-4111-8111-111111111111"
	response := OpsObserverProbeResponse(requestID)
	raw, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeOpsObserverProbeResponse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Status != "ok" || decoded.Observation != nil || decoded.Error != nil {
		t.Fatalf("decoded probe response = %#v", decoded)
	}
	if _, err := DecodeOpsObserverResponse(raw); err == nil {
		t.Fatal("content-free probe response was accepted as a normal observation")
	}
	if _, err := DecodeOpsObserverProbeResponse(append(raw, []byte(" {}")...)); err == nil {
		t.Fatal("trailing Ops Observer probe response value was accepted")
	}

	response.Observation = &OpsObserverObservation{}
	raw, err = json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeOpsObserverProbeResponse(raw); err == nil {
		t.Fatal("probe response carrying page content was accepted")
	}
}

func validOpsObserverRequest(now time.Time) OpsObserverRequest {
	return OpsObserverRequest{
		ContractID:        OpsObserverContractID,
		ChannelCredential: strings.Repeat("a", 64),
		RequestID:         "11111111-1111-4111-8111-111111111111",
		ObserverLeaseID:   "22222222-2222-4222-8222-222222222222",
		RunID:             "33333333-3333-4333-8333-333333333333",
		Operation:         OpsObserverStatusOperation,
		Deadline:          now.Add(time.Second),
		LeaseExpiresAt:    now.Add(DefaultOpsObserverTTL),
	}
}

func validOpsObservation() OpsObserverObservation {
	return OpsObserverObservation{
		RunID:                "33333333-3333-4333-8333-333333333333",
		Controller:           ControllerAgent,
		SessionEpoch:         1,
		ControlEpoch:         3,
		BrowserSessionSHA256: strings.Repeat("a", 64),
		AttachmentSHA256:     strings.Repeat("b", 64),
		SelectedBackend:      "official_chrome_extension",
		ProfileGeneration:    2,
		FrameSequence:        1,
		CapturedAt:           time.Date(2026, 8, 17, 1, 2, 3, 0, time.UTC),
		PageURL:              "https://example.com/path",
		PageTitle:            "Example",
	}
}
