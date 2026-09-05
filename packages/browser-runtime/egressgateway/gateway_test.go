package egressgateway

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBlockedIPRejectsPrivateReservedAndMetadataRanges(t *testing.T) {
	blocked := []string{
		"0.0.0.0", "10.0.0.1", "100.64.0.1", "127.0.0.1",
		"169.254.169.254", "172.16.0.1", "192.168.1.1", "192.0.0.1",
		"192.0.2.1", "198.18.0.1", "198.51.100.1", "203.0.113.1",
		"224.0.0.1", "255.255.255.255", "192.88.99.1", "::", "::1",
		"64:ff9b::a00:1", "64:ff9b:1::1", "100::1", "2001::1",
		"2001:db8::1", "2002::1", "3fff::1", "5f00::1", "fc00::1",
		"fe80::1", "ff02::1", "::ffff:127.0.0.1",
	}
	for _, raw := range blocked {
		if !IsBlockedIP(net.ParseIP(raw)) {
			t.Errorf("expected %s to be blocked", raw)
		}
	}
	for _, raw := range []string{"1.1.1.1", "8.8.8.8", "2606:4700:4700::1111"} {
		if IsBlockedIP(net.ParseIP(raw)) {
			t.Errorf("expected %s to remain public", raw)
		}
	}
}

func TestValidPublicHostnameRejectsInternalNames(t *testing.T) {
	for _, host := range []string{
		"localhost",
		"gateway",
		"service.internal",
		"printer.local",
		"metadata.google.internal",
		"bad host.example",
	} {
		if IsValidPublicHostname(host) {
			t.Errorf("expected %q to be rejected", host)
		}
	}
	for _, host := range []string{
		"api.openai.com",
		"api.anthropic.com",
		"1.1.1.1",
	} {
		if !IsValidPublicHostname(host) {
			t.Errorf("expected %q to be accepted as a hostname", host)
		}
	}
}

func TestGatewayRejectsPrivateHTTPAndConnectWithoutLeakingURL(t *testing.T) {
	var logs bytes.Buffer
	g := New(Options{
		Resolver: net.DefaultResolver,
		Dialer:   &net.Dialer{},
		Logger:   log.New(&logs, "", 0),
	})

	req := httptest.NewRequest(
		http.MethodGet,
		"http://127.0.0.1/private?token=secret",
		nil,
	)
	rec := httptest.NewRecorder()
	g.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("private HTTP status = %d", rec.Code)
	}
	if rec.Header().Get(DecisionHeader) != "blocked" {
		t.Fatalf("private HTTP response did not expose the internal block decision")
	}
	if strings.Contains(logs.String(), "token") ||
		strings.Contains(logs.String(), "secret") ||
		strings.Contains(logs.String(), "/private") {
		t.Fatalf("gateway log leaked URL data: %s", logs.String())
	}

	req = httptest.NewRequest(http.MethodConnect, "http://gateway.invalid", nil)
	req.Host = "169.254.169.254:443"
	rec = httptest.NewRecorder()
	g.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("private CONNECT status = %d", rec.Code)
	}
	if rec.Header().Get(DecisionHeader) != "blocked" {
		t.Fatalf("private CONNECT response did not expose the internal block decision")
	}
}

func TestGatewayHealthProbeUsesTheSharedContract(t *testing.T) {
	gateway := httptest.NewServer(New(Options{}))
	defer gateway.Close()

	response, err := http.Get(gateway.URL + HealthPath)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("health status = %d", response.StatusCode)
	}
	if response.Header.Get(DecisionHeader) != HealthDecision {
		t.Fatalf(
			"health decision = %q",
			response.Header.Get(DecisionHeader),
		)
	}

	proxied := httptest.NewRequest(
		http.MethodGet,
		"http://gateway.example"+HealthPath,
		nil,
	)
	recorder := httptest.NewRecorder()
	New(Options{}).ServeHTTP(recorder, proxied)
	if recorder.Code == http.StatusNoContent {
		t.Fatal("absolute proxy request reached the direct health endpoint")
	}
}

func TestGatewayReusesOneBoundedHTTPTransport(t *testing.T) {
	gateway := New(Options{})
	if gateway.transport == nil {
		t.Fatal("Gateway HTTP transport is nil")
	}
	if gateway.transport.MaxIdleConns != 64 ||
		gateway.transport.MaxIdleConnsPerHost != 16 ||
		gateway.transport.MaxConnsPerHost != 32 ||
		gateway.transport.IdleConnTimeout != 90*time.Second {
		t.Fatalf("Gateway transport limits = %#v", gateway.transport)
	}
	first := gateway.transport
	request := httptest.NewRequest(http.MethodGet, HealthPath, nil)
	recorder := httptest.NewRecorder()
	gateway.ServeHTTP(recorder, request)
	if gateway.transport != first {
		t.Fatal("Gateway replaced its shared HTTP transport")
	}
}

func TestResolveSecretSupportsDirectOrOwnerOnlyFile(t *testing.T) {
	t.Setenv("OPENLINKER_EGRESS_UPSTREAM_PROXY", "http://proxy.example:8080")
	value, err := ResolveSecret("OPENLINKER_EGRESS_UPSTREAM_PROXY")
	if err != nil || value != "http://proxy.example:8080" {
		t.Fatalf("direct secret = %q, %v", value, err)
	}

	t.Setenv("OPENLINKER_EGRESS_UPSTREAM_PROXY", "")
	path := filepath.Join(t.TempDir(), "proxy")
	if err := os.WriteFile(path, []byte("https://proxy.example:8443\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENLINKER_EGRESS_UPSTREAM_PROXY_FILE", path)
	value, err = ResolveSecret("OPENLINKER_EGRESS_UPSTREAM_PROXY")
	if err != nil || value != "https://proxy.example:8443" {
		t.Fatalf("file secret = %q, %v", value, err)
	}

	t.Setenv("OPENLINKER_EGRESS_UPSTREAM_PROXY", "http://second.example:8080")
	if _, err := ResolveSecret("OPENLINKER_EGRESS_UPSTREAM_PROXY"); err == nil {
		t.Fatal("expected dual-source rejection")
	}
}

func TestGatewayRejectsMixedPublicAndPrivateDNSAnswers(t *testing.T) {
	g := New(Options{
		LookupIP: func(context.Context, string) ([]net.IP, error) {
			return []net.IP{
				net.ParseIP("93.184.216.34"),
				net.ParseIP("127.0.0.1"),
			}, nil
		},
	})
	if _, err := g.resolvePublic(context.Background(), "mixed.example"); err == nil {
		t.Fatal("mixed public/private DNS answer was accepted")
	}
}

func TestGatewayPinsValidatedHTTPDestination(t *testing.T) {
	var receivedHost string
	var dialedAddress string
	upstream := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		receivedHost = request.Host
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()
	upstreamAddress := strings.TrimPrefix(upstream.URL, "http://")
	g := New(Options{
		LookupIP: func(context.Context, string) ([]net.IP, error) {
			return []net.IP{net.ParseIP("93.184.216.34")}, nil
		},
		DialContext: func(
			ctx context.Context,
			network string,
			address string,
		) (net.Conn, error) {
			dialedAddress = address
			return (&net.Dialer{}).DialContext(ctx, network, upstreamAddress)
		},
	})
	request := httptest.NewRequest(http.MethodGet, "http://public.example/", nil)
	recorder := httptest.NewRecorder()
	g.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("gateway status = %d", recorder.Code)
	}
	if dialedAddress != "93.184.216.34:80" {
		t.Fatalf("gateway dialed %q instead of the validated address", dialedAddress)
	}
	if receivedHost != "public.example" {
		t.Fatalf("original Host was not preserved: %q", receivedHost)
	}
}

func TestGatewayTriesEachValidatedAddressBeforeOpeningConnectTunnel(t *testing.T) {
	publicOrigin := httptest.NewTLSServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer publicOrigin.Close()
	publicAddress := strings.TrimPrefix(publicOrigin.URL, "https://")

	var dialed []string
	gateway := New(Options{
		LookupIP: func(context.Context, string) ([]net.IP, error) {
			return []net.IP{
				net.ParseIP("93.184.216.34"),
				net.ParseIP("93.184.216.35"),
			}, nil
		},
		DialContext: func(
			ctx context.Context,
			network string,
			address string,
		) (net.Conn, error) {
			dialed = append(dialed, address)
			if len(dialed) == 1 {
				return nil, errors.New("first public edge is unavailable")
			}
			return (&net.Dialer{}).DialContext(ctx, network, publicAddress)
		},
	})
	proxy := httptest.NewServer(gateway)
	defer proxy.Close()
	proxyURL, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: &http.Transport{
		Proxy:           http.ProxyURL(proxyURL),
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // test server
	}}
	response, err := client.Get("https://public.example/")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("public response status = %d", response.StatusCode)
	}
	want := []string{"93.184.216.34:443", "93.184.216.35:443"}
	if strings.Join(dialed, ",") != strings.Join(want, ",") {
		t.Fatalf("CONNECT attempts = %v, want %v", dialed, want)
	}
}

func TestGatewayRejectsDNSRebindingAfterPublicAnswer(t *testing.T) {
	var lookups int
	var dials int
	upstream := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()
	upstreamAddress := strings.TrimPrefix(upstream.URL, "http://")
	g := New(Options{
		LookupIP: func(context.Context, string) ([]net.IP, error) {
			lookups++
			if lookups == 1 {
				return []net.IP{net.ParseIP("93.184.216.34")}, nil
			}
			return []net.IP{net.ParseIP("127.0.0.1")}, nil
		},
		DialContext: func(
			ctx context.Context,
			network string,
			address string,
		) (net.Conn, error) {
			dials++
			return (&net.Dialer{}).DialContext(ctx, network, upstreamAddress)
		},
	})

	first := httptest.NewRecorder()
	g.ServeHTTP(
		first,
		httptest.NewRequest(http.MethodGet, "http://rebind.example/", nil),
	)
	if first.Code != http.StatusNoContent {
		t.Fatalf("first gateway status = %d", first.Code)
	}

	second := httptest.NewRecorder()
	g.ServeHTTP(
		second,
		httptest.NewRequest(http.MethodGet, "http://rebind.example/", nil),
	)
	if second.Code != http.StatusForbidden {
		t.Fatalf("rebound gateway status = %d", second.Code)
	}
	if second.Header().Get(DecisionHeader) != "blocked" {
		t.Fatal("rebound destination did not expose the block decision")
	}
	if lookups != 2 {
		t.Fatalf("DNS lookup count = %d", lookups)
	}
	if dials != 1 {
		t.Fatalf("blocked rebound unexpectedly dialed; dial count = %d", dials)
	}
}

func TestGatewayBlocksRedirectFromPublicOriginToLoopback(t *testing.T) {
	publicOrigin := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		http.Redirect(
			writer,
			request,
			"http://127.0.0.1/private",
			http.StatusFound,
		)
	}))
	defer publicOrigin.Close()
	publicAddress := strings.TrimPrefix(publicOrigin.URL, "http://")

	gateway := New(Options{
		LookupIP: func(context.Context, string) ([]net.IP, error) {
			return []net.IP{net.ParseIP("93.184.216.34")}, nil
		},
		DialContext: func(
			ctx context.Context,
			network string,
			address string,
		) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, publicAddress)
		},
	})
	proxy := httptest.NewServer(gateway)
	defer proxy.Close()
	proxyURL, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)},
	}
	response, err := client.Get("http://public.example/")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("redirected gateway status = %d", response.StatusCode)
	}
	if response.Header.Get(DecisionHeader) != "blocked" {
		t.Fatal("loopback redirect did not expose the block decision")
	}
}

func TestGatewayRejectsPrivateDoHAnswer(t *testing.T) {
	g := New(Options{
		Resolver: net.DefaultResolver,
		LookupIP: func(context.Context, string) ([]net.IP, error) {
			return []net.IP{net.ParseIP("127.0.0.1")}, nil
		},
		Dialer: &net.Dialer{},
		Logger: log.New(io.Discard, "", 0),
	})
	if _, err := g.resolvePublic(context.Background(), "attacker.example"); err == nil ||
		!strings.Contains(err.Error(), "private") {
		t.Fatalf("private secure DNS answer was accepted: %v", err)
	}
}

func TestUpstreamProxyRejectsAmbiguousURLComponents(t *testing.T) {
	for _, raw := range []string{
		"http://proxy.example:8080/path",
		"http://proxy.example:8080/?mode=unsafe",
		"http://proxy.example:8080/#fragment",
	} {
		if _, err := ParseUpstreamProxy(raw); err == nil {
			t.Fatalf("ambiguous upstream proxy URL was accepted: %s", raw)
		}
	}
	for _, raw := range []string{
		"http://proxy.example:8080",
		"https://user:pass@proxy.example/",
	} {
		if _, err := ParseUpstreamProxy(raw); err != nil {
			t.Fatalf("valid upstream proxy URL %s: %v", raw, err)
		}
	}
}
