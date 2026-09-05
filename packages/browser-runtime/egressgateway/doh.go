package egressgateway

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	maxDoHResponseBytes = 64 << 10
	maxDoHCacheEntries  = 1024
)

type DoHResolver struct {
	endpoint *url.URL
	client   *http.Client

	mu    sync.Mutex
	cache map[string]dohCacheEntry
}

type dohCacheEntry struct {
	addresses []net.IP
	expires   time.Time
}

type dohJSONResponse struct {
	Status int `json:"Status"`
	Answer []struct {
		Type int    `json:"type"`
		TTL  int64  `json:"TTL"`
		Data string `json:"data"`
	} `json:"Answer"`
}

func NewDoHResolver(raw string, upstream *url.URL) (*DoHResolver, error) {
	endpoint, err := url.Parse(strings.TrimSpace(raw))
	if err != nil ||
		endpoint.Scheme != "https" ||
		endpoint.Hostname() == "" ||
		endpoint.User != nil ||
		endpoint.RawQuery != "" ||
		endpoint.Fragment != "" {
		return nil, errors.New("OPENLINKER_EGRESS_DOH_URL must be a public HTTPS URL with a literal IP host")
	}
	endpointIP := net.ParseIP(endpoint.Hostname())
	if endpointIP == nil || IsBlockedIP(endpointIP) {
		return nil, errors.New("OPENLINKER_EGRESS_DOH_URL must use a public literal IP host")
	}
	if endpoint.Port() != "" && endpoint.Port() != "443" {
		return nil, errors.New("OPENLINKER_EGRESS_DOH_URL is restricted to port 443")
	}
	if endpoint.Path == "" || endpoint.Path == "/" {
		return nil, errors.New("OPENLINKER_EGRESS_DOH_URL must include a DNS JSON endpoint path")
	}
	transport := &http.Transport{
		Proxy:               http.ProxyURL(upstream),
		ForceAttemptHTTP2:   true,
		MaxIdleConns:        16,
		MaxIdleConnsPerHost: 4,
		MaxConnsPerHost:     8,
		TLSHandshakeTimeout: 15 * time.Second,
		IdleConnTimeout:     90 * time.Second,
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
	}
	return &DoHResolver{
		endpoint: endpoint,
		client:   &http.Client{Transport: transport, Timeout: 20 * time.Second},
		cache:    make(map[string]dohCacheEntry),
	}, nil
}

func (r *DoHResolver) EndpointHost() string {
	return r.endpoint.Host
}

func (r *DoHResolver) LookupIP(ctx context.Context, host string) ([]net.IP, error) {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	now := time.Now()
	r.mu.Lock()
	if entry, ok := r.cache[host]; ok && now.Before(entry.expires) {
		addresses := cloneIPs(entry.addresses)
		r.mu.Unlock()
		return addresses, nil
	}
	r.mu.Unlock()

	var addresses []net.IP
	var minimumTTL int64
	var lastErr error
	for _, queryType := range []string{"A", "AAAA"} {
		found, ttl, err := r.lookupType(ctx, host, queryType)
		if err != nil {
			lastErr = err
			continue
		}
		addresses = append(addresses, found...)
		if ttl > 0 && (minimumTTL == 0 || ttl < minimumTTL) {
			minimumTTL = ttl
		}
	}
	if len(addresses) == 0 {
		if lastErr == nil {
			lastErr = errors.New("secure DNS returned no address records")
		}
		return nil, lastErr
	}
	if minimumTTL <= 0 {
		minimumTTL = 30
	}
	if minimumTTL > 300 {
		minimumTTL = 300
	}
	r.mu.Lock()
	for cachedHost, entry := range r.cache {
		if !now.Before(entry.expires) {
			delete(r.cache, cachedHost)
		}
	}
	if len(r.cache) >= maxDoHCacheEntries {
		var oldestHost string
		var oldestExpiry time.Time
		for cachedHost, entry := range r.cache {
			if oldestHost == "" || entry.expires.Before(oldestExpiry) {
				oldestHost = cachedHost
				oldestExpiry = entry.expires
			}
		}
		delete(r.cache, oldestHost)
	}
	r.cache[host] = dohCacheEntry{
		addresses: cloneIPs(addresses),
		expires:   now.Add(time.Duration(minimumTTL) * time.Second),
	}
	r.mu.Unlock()
	return addresses, nil
}

func (r *DoHResolver) lookupType(
	ctx context.Context,
	host string,
	queryType string,
) ([]net.IP, int64, error) {
	requestURL := *r.endpoint
	query := requestURL.Query()
	query.Set("name", host)
	query.Set("type", queryType)
	requestURL.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		requestURL.String(),
		nil,
	)
	if err != nil {
		return nil, 0, errors.New("build secure DNS request")
	}
	request.Header.Set("Accept", "application/dns-json")
	response, err := r.client.Do(request)
	if err != nil {
		return nil, 0, errors.New("secure DNS unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("secure DNS returned status %d", response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxDoHResponseBytes+1))
	if err != nil || len(raw) > maxDoHResponseBytes {
		return nil, 0, errors.New("secure DNS response is invalid")
	}
	var decoded dohJSONResponse
	if err := json.Unmarshal(raw, &decoded); err != nil || decoded.Status != 0 {
		return nil, 0, errors.New("secure DNS response is invalid")
	}
	wantType := 1
	if queryType == "AAAA" {
		wantType = 28
	}
	var addresses []net.IP
	var minimumTTL int64
	for _, answer := range decoded.Answer {
		if answer.Type != wantType {
			continue
		}
		address := net.ParseIP(strings.TrimSpace(answer.Data))
		if address == nil {
			continue
		}
		addresses = append(addresses, address)
		if answer.TTL > 0 && (minimumTTL == 0 || answer.TTL < minimumTTL) {
			minimumTTL = answer.TTL
		}
	}
	if len(addresses) == 0 {
		return nil, 0, fmt.Errorf("secure DNS returned no %s records", strconv.Quote(queryType))
	}
	return addresses, minimumTTL, nil
}

func cloneIPs(input []net.IP) []net.IP {
	cloned := make([]net.IP, 0, len(input))
	for _, address := range input {
		cloned = append(cloned, append(net.IP(nil), address...))
	}
	return cloned
}
