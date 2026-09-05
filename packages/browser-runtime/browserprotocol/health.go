package browserprotocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

const (
	HealthContractID       = "openlinker.browser.health.v1"
	MaxHealthRequestBytes  = 1024
	MaxHealthResponseBytes = 1024
)

type HealthReason string

const (
	HealthReasonInvalidRequest HealthReason = "invalid_request"
	HealthReasonUnauthorized   HealthReason = "unauthorized"
)

type HealthRequest struct {
	ContractID        string `json:"contract_id"`
	ChannelCredential string `json:"channel_credential"`
	RequestID         string `json:"request_id"`
}

type HealthResponse struct {
	ContractID string       `json:"contract_id"`
	RequestID  string       `json:"request_id,omitempty"`
	Status     string       `json:"status"`
	Reason     HealthReason `json:"reason,omitempty"`
}

func (request HealthRequest) Validate() error {
	if request.ContractID != HealthContractID {
		return errors.New("unsupported browser health contract")
	}
	if !IsValidUUID(request.RequestID) {
		return errors.New("browser health request_id must be a UUID")
	}
	return nil
}

func (response HealthResponse) Validate(requestID string) error {
	if response.ContractID != HealthContractID || response.RequestID != requestID {
		return errors.New("browser health response identity is invalid")
	}
	switch response.Status {
	case "ok":
		if response.Reason != "" {
			return errors.New("browser health success response is invalid")
		}
	case "error":
		if response.Reason != HealthReasonInvalidRequest &&
			response.Reason != HealthReasonUnauthorized {
			return errors.New("browser health error response is invalid")
		}
	default:
		return errors.New("browser health response status is invalid")
	}
	return nil
}

func HealthSuccessResponse(requestID string) HealthResponse {
	return HealthResponse{
		ContractID: HealthContractID,
		RequestID:  requestID,
		Status:     "ok",
	}
}

func HealthErrorResponse(requestID string, reason HealthReason) HealthResponse {
	return HealthResponse{
		ContractID: HealthContractID,
		RequestID:  boundedString(requestID, 128),
		Status:     "error",
		Reason:     reason,
	}
}

func DecodeHealthRequest(raw []byte) (HealthRequest, error) {
	var request HealthRequest
	if err := decodeHealthJSON(raw, &request); err != nil {
		return HealthRequest{}, err
	}
	return request, nil
}

func DecodeHealthResponse(raw []byte) (HealthResponse, error) {
	var response HealthResponse
	if err := decodeHealthJSON(raw, &response); err != nil {
		return HealthResponse{}, err
	}
	return response, nil
}

func decodeHealthJSON(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("unexpected trailing browser health JSON value")
		}
		return err
	}
	return nil
}
