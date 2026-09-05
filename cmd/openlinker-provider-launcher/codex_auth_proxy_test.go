//go:build linux

package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestRewriteCodexProviderArgumentsUsesLocalCredentialProxy(t *testing.T) {
	argv := []string{
		"codex",
		"-c", `model_providers.openlinker_proxy.base_url="https://router.example/v1"`,
		"-c", `model_providers.openlinker_proxy.env_key="CODEX_API_KEY"`,
		"exec",
	}
	rewritten := rewriteCodexProviderArguments(argv, "http://127.0.0.1:43129", codexLocalAuthEnv)
	joined := strings.Join(rewritten, " ")
	for _, expected := range []string{
		`model_providers.openlinker_proxy.base_url="http://127.0.0.1:43129"`,
		`model_providers.openlinker_proxy.env_key="OPENLINKER_LOCAL_CODEX_AUTH"`,
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("rewritten args do not include %s: %s", expected, joined)
		}
	}
	if strings.Contains(joined, "router.example") || strings.Contains(joined, `env_key="CODEX_API_KEY"`) {
		t.Fatalf("rewritten args retain upstream credential details: %s", joined)
	}
}

func TestCodexProviderEnvironmentDoesNotExposeProviderSecret(t *testing.T) {
	environment := codexProviderEnvironment([]string{
		"PATH=/usr/bin",
		"CODEX_API_KEY=provider-secret",
		"OPENLINKER_AGENT_TOKEN=agent-secret",
		codexAuthProxySecretEnv + "=stale-secret",
	}, "ephemeral-local-authorization")
	joined := strings.Join(environment, "\n")
	if strings.Contains(joined, "provider-secret") || strings.Contains(joined, "agent-secret") || strings.Contains(joined, "stale-secret") {
		t.Fatalf("Codex environment exposes an upstream secret: %s", joined)
	}
	if !strings.Contains(joined, codexLocalAuthEnv+"=ephemeral-local-authorization") {
		t.Fatalf("Codex environment is missing the non-secret local credential: %s", joined)
	}
}

func TestNewCodexLocalAuthTokenIsRandomAndBounded(t *testing.T) {
	first, err := newCodexLocalAuthToken()
	if err != nil {
		t.Fatal(err)
	}
	second, err := newCodexLocalAuthToken()
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 43 || len(second) != 43 {
		t.Fatalf("unexpected local authorization lengths: %d, %d", len(first), len(second))
	}
	if first == second {
		t.Fatal("local authorizations were reused")
	}
}

func TestSetEnvironmentValueReplacesOnlyExactKey(t *testing.T) {
	environment := setEnvironmentValue([]string{
		"NO_PROXY=",
		"no_proxy=",
		"HTTP_PROXY=http://egress:3128",
	}, "NO_PROXY", "127.0.0.1,localhost")
	joined := strings.Join(environment, "\n")
	if strings.Count(joined, "NO_PROXY=") != 1 ||
		!strings.Contains(joined, "NO_PROXY=127.0.0.1,localhost") ||
		!strings.Contains(joined, "HTTP_PROXY=http://egress:3128") {
		t.Fatalf("local proxy bypass environment = %s", joined)
	}
}

func TestCodexAuthProxyPinsUpstreamAndInjectsCredential(t *testing.T) {
	var observedPath, observedAuthorization string
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		observedPath = request.URL.Path
		observedAuthorization = request.Header.Get("Authorization")
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set("Alt-Svc", `h3=":443"`)
		writer.Header().Set("CF-Ray", "upstream-ray")
		writer.Header().Set("NEL", `{"success_fraction":0}`)
		writer.Header().Set("Report-To", `{"group":"upstream"}`)
		_, _ = writer.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()
	target, err := url.Parse(upstream.URL + "/v1")
	if err != nil {
		t.Fatal(err)
	}
	proxy := httptest.NewServer(newCodexAuthProxyHandler(target, "provider-secret", "local-credential-proxy"))
	defer proxy.Close()

	request, err := http.NewRequest(http.MethodPost, proxy.URL+"/responses", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer local-credential-proxy")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("proxy status = %d", response.StatusCode)
	}
	if observedPath != "/v1/responses" {
		t.Fatalf("upstream path = %q", observedPath)
	}
	if observedAuthorization != "Bearer provider-secret" {
		t.Fatalf("upstream Authorization was not replaced")
	}
	for _, name := range []string{"Alt-Svc", "CF-Ray", "NEL", "Report-To"} {
		if value := response.Header.Get(name); value != "" {
			t.Fatalf("proxy retained upstream origin header %s=%q", name, value)
		}
	}
}

func TestCodexAuthProxyRejectsRequestsOutsideResponsesAPI(t *testing.T) {
	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		upstreamCalls++
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()
	target, err := url.Parse(upstream.URL + "/v1")
	if err != nil {
		t.Fatal(err)
	}
	proxy := httptest.NewServer(newCodexAuthProxyHandler(target, "provider-secret", "local-credential-proxy"))
	defer proxy.Close()

	tests := []struct {
		name          string
		method        string
		path          string
		authorization string
		status        int
	}{
		{name: "wrong path", method: http.MethodPost, path: "/models", authorization: "Bearer local-credential-proxy", status: http.StatusNotFound},
		{name: "wrong method", method: http.MethodGet, path: "/responses", authorization: "Bearer local-credential-proxy", status: http.StatusNotFound},
		{name: "wrong credential", method: http.MethodPost, path: "/responses", authorization: "Bearer caller-controlled", status: http.StatusUnauthorized},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request, requestErr := http.NewRequest(test.method, proxy.URL+test.path, strings.NewReader(`{}`))
			if requestErr != nil {
				t.Fatal(requestErr)
			}
			request.Header.Set("Authorization", test.authorization)
			response, requestErr := http.DefaultClient.Do(request)
			if requestErr != nil {
				t.Fatal(requestErr)
			}
			defer response.Body.Close()
			_, _ = io.Copy(io.Discard, response.Body)
			if response.StatusCode != test.status {
				t.Fatalf("status = %d, want %d", response.StatusCode, test.status)
			}
		})
	}
	if upstreamCalls != 0 {
		t.Fatalf("rejected requests reached upstream %d times", upstreamCalls)
	}
}
