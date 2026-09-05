//go:build linux

package main

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	codexAuthProxyStageEnv    = "OPENLINKER_CODEX_AUTH_PROXY_STAGE"
	codexAuthProxyUpstreamEnv = "OPENLINKER_CODEX_AUTH_PROXY_UPSTREAM"
	codexAuthProxySecretEnv   = "OPENLINKER_CODEX_AUTH_PROXY_SECRET"
	codexAuthProxyLocalEnv    = "OPENLINKER_CODEX_AUTH_PROXY_LOCAL_AUTH"
	codexLocalAuthEnv         = "OPENLINKER_LOCAL_CODEX_AUTH"
	providerExecStageEnv      = "OPENLINKER_PROVIDER_EXEC_STAGE"
)

type codexProxyLaunch struct {
	argv        []string
	environment []string
	proxy       *exec.Cmd
}

func prepareCodexAuthProxy(argv, environment []string) (*codexProxyLaunch, error) {
	upstream, ok := codexProviderArgument(argv, "model_providers.openlinker_proxy.base_url")
	if !ok {
		return nil, nil
	}
	parsed, err := url.Parse(upstream)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, errors.New("Codex custom Provider Base URL is invalid")
	}
	secret := environmentValue(environment, "CODEX_API_KEY")
	if strings.TrimSpace(secret) == "" {
		return nil, errors.New("CODEX_API_KEY is required for the Codex custom Provider")
	}
	localAuth, err := newCodexLocalAuthToken()
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, errors.New("start local Codex credential proxy listener")
	}
	tcpListener, ok := listener.(*net.TCPListener)
	if !ok {
		_ = listener.Close()
		return nil, errors.New("local Codex credential proxy listener is not TCP")
	}
	listenerAddress := tcpListener.Addr().String()
	listenerFile, err := tcpListener.File()
	_ = listener.Close()
	if err != nil {
		return nil, errors.New("prepare local Codex credential proxy listener")
	}
	executable, err := os.Executable()
	if err != nil {
		_ = listenerFile.Close()
		return nil, errors.New("resolve Provider launcher executable")
	}
	proxyCommand := exec.Command(executable)
	proxyCommand.ExtraFiles = []*os.File{listenerFile}
	proxyCommand.Env = codexAuthProxyEnvironment(environment, upstream, secret, localAuth)
	proxyCommand.Stdin = nil
	proxyCommand.Stdout = nil
	proxyCommand.Stderr = os.Stderr
	proxyCommand.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
	if err := proxyCommand.Start(); err != nil {
		_ = listenerFile.Close()
		return nil, errors.New("start local Codex credential proxy")
	}
	_ = listenerFile.Close()

	// The listener is loopback-only and the credential never crosses this
	// connection. Plain HTTP avoids relying on Provider-specific custom CA
	// behavior while the upstream connection remains HTTPS.
	localBaseURL := "http://" + listenerAddress
	rewritten := rewriteCodexProviderArguments(argv, localBaseURL, codexLocalAuthEnv)
	providerEnvironment := setEnvironmentValue(codexProviderEnvironment(environment, localAuth), "NO_PROXY", "127.0.0.1,localhost")
	providerEnvironment = setEnvironmentValue(providerEnvironment, "no_proxy", "127.0.0.1,localhost")
	return &codexProxyLaunch{
		argv: rewritten, environment: providerEnvironment, proxy: proxyCommand,
	}, nil
}

func runCodexWithAuthProxy(launch *codexProxyLaunch) int {
	executable, err := os.Executable()
	if err != nil {
		stopProxyCommand(launch.proxy)
		return 1
	}
	command := exec.Command(executable, launch.argv[1:]...)
	command.Env = append(removeEnvironment(launch.environment, providerExecStageEnv), providerExecStageEnv+"=1")
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	command.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
	err = command.Run()
	stopProxyCommand(launch.proxy)
	if err == nil {
		return 0
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		if status, ok := exitError.Sys().(syscall.WaitStatus); ok && status.Exited() {
			return status.ExitStatus()
		}
	}
	return 1
}

func stopProxyCommand(command *exec.Cmd) {
	if command == nil || command.Process == nil {
		return
	}
	_ = command.Process.Signal(syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		_ = command.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = command.Process.Kill()
		<-done
	}
}

func codexProviderArgument(argv []string, key string) (string, bool) {
	prefix := key + "="
	for _, argument := range argv {
		if !strings.HasPrefix(argument, prefix) {
			continue
		}
		value := strings.TrimPrefix(argument, prefix)
		decoded, err := strconv.Unquote(value)
		if err != nil {
			return "", false
		}
		return decoded, true
	}
	return "", false
}

func rewriteCodexProviderArguments(argv []string, localBaseURL, authEnv string) []string {
	rewritten := append([]string(nil), argv...)
	for index, argument := range rewritten {
		switch {
		case strings.HasPrefix(argument, "model_providers.openlinker_proxy.base_url="):
			rewritten[index] = fmt.Sprintf("model_providers.openlinker_proxy.base_url=%q", localBaseURL)
		case strings.HasPrefix(argument, "model_providers.openlinker_proxy.env_key="):
			rewritten[index] = fmt.Sprintf("model_providers.openlinker_proxy.env_key=%q", authEnv)
		}
	}
	return rewritten
}

func codexProviderEnvironment(environment []string, localAuth string) []string {
	clean := removeEnvironment(environment,
		"CODEX_API_KEY",
		"CODEX_API_KEY_FILE",
		"OPENLINKER_AGENT_TOKEN",
		"OPENLINKER_AGENT_TOKEN_FILE",
		codexAuthProxyStageEnv,
		codexAuthProxyUpstreamEnv,
		codexAuthProxySecretEnv,
		codexAuthProxyLocalEnv,
		codexLocalAuthEnv,
		providerExecStageEnv,
	)
	return append(clean, codexLocalAuthEnv+"="+localAuth)
}

func codexAuthProxyEnvironment(environment []string, upstream, secret, localAuth string) []string {
	allowed := map[string]bool{
		"PATH": true, "HOME": true, "HTTP_PROXY": true, "HTTPS_PROXY": true,
		"http_proxy": true, "https_proxy": true, "NO_PROXY": true, "no_proxy": true,
		"SSL_CERT_FILE": true,
	}
	clean := make([]string, 0, len(environment)+3)
	for _, item := range environment {
		key, _, ok := strings.Cut(item, "=")
		if ok && allowed[key] {
			clean = append(clean, item)
		}
	}
	return append(clean,
		codexAuthProxyStageEnv+"=1",
		codexAuthProxyUpstreamEnv+"="+upstream,
		codexAuthProxySecretEnv+"="+secret,
		codexAuthProxyLocalEnv+"="+localAuth,
	)
}

func newCodexLocalAuthToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", errors.New("generate local Codex credential proxy authorization")
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func setEnvironmentValue(environment []string, name, value string) []string {
	clean := removeEnvironment(environment, name)
	return append(clean, name+"="+value)
}

func removeEnvironment(environment []string, keys ...string) []string {
	blocked := make(map[string]bool, len(keys))
	for _, key := range keys {
		blocked[key] = true
	}
	clean := make([]string, 0, len(environment))
	for _, item := range environment {
		key, _, ok := strings.Cut(item, "=")
		if ok && blocked[key] {
			continue
		}
		clean = append(clean, item)
	}
	return clean
}

func environmentValue(environment []string, name string) string {
	prefix := name + "="
	for _, item := range environment {
		if strings.HasPrefix(item, prefix) {
			return strings.TrimPrefix(item, prefix)
		}
	}
	return ""
}

func runCodexAuthProxyStage() error {
	if os.Geteuid() != runtimeUID || os.Getegid() != runtimeGID {
		return errors.New("Codex credential proxy must run as the fixed Runtime UID/GID")
	}
	upstreamValue := strings.TrimSpace(os.Getenv(codexAuthProxyUpstreamEnv))
	secret := strings.TrimSpace(os.Getenv(codexAuthProxySecretEnv))
	localAuth := strings.TrimSpace(os.Getenv(codexAuthProxyLocalEnv))
	if upstreamValue == "" || secret == "" || localAuth == "" {
		return errors.New("Codex credential proxy configuration is incomplete")
	}
	upstream, err := url.Parse(upstreamValue)
	if err != nil || upstream.Host == "" || (upstream.Scheme != "http" && upstream.Scheme != "https") {
		return errors.New("Codex credential proxy upstream is invalid")
	}
	listenerFile := os.NewFile(3, "codex-auth-proxy-listener")
	if listenerFile == nil {
		return errors.New("Codex credential proxy listener is missing")
	}
	listener, err := net.FileListener(listenerFile)
	_ = listenerFile.Close()
	if err != nil {
		return errors.New("open Codex credential proxy listener")
	}
	defer listener.Close()
	server := &http.Server{
		Handler:           newCodexAuthProxyHandler(upstream, secret, localAuth),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       90 * time.Second,
		ErrorLog:          log.New(os.Stderr, "openlinker Codex credential proxy: ", 0),
	}
	err = server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func newCodexAuthProxyHandler(upstream *url.URL, secret, localAuth string) http.Handler {
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	originalDirector := proxy.Director
	proxy.Director = func(request *http.Request) {
		originalDirector(request)
		request.Host = upstream.Host
		request.Header.Set("Authorization", "Bearer "+secret)
		request.Header.Del("Proxy-Authorization")
	}
	proxy.Transport = &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          16,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	proxy.ModifyResponse = func(response *http.Response) error {
		sanitizeCodexProxyResponseHeaders(response.Header)
		response.Body = newResponseCompletionObserver(response.Body, response.StatusCode)
		return nil
	}
	proxy.ErrorHandler = func(writer http.ResponseWriter, _ *http.Request, err error) {
		fmt.Fprintf(os.Stderr, "openlinker Codex credential proxy: upstream request failed: %T\n", err)
		http.Error(writer, "provider upstream unavailable", http.StatusBadGateway)
	}
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Host == "" || request.URL.IsAbs() {
			http.Error(writer, "invalid provider request", http.StatusBadRequest)
			return
		}
		if request.Method != http.MethodPost || request.URL.Path != "/responses" {
			http.Error(writer, "provider request is not allowed", http.StatusNotFound)
			return
		}
		if request.Header.Get("Authorization") != "Bearer "+localAuth {
			http.Error(writer, "provider request is not authorized", http.StatusUnauthorized)
			return
		}
		request.Body = http.MaxBytesReader(writer, request.Body, 32<<20)
		proxy.ServeHTTP(writer, request)
	})
}

func sanitizeCodexProxyResponseHeaders(header http.Header) {
	// These headers describe the upstream origin rather than this loopback
	// credential proxy. In particular, forwarding Alt-Svc can make a capable
	// client reconnect to the upstream's advertised port on 127.0.0.1.
	for _, name := range []string{"Alt-Svc", "NEL", "Report-To"} {
		header.Del(name)
	}
	for name := range header {
		if strings.HasPrefix(strings.ToLower(name), "cf-") {
			header.Del(name)
		}
	}
}

type responseCompletionObserver struct {
	body       io.ReadCloser
	statusCode int
	bytesRead  int64
	tail       []byte
	completed  bool
	finished   bool
}

var responseCompletedMarker = []byte(`"type":"response.completed"`)

func newResponseCompletionObserver(body io.ReadCloser, statusCode int) io.ReadCloser {
	return &responseCompletionObserver{body: body, statusCode: statusCode}
}

func (observer *responseCompletionObserver) Read(buffer []byte) (int, error) {
	count, err := observer.body.Read(buffer)
	observer.bytesRead += int64(count)
	if count > 0 && !observer.completed {
		window := append(append([]byte(nil), observer.tail...), buffer[:count]...)
		observer.completed = bytes.Contains(window, responseCompletedMarker)
		keep := len(responseCompletedMarker) - 1
		if len(window) > keep {
			window = window[len(window)-keep:]
		}
		observer.tail = window
	}
	if err != nil {
		observer.finish(err)
	}
	return count, err
}

func (observer *responseCompletionObserver) Close() error {
	observer.finish(errors.New("body closed"))
	return observer.body.Close()
}

func (observer *responseCompletionObserver) finish(err error) {
	if observer.finished {
		return
	}
	observer.finished = true
	if observer.completed {
		return
	}
	errLabel := "none"
	if err != nil {
		errLabel = strings.ReplaceAll(err.Error(), "\n", " ")
	}
	fmt.Fprintf(os.Stderr,
		"openlinker Codex credential proxy: incomplete upstream stream status=%d bytes=%d completed=%t error=%s\n",
		observer.statusCode, observer.bytesRead, observer.completed, errLabel,
	)
}
