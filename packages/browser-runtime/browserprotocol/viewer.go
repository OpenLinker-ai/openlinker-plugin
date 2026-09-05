package browserprotocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"strings"
	"time"
)

const ViewerContractID = "openlinker.browser.viewer.v1"

type ViewerOperation string

const (
	ViewerOperationEnter ViewerOperation = "enter"
	ViewerOperationExit  ViewerOperation = "exit"
	ViewerOperationFrame ViewerOperation = "frame"
	ViewerOperationInput ViewerOperation = "input"
)

type ViewerInputKind string

const (
	ViewerInputPointer  ViewerInputKind = "pointer"
	ViewerInputKeyboard ViewerInputKind = "keyboard"
	ViewerInputScroll   ViewerInputKind = "scroll"
)

type ViewerPointerAction string

const (
	ViewerPointerMove  ViewerPointerAction = "move"
	ViewerPointerClick ViewerPointerAction = "click"
)

type ViewerKeyboardAction string

const (
	ViewerKeyboardPress ViewerKeyboardAction = "press"
	ViewerKeyboardText  ViewerKeyboardAction = "text"
)

type ViewerRequest struct {
	ContractID        string          `json:"contract_id"`
	ChannelCredential string          `json:"channel_credential"`
	RequestID         string          `json:"request_id"`
	Deadline          time.Time       `json:"deadline"`
	Identity          Identity        `json:"identity"`
	Operation         ViewerOperation `json:"operation"`
	Input             *ViewerInput    `json:"input,omitempty"`
}

type ViewerInput struct {
	Kind           ViewerInputKind      `json:"kind"`
	PointerAction  ViewerPointerAction  `json:"pointer_action,omitempty"`
	KeyboardAction ViewerKeyboardAction `json:"keyboard_action,omitempty"`
	X              *int                 `json:"x,omitempty"`
	Y              *int                 `json:"y,omitempty"`
	Button         string               `json:"button,omitempty"`
	ClickCount     int                  `json:"click_count,omitempty"`
	Key            string               `json:"key,omitempty"`
	Text           string               `json:"text,omitempty"`
	DeltaX         float64              `json:"delta_x,omitempty"`
	DeltaY         float64              `json:"delta_y,omitempty"`
}

type ViewerFrame struct {
	MIMEType string `json:"mime_type"`
	Data     []byte `json:"data"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
}

type ViewerResponse struct {
	ContractID string       `json:"contract_id"`
	RequestID  string       `json:"request_id,omitempty"`
	Status     string       `json:"status"`
	Frame      *ViewerFrame `json:"frame,omitempty"`
	Error      *Failure     `json:"error,omitempty"`
}

func (request ViewerRequest) Validate(now time.Time) *Failure {
	if request.ContractID != ViewerContractID {
		return NewFailure(ErrorProtocolInvalid, "unsupported browser viewer contract", false)
	}
	if !IsValidUUID(request.RequestID) {
		return NewFailure(ErrorProtocolInvalid, "request_id must be a UUID", false)
	}
	if failure := request.Identity.Validate(); failure != nil {
		return failure
	}
	if request.Identity.Controller != ControllerHuman {
		return NewFailure(
			ErrorStaleControlEpoch,
			"browser viewer request does not hold human control",
			false,
		)
	}
	now = now.UTC()
	if request.Deadline.IsZero() || !request.Deadline.After(now) {
		return NewFailure(ErrorDeadlineExceeded, "browser viewer deadline has elapsed", true)
	}
	if request.Deadline.After(now.Add(MaxActionDeadline)) {
		return NewFailure(
			ErrorProtocolInvalid,
			"browser viewer deadline exceeds the allowed horizon",
			false,
		)
	}
	switch request.Operation {
	case ViewerOperationEnter, ViewerOperationExit, ViewerOperationFrame:
		if request.Input != nil {
			return NewFailure(
				ErrorProtocolInvalid,
				"browser viewer operation contains unexpected input",
				false,
			)
		}
	case ViewerOperationInput:
		if request.Input == nil {
			return NewFailure(ErrorProtocolInvalid, "browser viewer input is missing", false)
		}
		if failure := request.Input.Validate(); failure != nil {
			return failure
		}
	default:
		return NewFailure(ErrorProtocolInvalid, "browser viewer operation is invalid", false)
	}
	return nil
}

func (input ViewerInput) Validate() *Failure {
	switch input.Kind {
	case ViewerInputPointer:
		return input.validatePointer()
	case ViewerInputKeyboard:
		return input.validateKeyboard()
	case ViewerInputScroll:
		return input.validateScroll()
	default:
		return NewFailure(ErrorProtocolInvalid, "browser viewer input kind is invalid", false)
	}
}

func (input ViewerInput) validatePointer() *Failure {
	if input.X == nil || input.Y == nil ||
		*input.X < 0 || *input.X >= BrowserViewportWidth ||
		*input.Y < 0 || *input.Y >= BrowserViewportHeight {
		return NewFailure(ErrorProtocolInvalid, "browser viewer pointer coordinates are invalid", false)
	}
	if input.KeyboardAction != "" || input.Key != "" || input.Text != "" ||
		input.DeltaX != 0 || input.DeltaY != 0 {
		return NewFailure(ErrorProtocolInvalid, "browser viewer pointer input is invalid", false)
	}
	switch input.PointerAction {
	case ViewerPointerMove:
		if input.Button != "" || input.ClickCount != 0 {
			return NewFailure(ErrorProtocolInvalid, "browser viewer pointer move is invalid", false)
		}
	case ViewerPointerClick:
		switch input.Button {
		case "left", "middle", "right":
		default:
			return NewFailure(ErrorProtocolInvalid, "browser viewer pointer button is invalid", false)
		}
		if input.ClickCount < 1 || input.ClickCount > 3 {
			return NewFailure(ErrorProtocolInvalid, "browser viewer click count is invalid", false)
		}
	default:
		return NewFailure(ErrorProtocolInvalid, "browser viewer pointer action is invalid", false)
	}
	return nil
}

func (input ViewerInput) validateKeyboard() *Failure {
	if input.PointerAction != "" || input.X != nil || input.Y != nil ||
		input.Button != "" || input.ClickCount != 0 ||
		input.DeltaX != 0 || input.DeltaY != 0 {
		return NewFailure(ErrorProtocolInvalid, "browser viewer keyboard input is invalid", false)
	}
	switch input.KeyboardAction {
	case ViewerKeyboardPress:
		if input.Key == "" || len(input.Key) > 64 || input.Text != "" {
			return NewFailure(ErrorProtocolInvalid, "browser viewer key press is invalid", false)
		}
	case ViewerKeyboardText:
		if input.Key != "" || input.Text == "" ||
			len(input.Text) > 4096 ||
			strings.ContainsRune(input.Text, '\x00') {
			return NewFailure(ErrorProtocolInvalid, "browser viewer text input is invalid", false)
		}
	default:
		return NewFailure(ErrorProtocolInvalid, "browser viewer keyboard action is invalid", false)
	}
	return nil
}

func (input ViewerInput) validateScroll() *Failure {
	if input.PointerAction != "" || input.KeyboardAction != "" ||
		input.X != nil || input.Y != nil || input.Button != "" ||
		input.ClickCount != 0 || input.Key != "" || input.Text != "" ||
		math.IsNaN(input.DeltaX) || math.IsInf(input.DeltaX, 0) ||
		math.IsNaN(input.DeltaY) || math.IsInf(input.DeltaY, 0) ||
		(math.Abs(input.DeltaX) > 4096) || (math.Abs(input.DeltaY) > 4096) ||
		(input.DeltaX == 0 && input.DeltaY == 0) {
		return NewFailure(ErrorProtocolInvalid, "browser viewer scroll input is invalid", false)
	}
	return nil
}

func (frame ViewerFrame) Validate() *Failure {
	if frame.MIMEType != "image/jpeg" ||
		len(frame.Data) == 0 ||
		len(frame.Data) > MaxViewerFrameBytes ||
		frame.Width != BrowserViewportWidth ||
		frame.Height != BrowserViewportHeight {
		return NewFailure(ErrorOutputInvalid, "browser viewer frame is invalid", false)
	}
	return nil
}

func ViewerSuccessResponse(requestID string, frame *ViewerFrame) ViewerResponse {
	return ViewerResponse{
		ContractID: ViewerContractID,
		RequestID:  requestID,
		Status:     "ok",
		Frame:      frame,
	}
}

func ViewerErrorResponse(requestID string, failure *Failure) ViewerResponse {
	if failure == nil {
		failure = NewFailure(ErrorInternal, "browser viewer failed", false)
	}
	return ViewerResponse{
		ContractID: ViewerContractID,
		RequestID:  boundedString(requestID, 128),
		Status:     "error",
		Error:      failure,
	}
}

func DecodeViewerRequest(raw []byte) (ViewerRequest, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var request ViewerRequest
	if err := decoder.Decode(&request); err != nil {
		return ViewerRequest{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return ViewerRequest{}, errors.New("unexpected trailing browser viewer JSON value")
		}
		return ViewerRequest{}, err
	}
	return request, nil
}
