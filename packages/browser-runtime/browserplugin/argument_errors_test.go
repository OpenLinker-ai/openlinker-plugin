package browserplugin

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserprotocol"
)

func TestMCPArgumentErrorsAreActionableAndConnectionRecovers(t *testing.T) {
	nine := make([]map[string]any, 9)
	for i := range nine {
		nine[i] = map[string]any{"kind": "wait", "duration_ms": 5000}
	}
	cases := []struct {
		name    string
		args    map[string]any
		message string
	}{
		{"observe with actions", map[string]any{"operation": "observe", "actions": nine[:1]}, "does not accept actions"},
		{"nine actions", map[string]any{"operation": "act", "actions": nine}, "one to eight actions"},
		{"mixed restricted batch", map[string]any{"operation": "act", "actions": []map[string]any{{"kind": "navigate", "url": "https://example.com"}, nine[0]}}, "only scroll, wait, and screenshot"},
		{"empty act", map[string]any{"operation": "act"}, "one to eight actions"},
		{"unknown operation", map[string]any{"operation": "secret-shaped-user-input"}, "operation is invalid"},
		{"invalid observation", map[string]any{"operation": "observe", "observation": "none"}, "observation mode is invalid"},
		{"wrong type", map[string]any{"operation": 42}, "arguments are invalid"},
		{"caller authority", map[string]any{"operation": "observe", "run_id": "secret-shaped-user-input"}, "arguments are invalid"},
	}
	for _, host := range []string{"codex", "claude"} {
		t.Run(host, func(t *testing.T) {
			executor := &fakeExecutor{observation: browserprotocol.Observation{PageStateID: "recovered"}}
			server := &Server{Host: host, IO: IO{Getenv: func(string) string { return "" }}, ClientFactory: func() (Executor, error) { return executor, nil }}
			client, transport := net.Pipe()
			defer transport.Close()
			if err := client.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- server.Serve(context.Background(), transport, transport) }()
			defer func() {
				client.Close()
				if err := <-done; err != nil {
					t.Error(err)
				}
			}()
			encoder, decoder := json.NewEncoder(client), json.NewDecoder(client)
			call := func(id int, args map[string]any) toolResult {
				t.Helper()
				request := map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/call", "params": map[string]any{"name": "browser_session", "arguments": args}}
				if err := encoder.Encode(request); err != nil {
					t.Fatal(err)
				}
				var response struct {
					ID     int        `json:"id"`
					Result toolResult `json:"result"`
					Error  *rpcError  `json:"error"`
				}
				if err := decoder.Decode(&response); err != nil {
					t.Fatal(err)
				}
				if response.ID != id || response.Error != nil {
					t.Fatalf("unexpected RPC response: %#v", response)
				}
				return response.Result
			}
			for i, tc := range cases {
				result := call(i+1, tc.args)
				if !result.IsError || result.StructuredContent.(map[string]any)["code"] != string(browserprotocol.ErrorProtocolInvalid) {
					t.Fatalf("%s: %#v", tc.name, result)
				}
				message, _ := result.StructuredContent.(map[string]any)["message"].(string)
				if !strings.Contains(message, tc.message) {
					t.Fatalf("%s: unhelpful message %q", tc.name, message)
				}
				if len(result.Content) != 1 || result.Content[0].Text != string(browserprotocol.ErrorProtocolInvalid)+": "+message {
					t.Fatalf("%s: text-only clients lost error details", tc.name)
				}
				if strings.Contains(message, "secret-shaped-user-input") {
					t.Fatal("error reflected raw caller data")
				}
			}
			executor.mu.Lock()
			count := len(executor.actions)
			executor.mu.Unlock()
			if count != 0 {
				t.Fatalf("invalid arguments executed %d actions", count)
			}
			if result := call(len(cases)+1, map[string]any{"operation": "observe"}); result.IsError || result.StructuredContent.(map[string]any)["page_state_id"] != "recovered" {
				t.Fatalf("same-connection recovery failed: %#v", result)
			}
		})
	}
}

func TestUnexpectedBrowserErrorsRemainPrivate(t *testing.T) {
	result := browserErrorResult(errors.New("private runtime path and secret-shaped-user-input"))
	if result.StructuredContent.(map[string]any)["code"] != browserprotocol.ErrorInternal || result.StructuredContent.(map[string]any)["message"] != "Browser tool failed" {
		t.Fatalf("unexpected internal error disclosure: %#v", result)
	}
}
