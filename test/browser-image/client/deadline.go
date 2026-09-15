//go:build !windows

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
	call := func(id int, arguments map[string]any) (map[string]any, error) {
		request, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/call", "params": map[string]any{"name": "browser_session", "arguments": arguments}})
		if err != nil {
			return nil, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		var output bytes.Buffer
		if err := server.Serve(ctx, bytes.NewReader(append(request, '\n')), &output); err != nil {
			return nil, err
		}
		var response struct {
			ID     int            `json:"id"`
			Result map[string]any `json:"result"`
			Error  any            `json:"error"`
		}
		if err := json.Unmarshal(output.Bytes(), &response); err != nil {
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
