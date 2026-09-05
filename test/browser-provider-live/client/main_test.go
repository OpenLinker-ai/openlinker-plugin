//go:build !windows

package main

import (
	"strings"
	"testing"
)

func TestReadSecretRejectsEmptyMultilineAndOversizedValues(t *testing.T) {
	if value, err := readSecret(strings.NewReader("provider-key\n")); err != nil || value != "provider-key" {
		t.Fatalf("valid secret = %q, %v", value, err)
	}
	for _, value := range []string{"", "first\nsecond", strings.Repeat("x", maxSecretBytes+1)} {
		if _, err := readSecret(strings.NewReader(value)); err == nil {
			t.Fatalf("invalid secret of length %d was accepted", len(value))
		}
	}
}

func TestValidateProviderEvidenceRequiresDirectAuthorityFields(t *testing.T) {
	digest := strings.Repeat("a", 64)
	output := map[string]any{
		"browser_execution_profile":             "isolated",
		"browser_tool":                          "browser_session",
		"browser_client_mode_selected":          "plugin_native",
		"browser_interaction_policy":            "full",
		"browser_mutation_origins_sha256":       digest,
		"browser_contract_id":                   browserprotocolContractForTest,
		"browser_interaction_policy_generation": int64(1),
	}
	if err := validateProviderEvidence(output, "plugin_native", digest); err != nil {
		t.Fatal(err)
	}
	delete(output, "browser_mutation_origins_sha256")
	if err := validateProviderEvidence(output, "plugin_native", digest); err == nil {
		t.Fatal("missing authority evidence was accepted")
	}
}

func TestLifecycleRecorderRequiresReadyAndSuccessfulClose(t *testing.T) {
	digest := strings.Repeat("b", 64)
	recorder := &lifecycleRecorder{}
	_ = recorder.emit("run.browser.lifecycle", map[string]any{
		"phase":                           "ready",
		"browser_client_mode_selected":    "direct_mcp",
		"browser_interaction_policy":      "full",
		"browser_mutation_origins_sha256": digest,
	})
	_ = recorder.emit("run.browser.lifecycle", map[string]any{
		"phase":                           "closed",
		"status":                          "success",
		"browser_interaction_policy":      "full",
		"browser_mutation_origins_sha256": digest,
	})
	ready, closed := recorder.validate("direct_mcp", digest)
	if !ready || !closed {
		t.Fatalf("lifecycle ready=%v closed=%v", ready, closed)
	}
}

func TestMarkerAndOriginValidation(t *testing.T) {
	if !validMarker("provider-live-0123456789abcdef") {
		t.Fatal("valid marker was rejected")
	}
	for _, marker := range []string{"provider-live-short", "PROVIDER-live-0123456789abcdef", "provider-live-0123456789abcdef/"} {
		if validMarker(marker) {
			t.Fatalf("invalid marker %q was accepted", marker)
		}
	}
	if origin, err := publicHTTPSOrigin("https://Fixture.Example:443/provider-live"); err != nil || origin != "https://fixture.example" {
		t.Fatalf("origin = %q, %v", origin, err)
	}
	if _, err := publicHTTPSOrigin("http://fixture.example/provider-live"); err == nil {
		t.Fatal("HTTP fixture was accepted")
	}
}

const browserprotocolContractForTest = "openlinker.browser.v2"
