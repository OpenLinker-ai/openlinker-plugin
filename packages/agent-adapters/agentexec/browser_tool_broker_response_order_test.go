//go:build !windows

package agentexec

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"testing"
	"time"
)

// The production MCP server executes requests concurrently. A higher response ID
// does not imply that earlier requests have completed.
func TestBrokerMCPProviderWaitsForEveryResponse(t *testing.T) {
	tests := []struct {
		order   []int
		wantErr bool
	}{
		{order: []int{1, 2, 3}}, {order: []int{1, 3, 2}},
		{order: []int{2, 1, 3}}, {order: []int{2, 3, 1}},
		{order: []int{3, 1, 2}}, {order: []int{3, 2, 1}},
		{order: []int{1, 3}, wantErr: true},
		{order: []int{1, 1, 3}, wantErr: true},
		{order: []int{1, 4, 3}, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(fmt.Sprint(tc.order), func(t *testing.T) {
			socket := filepath.Join(shortBrowserTestRoot(t), "fixture.sock")
			listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = listener.Close() })
			if err := listener.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				done <- func() error {
					connection, err := listener.AcceptUnix()
					if err != nil {
						return err
					}
					defer connection.Close()
					if err := connection.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
						return err
					}
					scanner := bufio.NewScanner(connection)
					for range 3 {
						if !scanner.Scan() {
							return fmt.Errorf("fixture did not receive all requests: %v", scanner.Err())
						}
					}
					evidenceSent := false
					for _, id := range tc.order {
						result := map[string]any{"serverInfo": map[string]any{"version": "ordered-test"}}
						if id != 1 {
							result = map[string]any{
								"content": []map[string]any{{"type": "image"}},
								"structuredContent": map[string]any{
									"page_state_id": "page-state-1", "origin": "https://example.com",
								},
							}
							if !evidenceSent {
								result["structuredContent"].(map[string]any)["attachment_evidence"] = map[string]any{}
								evidenceSent = true
							}
						}
						if err := json.NewEncoder(connection).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result}); err != nil {
							return err
						}
					}
					return nil
				}()
			}()
			provider := &brokerMCPProvider{}
			_, runErr := provider.Run(context.Background(), RunContext{Browser: &BrowserRunContext{ToolSocket: socket}})
			if (runErr != nil) != tc.wantErr {
				t.Fatalf("Run error = %v, want error: %v", runErr, tc.wantErr)
			}
			if !tc.wantErr && (!provider.observed || provider.serverVersion != "ordered-test") {
				t.Fatalf("provider returned before all responses: observed=%v version=%q", provider.observed, provider.serverVersion)
			}
			select {
			case err := <-done:
				if err != nil && !tc.wantErr {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("MCP response fixture did not stop")
			}
		})
	}
}
