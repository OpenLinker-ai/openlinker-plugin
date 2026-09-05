package egressgateway

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestDoHResolverRequiresPublicLiteralHTTPSOrigin(t *testing.T) {
	for _, raw := range []string{
		"http://1.1.1.1/dns-query",
		"https://resolver.example/dns-query",
		"https://127.0.0.1/dns-query",
		"https://1.1.1.1:8443/dns-query",
		"https://1.1.1.1/",
	} {
		if _, err := NewDoHResolver(raw, nil); err == nil {
			t.Errorf("expected DoH endpoint rejection: %s", raw)
		}
	}
	if _, err := NewDoHResolver("https://1.1.1.1/dns-query", nil); err != nil {
		t.Fatalf("valid DoH endpoint: %v", err)
	}
}

func TestDoHResolverReturnsAndCachesAddressRecords(t *testing.T) {
	resolver, err := NewDoHResolver("https://1.1.1.1/dns-query", nil)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	resolver.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		body := `{"Status":0,"Answer":[]}`
		if request.URL.Query().Get("type") == "A" {
			body = `{"Status":0,"Answer":[{"type":1,"TTL":60,"data":"93.184.216.34"}]}`
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     make(http.Header),
		}, nil
	})
	first, err := resolver.LookupIP(context.Background(), "Example.COM.")
	if err != nil ||
		len(first) != 1 ||
		!first[0].Equal(net.ParseIP("93.184.216.34")) {
		t.Fatalf("first lookup = %v, %v", first, err)
	}
	second, err := resolver.LookupIP(context.Background(), "example.com")
	if err != nil || len(second) != 1 || calls != 2 {
		t.Fatalf("cached lookup = %v, calls=%d, err=%v", second, calls, err)
	}
	second[0][0] ^= 0xff
	third, _ := resolver.LookupIP(context.Background(), "example.com")
	if !third[0].Equal(net.ParseIP("93.184.216.34")) {
		t.Fatal("cached IP slice was returned without cloning")
	}
}

func TestDoHResolverBoundsCacheAndEvictsOldestEntry(t *testing.T) {
	resolver, err := NewDoHResolver("https://1.1.1.1/dns-query", nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for index := 0; index < maxDoHCacheEntries; index++ {
		resolver.cache[fmt.Sprintf("cached-%04d.example", index)] = dohCacheEntry{
			addresses: []net.IP{net.ParseIP("93.184.216.34")},
			expires:   now.Add(time.Duration(index+1) * time.Hour),
		}
	}
	resolver.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body := `{"Status":0,"Answer":[]}`
		if request.URL.Query().Get("type") == "A" {
			body = `{"Status":0,"Answer":[{"type":1,"TTL":60,"data":"93.184.216.34"}]}`
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     make(http.Header),
		}, nil
	})
	if _, err := resolver.LookupIP(context.Background(), "fresh.example"); err != nil {
		t.Fatal(err)
	}
	if len(resolver.cache) != maxDoHCacheEntries {
		t.Fatalf("DoH cache entries = %d", len(resolver.cache))
	}
	if _, found := resolver.cache["cached-0000.example"]; found {
		t.Fatal("oldest DoH cache entry was not evicted")
	}
	if _, found := resolver.cache["fresh.example"]; !found {
		t.Fatal("fresh DoH cache entry was not retained")
	}
}

func TestUpstreamProxyURLCanBePassedToDoHTransport(t *testing.T) {
	upstream, _ := url.Parse("http://proxy.example:8080")
	resolver, err := NewDoHResolver("https://1.1.1.1/dns-query", upstream)
	if err != nil {
		t.Fatal(err)
	}
	transport, ok := resolver.client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("DoH transport = %T", resolver.client.Transport)
	}
	if transport.MaxIdleConns != 16 ||
		transport.MaxIdleConnsPerHost != 4 ||
		transport.MaxConnsPerHost != 8 ||
		transport.IdleConnTimeout != 90*time.Second {
		t.Fatalf("DoH transport limits = %#v", transport)
	}
}
