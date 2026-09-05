package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProviderLiveFixtureExposesMarkerAndCountsRealBrowserDocuments(t *testing.T) {
	marker := "provider-live-0123456789abcdef"
	var observed counters
	handler := providerLiveHandler(marker, &observed)
	request := httptest.NewRequest(http.MethodGet, "https://fixture.example/provider-live", nil)
	request.Header.Set("User-Agent", "Mozilla/5.0 Chrome/140.0.0.0 Safari/537.36")
	request.Header.Set("Sec-Fetch-Dest", "document")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK ||
		!strings.Contains(response.Body.String(), marker) ||
		!strings.Contains(response.Body.String(), "id=\"provider-live-marker\"") ||
		observed.providerLiveRequests.Load() != 1 ||
		observed.providerLiveBrowsers.Load() != 1 {
		t.Fatalf(
			"response=%d body=%q requests=%d browsers=%d",
			response.Code,
			response.Body.String(),
			observed.providerLiveRequests.Load(),
			observed.providerLiveBrowsers.Load(),
		)
	}
}

func TestProviderLiveFixtureDoesNotCountOrdinaryHTTPClientsAsBrowsers(t *testing.T) {
	var observed counters
	handler := providerLiveHandler("provider-live-0123456789abcdef", &observed)
	request := httptest.NewRequest(http.MethodGet, "https://fixture.example/provider-live", nil)
	request.Header.Set("User-Agent", "curl/8.0")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if observed.providerLiveRequests.Load() != 1 || observed.providerLiveBrowsers.Load() != 0 {
		t.Fatalf(
			"requests=%d browsers=%d",
			observed.providerLiveRequests.Load(),
			observed.providerLiveBrowsers.Load(),
		)
	}
}

func TestProviderLiveMarkerValidation(t *testing.T) {
	if !validProviderLiveMarker("provider-live-0123456789abcdef") {
		t.Fatal("valid marker was rejected")
	}
	for _, marker := range []string{
		"provider-live-short",
		"provider-live-0123456789ABCDEF",
		"provider-live-0123456789abcdef/",
	} {
		if validProviderLiveMarker(marker) {
			t.Fatalf("invalid marker %q was accepted", marker)
		}
	}
}
