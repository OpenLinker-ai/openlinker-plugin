//go:build !windows

package browserruntime

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserclient"
	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserplugin"
	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserprotocol"
)

// Only engine work is simulated: the MCP server, client, Unix socket, Runtime
// authorization, deadline and response serialization are production code.
func TestMCPDeliversRuntimeDeadlineAndAcceptsNextAction(t *testing.T) {
	for _, host := range []string{"codex", "claude"} {
		t.Run(host, func(t *testing.T) {
			engine := &fakeEngine{execute: func(ctx context.Context, identity browserprotocol.Identity, action browserprotocol.Action) (browserprotocol.Observation, *browserprotocol.Failure) {
				if action.Kind == browserprotocol.ActionBatch {
					if len(action.Actions) != 8 {
						t.Error("MCP split the batch")
					}
					<-ctx.Done()
					time.Sleep(50 * time.Millisecond) // engine cleanup after execution stops
					return browserprotocol.Observation{}, browserprotocol.NewFailure(browserprotocol.ErrorDeadlineExceeded, "action deadline elapsed", true)
				}
				return (&fakeEngine{}).Execute(ctx, identity, action)
			}}
			server, socket, stop, done := startTestServer(t, engine, ServerOptions{})
			defer stopTestServer(t, server, stop, done)
			dir := shortTempDir(t)
			credential := filepath.Join(dir, "credential")
			if err := os.WriteFile(credential, []byte(strings.Repeat("a", 64)), 0o600); err != nil {
				t.Fatal(err)
			}
			lease := filepath.Join(dir, "lease.json")
			writeActiveLeaseFixture(t, lease, validRuntimeRequest().Identity, time.Now().Add(time.Minute))
			client, err := browserclient.New(browserclient.Config{SocketPath: socket, CredentialFile: credential, LeaseFile: lease, Timeout: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			mcp := &browserplugin.Server{Host: host, IO: browserplugin.IO{Getenv: func(string) string { return "" }},
				ClientFactory:    func() (browserplugin.Executor, error) { return client, nil },
				EvidenceSupplier: func() (browserplugin.EvidenceSnapshot, error) { return browserplugin.EvidenceSnapshot{}, nil },
			}
			input, send := io.Pipe()
			receive, output := io.Pipe()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			mcpDone := make(chan error, 1)
			go func() { mcpDone <- mcp.Serve(ctx, input, output); output.Close() }()
			defer func() {
				send.Close()
				receive.Close()
				cancel()
				if err := <-mcpDone; err != nil {
					t.Error(err)
				}
			}()
			encoder, decoder := json.NewEncoder(send), json.NewDecoder(receive)
			call := func(id int, arguments map[string]any) map[string]any {
				t.Helper()
				if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/call", "params": map[string]any{"name": "browser_session", "arguments": arguments}}); err != nil {
					t.Fatal(err)
				}
				var response struct {
					ID     int            `json:"id"`
					Result map[string]any `json:"result"`
					Error  any            `json:"error"`
				}
				if err := decoder.Decode(&response); err != nil {
					t.Fatal(err)
				}
				if response.ID != id || response.Error != nil {
					t.Fatalf("MCP response: %#v", response)
				}
				return response.Result
			}
			actions := make([]map[string]any, 8)
			for i := range actions {
				actions[i] = map[string]any{"kind": "wait", "duration_ms": 5000}
			}
			failure := call(1, map[string]any{"operation": "act", "actions": actions})
			if failure["isError"] != true {
				t.Fatalf("expected tool error: %#v", failure)
			}
			structured, _ := failure["structuredContent"].(map[string]any)
			if structured["code"] != string(browserprotocol.ErrorDeadlineExceeded) {
				t.Fatalf("deadline was not delivered: %#v", structured)
			}
			recovered := call(2, map[string]any{"operation": "observe"})
			if recovered["isError"] == true {
				t.Fatalf("recovery failed: %#v", recovered)
			}
			if engine.calls.Load() != 2 {
				t.Fatalf("unexpected replay: %d engine calls", engine.calls.Load())
			}
		})
	}
}
