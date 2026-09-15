//go:build !windows

package browserclient

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserprotocol"
)

// Runtime stops the engine before returning a deadline failure. Its response
// therefore arrives after the action deadline, even on a healthy local socket.
func TestExecuteReceivesDeadlineFailureAfterActionCleanup(t *testing.T) {
	config, _ := writeClientConfig(t, time.Now().UTC(), time.Minute)
	config.Timeout = time.Second
	config.Now = time.Now
	listener, err := net.Listen("unix", config.SocketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 2; i++ {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			var request browserprotocol.Request
			if json.NewDecoder(connection).Decode(&request) != nil {
				connection.Close()
				return
			}
			response := browserprotocol.SuccessResponse(request.RequestID, browserprotocol.Observation{PageStateID: "recovered", Viewport: &browserprotocol.Viewport{Width: browserprotocol.BrowserViewportWidth, Height: browserprotocol.BrowserViewportHeight}, NavigationGeneration: 1})
			if i == 0 {
				time.Sleep(time.Until(request.Deadline) + 50*time.Millisecond)
				failure := browserprotocol.NewFailure(browserprotocol.ErrorDeadlineExceeded, "action exceeded its deadline", true)
				response = browserprotocol.ErrorResponse(request.RequestID, failure)
			}
			_ = json.NewEncoder(connection).Encode(response)
			connection.Close()
		}
	}()
	client, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	_, failure := client.Execute(context.Background(), browserprotocol.Action{Kind: browserprotocol.ActionScreenshot})
	if failure == nil || failure.Code != browserprotocol.ErrorDeadlineExceeded || failure.Message != "action exceeded its deadline" {
		t.Errorf("deadline response was lost: %#v", failure)
	}
	observation, failure := client.Execute(context.Background(), browserprotocol.Action{Kind: browserprotocol.ActionScreenshot})
	if failure != nil || observation.PageStateID != "recovered" {
		t.Errorf("next action: %#v, %v", observation, failure)
	}
	<-done
}

func TestActionResponseGraceIsBoundedAndDoesNotDelayCancellation(t *testing.T) {
	for _, mode := range []string{"unresponsive runtime", "caller deadline", "caller cancellation"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			config, _ := writeClientConfig(t, time.Now().UTC(), time.Minute)
			config.Timeout, config.Now = time.Second, time.Now
			listener, err := net.Listen("unix", config.SocketPath)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			ctx, cancel := context.WithCancel(context.Background())
			if mode == "caller deadline" {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), 100*time.Millisecond)
			}
			defer cancel()
			release := make(chan struct{})
			defer close(release)
			go func() {
				connection, err := listener.Accept()
				if err != nil {
					return
				}
				defer connection.Close()
				var request browserprotocol.Request
				if json.NewDecoder(connection).Decode(&request) != nil {
					return
				}
				if mode == "caller cancellation" {
					cancel()
				}
				<-release
			}()
			client, err := New(config)
			if err != nil {
				t.Fatal(err)
			}
			started := time.Now()
			_, failure := client.Execute(ctx, browserprotocol.Action{Kind: browserprotocol.ActionScreenshot})
			elapsed := time.Since(started)
			if failure == nil {
				t.Fatal("unresponsive Runtime returned success")
			}
			if mode == "unresponsive runtime" {
				if failure.Code != browserprotocol.ErrorRuntimeUnavailable || elapsed < config.Timeout || elapsed > 7*time.Second {
					t.Fatalf("unbounded/incorrect transport failure after %v: %v", elapsed, failure)
				}
			} else if elapsed > time.Second {
				t.Fatalf("cancellation waited for response grace: %v", elapsed)
			}
			if mode == "caller deadline" && failure.Code != browserprotocol.ErrorDeadlineExceeded {
				t.Fatal(failure)
			}
		})
	}
}
