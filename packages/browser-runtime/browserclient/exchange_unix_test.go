//go:build !windows

package browserclient

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserprotocol"
)

func TestExecuteSendsCredentialAndLeaseIdentityOverUDS(t *testing.T) {
	now := time.Now().UTC()
	config, identity := writeClientConfig(t, now, time.Minute)
	listener, err := net.Listen("unix", config.SocketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	requests := make(chan browserprotocol.Request, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer connection.Close()
		var request browserprotocol.Request
		if json.NewDecoder(connection).Decode(&request) != nil {
			return
		}
		requests <- request
		_ = json.NewEncoder(connection).Encode(browserprotocol.SuccessResponse(
			request.RequestID,
			browserprotocol.Observation{
				PageStateID: "page-state-1",
				Viewport: &browserprotocol.Viewport{
					Width:  browserprotocol.BrowserViewportWidth,
					Height: browserprotocol.BrowserViewportHeight,
				},
				NavigationGeneration: 1,
				Origin:               "https://example.com",
				Title:                "Example",
			},
		))
	}()
	client, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	observation, failure := client.Execute(
		context.Background(),
		browserprotocol.Action{Kind: browserprotocol.ActionScreenshot},
	)
	if failure != nil {
		t.Fatal(failure)
	}
	if observation.PageStateID != "page-state-1" {
		t.Fatalf("observation = %#v", observation)
	}
	request := <-requests
	if !browserprotocol.SameIdentity(request.Identity, identity) ||
		request.ChannelCredential != strings.Repeat("c", 32) ||
		request.ContractID != browserprotocol.ContractID ||
		request.Action.Kind != browserprotocol.ActionScreenshot {
		t.Fatalf("request = %#v", request)
	}
}

func TestExecuteRejectsMismatchedAndOversizedResponses(t *testing.T) {
	now := time.Now().UTC()
	tests := []struct {
		name     string
		response func(browserprotocol.Request) any
		wantCode browserprotocol.ErrorCode
	}{
		{
			name: "request identity",
			response: func(request browserprotocol.Request) any {
				response := browserprotocol.SuccessResponse(
					"55555555-5555-4555-8555-555555555555",
					browserprotocol.Observation{PageStateID: "state"},
				)
				return response
			},
			wantCode: browserprotocol.ErrorOutputInvalid,
		},
		{
			name: "oversized",
			response: func(browserprotocol.Request) any {
				return map[string]any{"padding": strings.Repeat("x", browserprotocol.MaxResponseBytes)}
			},
			wantCode: browserprotocol.ErrorOutputTooLarge,
		},
		{
			name: "unknown failure code",
			response: func(request browserprotocol.Request) any {
				return map[string]any{
					"contract_id": browserprotocol.ContractID,
					"request_id":  request.RequestID,
					"status":      "error",
					"error": map[string]any{
						"code":        "PROVIDER_CONTROLLED_CODE",
						"message":     "untrusted",
						"recoverable": false,
					},
				}
			},
			wantCode: browserprotocol.ErrorOutputInvalid,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config, _ := writeClientConfig(t, now, time.Minute)
			listener, err := net.Listen("unix", config.SocketPath)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			go func() {
				connection, acceptErr := listener.Accept()
				if acceptErr != nil {
					return
				}
				defer connection.Close()
				var request browserprotocol.Request
				_ = json.NewDecoder(connection).Decode(&request)
				_ = json.NewEncoder(connection).Encode(test.response(request))
			}()
			client, err := New(config)
			if err != nil {
				t.Fatal(err)
			}
			_, failure := client.Execute(
				context.Background(),
				browserprotocol.Action{Kind: browserprotocol.ActionScreenshot},
			)
			if failure == nil || failure.Code != test.wantCode {
				t.Fatalf("failure = %#v, want %s", failure, test.wantCode)
			}
		})
	}
}

func TestExecuteCancellationInterruptsIdleRuntime(t *testing.T) {
	now := time.Now().UTC()
	config, _ := writeClientConfig(t, now, time.Minute)
	listener, err := net.Listen("unix", config.SocketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer connection.Close()
		var request browserprotocol.Request
		_ = json.NewDecoder(connection).Decode(&request)
		close(accepted)
		<-release
	}()
	client, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan *browserprotocol.Failure, 1)
	go func() {
		_, failure := client.Execute(
			ctx,
			browserprotocol.Action{Kind: browserprotocol.ActionScreenshot},
		)
		done <- failure
	}()
	<-accepted
	cancel()
	select {
	case failure := <-done:
		if failure == nil || failure.Code != browserprotocol.ErrorCanceled {
			t.Fatalf("failure = %#v", failure)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled Browser request did not return")
	}
}

func TestExecuteErrorsNeverContainCredential(t *testing.T) {
	now := time.Now().UTC()
	config, _ := writeClientConfig(t, now, time.Minute)
	if err := os.Remove(config.SocketPath); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	client, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	_, failure := client.Execute(
		context.Background(),
		browserprotocol.Action{Kind: browserprotocol.ActionScreenshot},
	)
	if failure == nil || strings.Contains(failure.Message, strings.Repeat("c", 32)) {
		t.Fatalf("failure = %#v", failure)
	}
}

func TestExchangeClassifiesTransportAndMalformedResponses(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		response []byte
		holdOpen bool
		wantCode browserprotocol.ErrorCode
	}{
		{
			name:     "zero-byte EOF",
			wantCode: browserprotocol.ErrorRuntimeUnavailable,
		},
		{
			name:     "partial JSON",
			response: []byte("{"),
			wantCode: browserprotocol.ErrorOutputInvalid,
		},
		{
			name:     "socket timeout",
			holdOpen: true,
			wantCode: browserprotocol.ErrorRuntimeUnavailable,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir, err := os.MkdirTemp("", "olbc-x-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(dir) })
			socketPath := filepath.Join(dir, "browser.sock")
			listener, err := net.Listen("unix", socketPath)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			release := make(chan struct{})
			if !test.holdOpen {
				close(release)
			} else {
				defer close(release)
			}
			go func() {
				connection, acceptErr := listener.Accept()
				if acceptErr != nil {
					return
				}
				defer connection.Close()
				var request browserprotocol.Request
				_ = json.NewDecoder(connection).Decode(&request)
				if len(test.response) > 0 {
					_, _ = connection.Write(test.response)
				}
				<-release
			}()
			request := browserprotocol.Request{
				ContractID:        browserprotocol.ContractID,
				ChannelCredential: strings.Repeat("a", 64),
				RequestID:         "11111111-1111-4111-8111-111111111111",
			}
			_, failure := exchange(
				context.Background(),
				socketPath,
				request,
				time.Now().Add(100*time.Millisecond),
			)
			if failure == nil || failure.Code != test.wantCode {
				t.Fatalf("failure = %#v, want %s", failure, test.wantCode)
			}
		})
	}
}
