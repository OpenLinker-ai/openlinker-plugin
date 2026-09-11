package pluginbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OpenLinker-ai/openlinker-plugin/internal/pluginhost/shared"
	"github.com/OpenLinker-ai/openlinker-plugin/packages/agent-adapters/agent"
)

func TestServerInitializeAndToolListUseOnlyProtocolOutput(t *testing.T) {
	dir := t.TempDir()
	environment := map[string]string{"OPENLINKER_AGENT_CONFIG": filepath.Join(dir, "agent.json")}
	getenv := func(key string) string { return environment[key] }
	input := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}` + "\n" +
			`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n" +
			`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}` + "\n",
	)
	var stdout, stderr bytes.Buffer
	server := &Server{Host: "codex", IO: shared.IO{Getenv: getenv, Stderr: &stderr}, Agent: agent.NewService(getenv, nil, "test")}
	if err := server.Serve(context.Background(), input, &stdout); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("stdout = %q", stdout.String())
	}
	responses := map[float64]map[string]any{}
	for _, line := range lines {
		var response map[string]any
		if json.Unmarshal([]byte(line), &response) != nil {
			t.Fatalf("invalid JSON-RPC output: %s", stdout.String())
		}
		responses[response["id"].(float64)] = response
	}
	initialized, listed := responses[1], responses[2]
	if initialized == nil || listed == nil {
		t.Fatalf("missing JSON-RPC responses: %s", stdout.String())
	}
	result := listed["result"].(map[string]any)
	tools := result["tools"].([]any)
	if len(tools) != 14 {
		t.Fatalf("tools = %d, want 14", len(tools))
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected stderr: %s", stderr.String())
	}
}

func TestServerAcceptsCodexToolCallMetaAndRejectsOtherOuterFields(t *testing.T) {
	dir := t.TempDir()
	environment := map[string]string{"OPENLINKER_AGENT_CONFIG": filepath.Join(dir, "agent.json")}
	getenv := func(key string) string { return environment[key] }
	input := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"diagnose_agent_mode","arguments":{},"_meta":{"threadId":"thread-1","progressToken":1,"x-codex-turn-metadata":{"turnId":"turn-1"}}}}` + "\n" +
			`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"diagnose_agent_mode","arguments":{},"unexpected":true}}` + "\n",
	)
	var stdout bytes.Buffer
	server := &Server{Host: "codex", IO: shared.IO{Getenv: getenv}, Agent: agent.NewService(getenv, nil, "test")}
	if err := server.Serve(context.Background(), input, &stdout); err != nil {
		t.Fatal(err)
	}
	responses := map[float64]map[string]any{}
	for _, line := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
		var response map[string]any
		if err := json.Unmarshal([]byte(line), &response); err != nil {
			t.Fatalf("invalid JSON-RPC output %q: %v", line, err)
		}
		responses[response["id"].(float64)] = response
	}
	if responses[1]["error"] != nil || responses[1]["result"] == nil {
		t.Fatalf("Codex _meta tools/call failed: %#v", responses[1])
	}
	errorValue, ok := responses[2]["error"].(map[string]any)
	if !ok || errorValue["code"] != float64(-32602) {
		t.Fatalf("unknown outer field was not rejected: %#v", responses[2])
	}
}

func TestConfigureAgentModeSchemaIncludesProviderAndBrowserProfiles(t *testing.T) {
	for _, definition := range toolDefinitions() {
		if definition.Name != "configure_agent_mode" {
			continue
		}
		properties := definition.InputSchema["properties"].(map[string]any)
		for _, field := range []string{
			"codex_base_url",
			"execution_profile",
			"browser_interaction_policy",
			"browser_client_mode",
			"browser_native_plugin",
			"browser_plugin_bin",
			"browser_socket",
			"browser_credential_file",
			"browser_lease_root",
			"browser_broker_root",
		} {
			if _, exists := properties[field]; !exists {
				t.Fatalf("configure_agent_mode schema is missing %s", field)
			}
		}
		return
	}
	t.Fatal("configure_agent_mode tool is missing")
}

func TestConfigureAgentModePersistsBrowserInteractionPolicy(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "agent.json")
	getenv := func(key string) string {
		if key == "OPENLINKER_AGENT_CONFIG" {
			return configPath
		}
		return ""
	}
	server := &Server{
		Host:  "codex",
		IO:    shared.IO{Getenv: getenv},
		Agent: agent.NewService(getenv, nil, "test"),
	}
	result, err := server.configureAgent(map[string]any{
		"provider":                   "codex",
		"agent_id":                   "11111111-1111-4111-8111-111111111111",
		"workspace":                  dir,
		"execution_profile":          "browser",
		"browser_interaction_policy": "full",
		"browser_client_mode":        "mcp",
		"browser_socket":             filepath.Join(dir, "control.sock"),
		"browser_credential_file":    filepath.Join(dir, "channel"),
		"browser_lease_root":         filepath.Join(dir, "leases"),
		"browser_broker_root":        filepath.Join(dir, "broker"),
	})
	if err != nil {
		t.Fatal(err)
	}
	structured := result.StructuredContent.(map[string]any)
	if structured["browser_interaction_policy"] != "full" {
		t.Fatalf("configure result = %#v", structured)
	}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]any
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	if saved["browser_interaction_policy"] != "full" {
		t.Fatalf("saved Agent config = %#v", saved)
	}
}

func TestServerCancellationDoesNotWaitForStdinEOF(t *testing.T) {
	reader, writer := io.Pipe()
	defer writer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	server := &Server{Host: "codex", IO: shared.IO{Getenv: func(string) string { return "" }}, Agent: agent.NewService(func(string) string { return "" }, nil, "test")}
	go func() { done <- server.Serve(ctx, reader, &bytes.Buffer{}) }()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve cancellation = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve waited for stdin EOF after cancellation")
	}
}

func TestCancelRequestCancelsMatchingJSONRPCRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancels := map[string]context.CancelFunc{`"run-1"`: cancel}
	cancelRequest(json.RawMessage(`{"requestId":"run-1"}`), cancels)
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("matching MCP cancellation did not cancel the request context")
	}
}

func TestAgentConfigurationToolRejectsSecrets(t *testing.T) {
	server := &Server{IO: shared.IO{Getenv: func(string) string { return "" }}}
	_, err := server.configureAgent(map[string]any{
		"provider": "codex", "agent_id": "11111111-1111-4111-8111-111111111111", "workspace": t.TempDir(),
		"api_key": "never-accept-this",
	})
	if err == nil || !strings.Contains(err.Error(), "secrets") {
		t.Fatalf("error = %v", err)
	}
	_, err = server.configureAgent(map[string]any{
		"provider": "codex", "agent_id": "11111111-1111-4111-8111-111111111111", "workspace": t.TempDir(),
		"nested": map[string]any{"agent_token": "never-accept-this"},
	})
	if err == nil || !strings.Contains(err.Error(), "secrets") {
		t.Fatalf("nested secret error = %v", err)
	}
}

func TestAgentConfigurationToolStoresCodexBaseURL(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "agent.json")
	server := &Server{IO: shared.IO{Getenv: func(key string) string {
		if key == "OPENLINKER_AGENT_CONFIG" {
			return configPath
		}
		return ""
	}}}
	_, err := server.configureAgent(map[string]any{
		"provider": "codex", "agent_id": "11111111-1111-4111-8111-111111111111", "workspace": dir,
		"codex_base_url": "https://router.example/v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"codex_base_url": "https://router.example/v1"`) {
		t.Fatalf("stored config = %s", raw)
	}
}

func TestA2AContextUsesConversationID(t *testing.T) {
	value := a2aContextArgument(map[string]any{
		"conversation_id": "conversation-1",
		"a2a_context":     map[string]any{"protocol_task_id": "turn-2"},
	})
	if value == nil || value.ProtocolContextID != "conversation-1" || value.RootContextID != "conversation-1" || value.ProtocolTaskID != "turn-2" {
		t.Fatalf("A2A context = %#v", value)
	}
}
