package egressgateway

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/netpolicy"
)

const Version = "0.1.0"
const DefaultDoHURL = "https://1.1.1.1/dns-query"
const maxGatewaySecretBytes = 64 << 10
const maxTunnelDialAddresses = 8

var blockedDestinationPrefixes = netpolicy.BlockedDestinationPrefixes()

type Options struct {
	Resolver    *net.Resolver
	LookupIP    func(context.Context, string) ([]net.IP, error)
	Dialer      *net.Dialer
	DialContext func(context.Context, string, string) (net.Conn, error)
	Upstream    *url.URL
	Logger      *log.Logger
}

type Gateway struct {
	resolver    *net.Resolver
	lookupIP    func(context.Context, string) ([]net.IP, error)
	dialContext func(context.Context, string, string) (net.Conn, error)
	upstream    *url.URL
	transport   *http.Transport
	logger      *log.Logger
}

func New(options Options) *Gateway {
	resolver := options.Resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	dialer := options.Dialer
	if dialer == nil {
		dialer = &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	}
	logger := options.Logger
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	dialContext := options.DialContext
	if dialContext == nil {
		dialContext = dialer.DialContext
	}
	transport := &http.Transport{
		Proxy:                 http.ProxyURL(options.Upstream),
		DialContext:           dialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          64,
		MaxIdleConnsPerHost:   16,
		MaxConnsPerHost:       32,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
		IdleConnTimeout:       90 * time.Second,
	}
	return &Gateway{
		resolver:    resolver,
		lookupIP:    options.LookupIP,
		dialContext: dialContext,
		upstream:    options.Upstream,
		transport:   transport,
		logger:      logger,
	}
}

func NewDefault(rawDoHURL string, upstream *url.URL, logger *log.Logger) (*Gateway, string, error) {
	if strings.TrimSpace(rawDoHURL) == "" {
		rawDoHURL = DefaultDoHURL
	}
	doh, err := NewDoHResolver(rawDoHURL, upstream)
	if err != nil {
		return nil, "", err
	}
	return New(Options{
		Resolver: net.DefaultResolver,
		LookupIP: doh.LookupIP,
		Dialer:   &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second},
		Upstream: upstream,
		Logger:   logger,
	}), doh.EndpointHost(), nil
}

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if isDirectHealthRequest(r) {
		w.Header().Set(DecisionHeader, HealthDecision)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method == http.MethodConnect {
		g.serveConnect(w, r)
		return
	}
	if r.URL == nil || r.URL.Scheme == "" || r.URL.Host == "" || r.URL.User != nil {
		http.Error(w, "absolute public URL required", http.StatusBadRequest)
		return
	}
	if r.URL.Scheme != "http" {
		http.Error(w, "HTTPS proxy requests must use CONNECT", http.StatusBadRequest)
		return
	}
	port := r.URL.Port()
	if port == "" {
		port = "80"
	}
	if err := validatePort(port, false); err != nil {
		writeBlockedResponse(w, err.Error())
		return
	}
	addresses, err := g.resolvePublic(r.Context(), r.URL.Hostname())
	if err != nil {
		g.logger.Printf("blocked method=%s host=%s", r.Method, safeHost(r.URL.Hostname()))
		writeBlockedResponse(w, "private or invalid destination blocked")
		return
	}

	outbound := r.Clone(r.Context())
	outbound.RequestURI = ""
	// Pin the actual TCP destination to the address that passed validation. The
	// original authority remains in Host, so HTTP virtual hosting still works.
	// This avoids a second DNS lookup in either the local transport or an
	// operator upstream proxy and closes DNS-rebinding/TOCTOU bypasses.
	outbound.Host = r.URL.Host
	outbound.URL.Host = net.JoinHostPort(addresses[0].String(), port)
	removeHopHeaders(outbound.Header)
	outbound.Header.Del("Proxy-Authorization")
	response, err := g.transport.RoundTrip(outbound)
	if err != nil {
		http.Error(w, "public destination unavailable", http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	removeHopHeaders(response.Header)
	copyHeaders(w.Header(), response.Header)
	w.WriteHeader(response.StatusCode)
	_, _ = io.Copy(w, response.Body)
}

func isDirectHealthRequest(r *http.Request) bool {
	return r.Method == http.MethodGet &&
		r.URL != nil &&
		r.URL.Scheme == "" &&
		r.URL.Host == "" &&
		r.URL.Path == HealthPath &&
		r.URL.RawQuery == ""
}

func (g *Gateway) serveConnect(w http.ResponseWriter, r *http.Request) {
	host, port, err := net.SplitHostPort(r.Host)
	if err != nil {
		http.Error(w, "CONNECT requires host:port", http.StatusBadRequest)
		return
	}
	if err := validatePort(port, true); err != nil {
		writeBlockedResponse(w, err.Error())
		return
	}
	addresses, err := g.resolvePublic(r.Context(), host)
	if err != nil {
		g.logger.Printf("blocked method=CONNECT host=%s", safeHost(host))
		writeBlockedResponse(w, "private or invalid destination blocked")
		return
	}
	upstreamConn, err := g.openTunnel(r.Context(), addresses, port)
	if err != nil {
		g.logger.Printf("unavailable method=CONNECT host=%s", safeHost(host))
		http.Error(w, "public destination unavailable", http.StatusBadGateway)
		return
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		upstreamConn.Close()
		http.Error(w, "tunneling unavailable", http.StatusInternalServerError)
		return
	}
	clientConn, buffered, err := hijacker.Hijack()
	if err != nil {
		upstreamConn.Close()
		return
	}
	_, _ = buffered.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
	_ = buffered.Flush()

	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(upstreamConn, clientConn); done <- struct{}{} }()
	go func() { _, _ = io.Copy(clientConn, upstreamConn); done <- struct{}{} }()
	<-done
	_ = clientConn.Close()
	_ = upstreamConn.Close()
}

// openTunnel tries only the bounded address set already returned and fully
// validated by resolvePublic. No retry performs another DNS lookup, and the
// client has not received a successful CONNECT response or sent application
// bytes yet, so moving to the next validated public edge cannot duplicate an
// HTTP mutation.
func (g *Gateway) openTunnel(
	ctx context.Context,
	addresses []net.IP,
	port string,
) (net.Conn, error) {
	attempts := len(addresses)
	if attempts > maxTunnelDialAddresses {
		attempts = maxTunnelDialAddresses
	}
	var lastErr error
	for index := 0; index < attempts; index++ {
		target := net.JoinHostPort(addresses[index].String(), port)
		var connection net.Conn
		if g.upstream == nil {
			connection, lastErr = g.dialContext(ctx, "tcp", target)
		} else {
			connection, lastErr = g.connectThroughUpstream(ctx, target)
		}
		if lastErr == nil {
			return connection, nil
		}
	}
	if lastErr == nil {
		lastErr = errors.New("public destination has no validated address")
	}
	return nil, lastErr
}

func writeBlockedResponse(w http.ResponseWriter, message string) {
	w.Header().Set(DecisionHeader, "blocked")
	http.Error(w, message, http.StatusForbidden)
}

func (g *Gateway) resolvePublic(ctx context.Context, host string) ([]net.IP, error) {
	host = strings.TrimSpace(strings.TrimSuffix(host, "."))
	if !IsValidPublicHostname(host) {
		return nil, errors.New("invalid public hostname")
	}
	if literal := net.ParseIP(host); literal != nil {
		if IsBlockedIP(literal) {
			return nil, errors.New("private address")
		}
		return []net.IP{literal}, nil
	}
	var resolved []net.IP
	var err error
	if g.lookupIP != nil {
		resolved, err = g.lookupIP(ctx, host)
	} else {
		resolved, err = g.resolver.LookupIP(ctx, "ip", host)
	}
	if err != nil || len(resolved) == 0 {
		return nil, errors.New("DNS resolution failed")
	}
	for _, address := range resolved {
		if IsBlockedIP(address) {
			return nil, errors.New("DNS resolved to private address")
		}
	}
	return resolved, nil
}

func IsValidPublicHostname(host string) bool {
	if host == "" || strings.ContainsAny(host, " /\\@") {
		return false
	}
	if net.ParseIP(host) != nil {
		return true
	}
	if netpolicy.LooksLikeIPLiteral(host) {
		return false
	}
	lower := strings.ToLower(host)
	if !strings.Contains(lower, ".") ||
		strings.HasSuffix(lower, ".local") ||
		strings.HasSuffix(lower, ".internal") ||
		strings.HasSuffix(lower, ".localhost") ||
		lower == "metadata.google.internal" {
		return false
	}
	return true
}

func IsBlockedIP(ip net.IP) bool {
	address, ok := netip.AddrFromSlice(ip)
	if !ok {
		return true
	}
	address = address.Unmap()
	if !address.IsGlobalUnicast() {
		return true
	}
	for _, prefix := range blockedDestinationPrefixes {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

func validatePort(raw string, secure bool) error {
	port, err := strconv.Atoi(raw)
	if err != nil || port <= 0 || port > 65535 {
		return errors.New("invalid destination port")
	}
	if secure && port != 443 {
		return errors.New("CONNECT is restricted to port 443")
	}
	if !secure && port != 80 {
		return errors.New("plain HTTP is restricted to port 80")
	}
	return nil
}

func ParseUpstreamProxy(raw string) (*url.URL, error) {
	proxyURL, err := url.Parse(strings.TrimSpace(raw))
	if err != nil ||
		proxyURL.Hostname() == "" ||
		(proxyURL.Scheme != "http" && proxyURL.Scheme != "https") ||
		(proxyURL.Path != "" && proxyURL.Path != "/") ||
		proxyURL.RawQuery != "" ||
		proxyURL.Fragment != "" {
		return nil, errors.New("OPENLINKER_EGRESS_UPSTREAM_PROXY must be an HTTP or HTTPS proxy URL")
	}
	return proxyURL, nil
}

func (g *Gateway) connectThroughUpstream(ctx context.Context, target string) (net.Conn, error) {
	port := g.upstream.Port()
	if port == "" {
		port = map[bool]string{true: "443", false: "80"}[g.upstream.Scheme == "https"]
	}
	conn, err := g.dialContext(
		ctx,
		"tcp",
		net.JoinHostPort(g.upstream.Hostname(), port),
	)
	if err != nil {
		return nil, err
	}
	if g.upstream.Scheme == "https" {
		tlsConn := tls.Client(conn, &tls.Config{
			MinVersion: tls.VersionTLS12,
			ServerName: g.upstream.Hostname(),
		})
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			conn.Close()
			return nil, err
		}
		conn = tlsConn
	}
	request := &http.Request{
		Method: http.MethodConnect,
		URL:    &url.URL{Opaque: target},
		Host:   target,
		Header: make(http.Header),
	}
	if g.upstream.User != nil {
		password, _ := g.upstream.User.Password()
		token := base64.StdEncoding.EncodeToString(
			[]byte(g.upstream.User.Username() + ":" + password),
		)
		request.Header.Set("Proxy-Authorization", "Basic "+token)
	}
	if err := request.Write(conn); err != nil {
		conn.Close()
		return nil, err
	}
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, request)
	if err != nil {
		conn.Close()
		return nil, err
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		conn.Close()
		return nil, errors.New("upstream proxy CONNECT failed")
	}
	return &bufferedConn{Conn: conn, reader: reader}, nil
}

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) {
	return c.reader.Read(p)
}

func ResolveSecret(name string) (string, error) {
	direct := strings.TrimSpace(os.Getenv(name))
	file := strings.TrimSpace(os.Getenv(name + "_FILE"))
	if direct != "" && file != "" {
		return "", fmt.Errorf("%s and %s_FILE are mutually exclusive", name, name)
	}
	if file == "" {
		return direct, nil
	}
	info, err := os.Lstat(file)
	if err != nil ||
		info.Mode()&os.ModeSymlink != 0 ||
		!info.Mode().IsRegular() ||
		info.Mode().Perm()&0o077 != 0 ||
		info.Size() <= 0 ||
		info.Size() > maxGatewaySecretBytes {
		return "", fmt.Errorf("%s_FILE must be an owner-only regular non-symlink file", name)
	}
	if err := validateSecretFileOwner(info); err != nil {
		return "", fmt.Errorf("%s_FILE %w", name, err)
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		return "", fmt.Errorf("read %s_FILE", name)
	}
	value := strings.TrimSuffix(string(raw), "\n")
	value = strings.TrimSuffix(value, "\r")
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("%s_FILE is empty", name)
	}
	return value, nil
}

func removeHopHeaders(header http.Header) {
	for _, key := range []string{
		"Connection",
		"Proxy-Connection",
		"Keep-Alive",
		"Proxy-Authenticate",
		"Proxy-Authorization",
		"Te",
		"Trailer",
		"Transfer-Encoding",
		"Upgrade",
	} {
		header.Del(key)
	}
}

func copyHeaders(destination, source http.Header) {
	for key, values := range source {
		for _, value := range values {
			destination.Add(key, value)
		}
	}
}

func safeHost(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	if len(host) > 200 {
		return host[:200]
	}
	return host
}
