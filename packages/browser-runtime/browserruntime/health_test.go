//go:build !windows

package browserruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserclient"
	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserprotocol"
)

func TestHealthCheckIsAuthenticatedAndDoesNotExecuteEngine(t *testing.T) {
	t.Parallel()
	engine := &fakeEngine{}
	server, socketPath, cancel, done := startTestServer(t, engine, ServerOptions{})
	defer stopTestServer(t, server, cancel, done)

	if err := browserclient.CheckHealth(
		context.Background(),
		socketPath,
		strings.Repeat("a", 64),
		time.Second,
	); err != nil {
		t.Fatal(err)
	}
	if engine.calls.Load() != 0 {
		t.Fatalf("health check executed Engine %d times", engine.calls.Load())
	}
	if err := browserclient.CheckHealth(
		context.Background(),
		socketPath,
		strings.Repeat("b", 64),
		time.Second,
	); err == nil {
		t.Fatal("health check accepted an invalid credential")
	}
}

func TestHealthCheckTimesOutBehindBlockedActionAndRecovers(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	release := make(chan struct{})
	engine := &fakeEngine{run: func(context.Context) (
		browserprotocol.Observation,
		*browserprotocol.Failure,
	) {
		close(started)
		<-release
		return browserprotocol.Observation{
			PageStateID: "state-1",
			Viewport: &browserprotocol.Viewport{
				Width:  browserprotocol.BrowserViewportWidth,
				Height: browserprotocol.BrowserViewportHeight,
			},
			NavigationGeneration: 1,
		}, nil
	}}
	server, socketPath, cancel, done := startTestServer(t, engine, ServerOptions{})
	defer stopTestServer(t, server, cancel, done)

	requestResult := make(chan browserprotocol.Response, 1)
	go func() {
		requestResult <- sendRequest(t, socketPath, validRuntimeRequest())
	}()
	<-started
	if err := browserclient.CheckHealth(
		context.Background(),
		socketPath,
		strings.Repeat("a", 64),
		100*time.Millisecond,
	); err == nil {
		t.Fatal("health check succeeded while the Server slot was blocked")
	}
	close(release)
	if response := <-requestResult; response.Status != "ok" {
		t.Fatalf("action response = %#v", response)
	}
	if err := browserclient.CheckHealth(
		context.Background(),
		socketPath,
		strings.Repeat("a", 64),
		time.Second,
	); err != nil {
		t.Fatalf("health did not recover after action completion: %v", err)
	}
}

func TestResponseWritersHandleShortWrites(t *testing.T) {
	t.Parallel()
	server := &Server{options: ServerOptions{MaxResponseBytes: browserprotocol.MaxResponseBytes}}
	writer := &boundedWriter{limit: 3}
	response := browserprotocol.ErrorResponse(
		"11111111-1111-4111-8111-111111111111",
		browserprotocol.NewFailure(
			browserprotocol.ErrorRuntimeUnavailable,
			"runtime unavailable",
			true,
		),
	)
	if err := server.writeResponse(writer, response); err != nil {
		t.Fatal(err)
	}
	var decoded browserprotocol.Response
	if err := json.NewDecoder(bytes.NewReader(writer.output.Bytes())).Decode(&decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.RequestID != response.RequestID || writer.calls < 2 {
		t.Fatalf("decoded=%#v calls=%d", decoded, writer.calls)
	}
	if err := writeAllResponse(zeroWriter{}, []byte("response")); err == nil ||
		!strings.Contains(err.Error(), io.ErrShortWrite.Error()) {
		t.Fatalf("zero writer error = %v", err)
	}
}

type boundedWriter struct {
	limit  int
	calls  int
	output bytes.Buffer
}

func (writer *boundedWriter) Write(value []byte) (int, error) {
	writer.calls++
	if len(value) > writer.limit {
		value = value[:writer.limit]
	}
	return writer.output.Write(value)
}

type zeroWriter struct{}

func (zeroWriter) Write([]byte) (int, error) { return 0, nil }
