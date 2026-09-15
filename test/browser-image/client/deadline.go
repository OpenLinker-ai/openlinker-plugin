//go:build !windows

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserclient"
	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserplugin"
	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserprotocol"
)

// Uses the real Runtime/Engine/Chrome image. MCP host labels exercise the same
// tool entry points without provider credentials or model-generated assertions.
func validateMCPDeadlineRecovery(client *browserclient.Client, host string) error {
	server := &browserplugin.Server{
		Host: host, IO: browserplugin.IO{Getenv: func(string) string { return "" }},
		ClientFactory: func() (browserplugin.Executor, error) { return client, nil },
		// Environment evidence was already verified by the caller's preflight.
		EvidenceSupplier: func() (browserplugin.EvidenceSnapshot, error) { return browserplugin.EvidenceSnapshot{}, nil },
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	input, send := io.Pipe()
	receive, output := io.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer output.Close()
		_ = server.Serve(ctx, input, output)
	}()
	defer func() {
		send.Close()
		receive.Close()
		cancel()
		<-done
	}()
	encoder, decoder := json.NewEncoder(send), json.NewDecoder(receive)
	call := func(id int, arguments map[string]any) (map[string]any, error) {
		// Keep stdin open until the response. EOF is a client disconnect and
		// correctly cancels an outstanding long-running tool call.
		timer := time.AfterFunc(30*time.Second, cancel)
		defer timer.Stop()
		if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/call", "params": map[string]any{"name": "browser_session", "arguments": arguments}}); err != nil {
			return nil, err
		}
		var response struct {
			ID     int            `json:"id"`
			Result map[string]any `json:"result"`
			Error  any            `json:"error"`
		}
		if err := decoder.Decode(&response); err != nil {
			return nil, err
		}
		if response.ID != id || response.Error != nil {
			return nil, fmt.Errorf("invalid MCP response for %d", id)
		}
		return response.Result, nil
	}
	actions := make([]map[string]any, 8)
	for i := range actions {
		actions[i] = map[string]any{"kind": "wait", "duration_ms": 5000}
	}
	started := time.Now()
	failure, err := call(1, map[string]any{"operation": "act", "actions": actions})
	if err != nil {
		return err
	}
	structured, _ := failure["structuredContent"].(map[string]any)
	if failure["isError"] != true || structured["code"] != string(browserprotocol.ErrorDeadlineExceeded) {
		return fmt.Errorf("expected deadline failure, got %v", structured["code"])
	}
	if elapsed := time.Since(started); elapsed < 19*time.Second || elapsed > 27*time.Second {
		return fmt.Errorf("deadline response took %v; expected the 20-second action budget plus bounded cleanup", elapsed)
	}
	recovered, err := call(2, map[string]any{"operation": "observe", "observation": "semantic"})
	if err != nil {
		return err
	}
	state, _ := recovered["structuredContent"].(map[string]any)
	if recovered["isError"] == true || state["status"] != "ok" || state["page_state_id"] == "" {
		return fmt.Errorf("next MCP action failed: %v", state["code"])
	}
	return nil
}
