package browserprotocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestHealthContractStrictValidation(t *testing.T) {
	t.Parallel()
	requestID := "11111111-1111-4111-8111-111111111111"
	request := HealthRequest{
		ContractID:        HealthContractID,
		ChannelCredential: strings.Repeat("a", 64),
		RequestID:         requestID,
	}
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeHealthRequest(raw)
	if err != nil || decoded.Validate() != nil {
		t.Fatalf("decoded request = %#v, err = %v", decoded, err)
	}
	if err := HealthSuccessResponse(requestID).Validate(requestID); err != nil {
		t.Fatal(err)
	}
	if err := HealthErrorResponse(requestID, HealthReasonUnauthorized).Validate(requestID); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range [][]byte{
		append(raw, []byte(` {}`)...),
		[]byte(`{"contract_id":"openlinker.browser.health.v1","request_id":"11111111-1111-4111-8111-111111111111","channel_credential":"value","unknown":true}`),
	} {
		if _, err := DecodeHealthRequest(invalid); err == nil {
			t.Fatalf("DecodeHealthRequest(%q) succeeded", invalid)
		}
	}
}
