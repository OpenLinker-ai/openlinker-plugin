//go:build !windows

package main

import (
	"strings"
	"testing"
	"time"

	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserprotocol"
)

func TestSummarizeLatencyReportsNearestRankPercentiles(t *testing.T) {
	samples := make([]time.Duration, 100)
	for index := range samples {
		samples[index] = time.Duration(100-index) * time.Millisecond
	}
	stats := summarizeLatency(samples)
	if stats.Samples != 100 ||
		stats.P50MS != 50 ||
		stats.P95MS != 95 ||
		stats.P99MS != 99 {
		t.Fatalf("latency stats = %#v", stats)
	}
}

func TestSummarizeLatencyHandlesEmptyAndSubMillisecondSamples(t *testing.T) {
	if stats := summarizeLatency(nil); stats != (latencyStats{}) {
		t.Fatalf("empty latency stats = %#v", stats)
	}
	stats := summarizeLatency([]time.Duration{
		1500 * time.Microsecond,
		500 * time.Microsecond,
	})
	if stats.Samples != 2 || stats.P50MS != 0.5 ||
		stats.P95MS != 1.5 || stats.P99MS != 1.5 {
		t.Fatalf("sub-millisecond latency stats = %#v", stats)
	}
}

func TestRebindAttemptURLPreservesHostAndForcesANewRequest(t *testing.T) {
	first, err := rebindAttemptURL(
		"http://fixture.example/path?existing=value",
		0,
	)
	if err != nil {
		t.Fatal(err)
	}
	second, err := rebindAttemptURL(
		"http://fixture.example/path?existing=value",
		1,
	)
	if err != nil {
		t.Fatal(err)
	}
	if first !=
		"http://fixture.example/path?existing=value&openlinker_rebind_attempt=1" {
		t.Fatalf("first attempt URL = %q", first)
	}
	if second !=
		"http://fixture.example/path?existing=value&openlinker_rebind_attempt=2" {
		t.Fatalf("second attempt URL = %q", second)
	}
}

func TestRebindAttemptURLRejectsMalformedOrNonHTTPTargets(t *testing.T) {
	for _, target := range []string{
		"://bad",
		"https://fixture.example/",
		"http:///missing-host",
	} {
		if _, err := rebindAttemptURL(target, 0); err == nil {
			t.Errorf("accepted invalid rebind target %q", target)
		}
	}
}

func TestValidateMCPAttachmentEvidenceRequiresBoundedConstants(t *testing.T) {
	environment := browserprotocol.EnvironmentEvidence{
		BrowserEngine:       "chromium",
		BrowserDistribution: "playwright_chromium",
		BrowserVersion:      "149.0.7827.0",
		BrowserMajorVersion: 149,
		BrowserLocale:       "en-US",
		BrowserTimezone:     "UTC",
		FontContractVersion: "openlinker.browser.fonts.v1",
		FontManifestSHA256:  strings.Repeat("a", 64),
	}
	actual := map[string]any{
		"browser_engine":                        "chromium",
		"browser_distribution":                  "playwright_chromium",
		"browser_major_version":                 float64(149),
		"browser_locale":                        "en-US",
		"browser_timezone":                      "UTC",
		"font_contract_version":                 "openlinker.browser.fonts.v1",
		"font_manifest_sha256":                  strings.Repeat("a", 64),
		"browser_interaction_policy":            "restricted",
		"browser_interaction_policy_generation": float64(1),
		"browser_mutation_origins":              []any{},
		"browser_mutation_origins_sha256":       browserprotocol.RestrictedMutationOriginsSHA256,
		"browser_contract_id":                   browserprotocol.ContractID,
	}
	identity := identityFor(1, 1, 1, 1, 1)
	if err := validateMCPAttachmentEvidence(actual, environment, identity); err != nil {
		t.Fatal(err)
	}
	actual["browser_version"] = environment.BrowserVersion
	if err := validateMCPAttachmentEvidence(actual, environment, identity); err == nil {
		t.Fatal("accepted attachment evidence containing the full Browser version")
	}
}

func TestFullIdentityForCanonicalizesAndBindsMutationOrigins(t *testing.T) {
	identity, err := fullIdentityFor(
		1,
		2,
		3,
		4,
		5,
		[]string{"https://B.example", "https://a.example:443"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if identity.BrowserInteractionPolicy != "full" ||
		identity.BrowserInteractionPolicyGeneration != 2 ||
		len(identity.BrowserMutationOrigins) != 2 ||
		identity.BrowserMutationOrigins[0] != "https://a.example" ||
		identity.BrowserMutationOrigins[1] != "https://b.example" ||
		identity.BrowserMutationOriginsSHA256 == "" ||
		identity.BrowserMutationOriginsSHA256 == browserprotocol.RestrictedMutationOriginsSHA256 {
		t.Fatalf("full identity = %#v", identity)
	}
}
