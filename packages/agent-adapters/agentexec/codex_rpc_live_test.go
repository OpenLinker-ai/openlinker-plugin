package agentexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/codexrpc"
)

// Exercises the installed official binary against a local fake Responses API.
// No personal credentials, real model requests, or external service are used.
func TestInstalledCodexRPCWithLocalResponsesAPI(t *testing.T) {
	if os.Getenv("OPENLINKER_TEST_CODEX_RPC_LOCAL_MODEL") != "1" {
		t.Skip("opt-in installed Codex, local fake model only")
	}
	for _, test := range []struct {
		name             string
		native, codeMode bool
	}{
		{"standard", false, false},
		{"native-browser", true, false},
		{"native-browser-code-mode", true, true},
	} {
		t.Run(test.name, func(t *testing.T) { testInstalledCodexRPCWithLocalAPI(t, test.native, test.codeMode) })
	}
}
func testInstalledCodexRPCWithLocalAPI(t *testing.T, native, codeMode bool) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer local-test-key" {
			t.Error("unexpected local API authentication")
			http.Error(w, "bad auth", 401)
			return
		}
		var request struct {
			Tools []map[string]any `json:"tools"`
			Input []map[string]any `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error("decode local fake API request", err)
			http.Error(w, "invalid request", 400)
			return
		}
		n := calls.Add(1)
		if (!native && n == 2) || (native && n == 4) {
			previousAnswer := false
			for _, item := range request.Input {
				if item["role"] == "assistant" {
					raw, _ := json.Marshal(item)
					if strings.Contains(string(raw), "local protocol answer 1") {
						previousAnswer = true
					}
				}
			}
			if !previousAnswer {
				t.Error("resumed native thread lost its prior assistant context")
			}
		}
		if native && n == 2 {
			if codeMode {
				output := localCodeModeOutput(request.Input, "code-browser-1")
				if !strings.Contains(output, "isolated browser discovered") {
					t.Errorf("Code Mode Browser discovery or host-API regression sentinel failed: %s", output)
				}
			} else {
				found := false
				for _, item := range request.Input {
					if item["type"] == "tool_search_output" {
						raw, _ := json.Marshal(item)
						if strings.Contains(string(raw), "mcp__openlinker_browser") && strings.Contains(string(raw), "browser_session") {
							found = true
						}
					}
				}
				if !found {
					t.Error("native Browser tool was not discovered")
				}
			}
		}
		if native && n == 3 {
			raw, _ := json.Marshal(request.Input)
			output := string(raw)
			if codeMode {
				output = localCodeModeOutput(request.Input, "code-browser-2")
			}
			if !strings.Contains(output, "local browser result") {
				t.Errorf("native MCP call did not return its result: %s", output)
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"response-%d\"}}\n\n", n)
		if codeMode && (n == 1 || n == 2) {
			// Selected exposure checks only: this is not an exhaustive audit of
			// dynamic imports, globalThis handles, or the upstream V8 boundary.
			code := `if ([typeof process, typeof require, typeof Deno, typeof fetch, typeof XMLHttpRequest, typeof WebSocket].some(x => x !== "undefined")) throw Error("unexpected host API");
if (ALL_TOOLS.some(x => /(^|__)(exec_command|shell|shell_command|write_stdin)$/.test(x.name))) throw Error("shell tool exposed");
const browser = ALL_TOOLS.find(x => /browser_session$/.test(x.name));
if (!browser) throw Error("Browser tool missing");
text("isolated browser discovered");`
			if n == 2 {
				code = `const browser = ALL_TOOLS.find(x => /browser_session$/.test(x.name)); text(await tools[browser.name]({}));`
			}
			item := map[string]any{"type": "response.output_item.done", "item": map[string]any{
				"type": "custom_tool_call", "call_id": fmt.Sprintf("code-browser-%d", n), "name": "exec", "input": code,
			}}
			raw, _ := json.Marshal(item)
			fmt.Fprintf(w, "data: %s\n\n", raw)
		} else if native && n == 1 {
			fmt.Fprint(w, "data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"tool_search_call\",\"call_id\":\"local-search\",\"execution\":\"client\",\"arguments\":{\"query\":\"openlinker_browser browser_session\"}}}\n\n")
		} else if native && n == 2 {
			fmt.Fprint(w, "data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"function_call\",\"call_id\":\"local-browser-call\",\"namespace\":\"mcp__openlinker_browser\",\"name\":\"browser_session\",\"arguments\":\"{}\"}}\n\n")
		} else {
			answer := n
			if native {
				answer -= 2
			}
			fmt.Fprintf(w, "data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"message\",\"role\":\"assistant\",\"id\":\"message-%d\",\"phase\":\"final_answer\",\"content\":[{\"type\":\"output_text\",\"text\":\"local protocol answer %d\"}]}}\n\n", n, answer)
		}
		fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"response-%d\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n", n)
	}))
	defer server.Close()
	source := t.TempDir()
	workspace := t.TempDir()
	marker := filepath.Join(t.TempDir(), "untrusted-mcp-started")
	malicious := fmt.Sprintf("[mcp_servers.untrusted]\ncommand=\"/usr/bin/touch\"\nargs=[%q]\n", marker)
	if err := os.WriteFile(filepath.Join(source, "config.toml"), []byte(malicious), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(workspace, ".codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".codex", "config.toml"), []byte(malicious), 0o600); err != nil {
		t.Fatal(err)
	}
	config := ProviderConfig{Bin: "codex", Workspace: workspace, Model: "gpt-5.4", CodexBaseURL: server.URL + "/v1", Sandbox: "workspace-write", CodexApproval: "never", SessionReuse: true, SessionStore: filepath.Join(t.TempDir(), "sessions.json"), Timeout: 20 * time.Second,
		Env: []string{"PATH=" + os.Getenv("PATH"), "HOME=" + source, "CODEX_HOME=" + source, "CODEX_API_KEY=local-test-key", "NO_PROXY=127.0.0.1,localhost"}}
	if native {
		config.ExecutionProfile = "browser"
		config.BrowserClientMode = "native"
		config.Sandbox = "danger-full-access"
		config.BrowserNativePlugin = makeLiveBrowserMarketplace(t, workspace)
	}
	if codeMode {
		// The pinned Codex catalog selects Code Mode for this model. Leave
		// feature defaults unchanged and exercise production's required exec path.
		config.Model = "gpt-5.6-sol"
	}
	provider := CodexProvider{Config: config}
	run := RunContext{Input: "reply briefly", Conversation: &ConversationContext{SessionKey: "local-rpc-test", Source: "core"}}
	for n := 1; n <= 2; n++ {
		run.RunID = fmt.Sprintf("run-%d", n)
		result, err := provider.Run(context.Background(), run)
		if err != nil {
			var rpcErr *codexrpc.Error
			if errors.As(err, &rpcErr) {
				t.Log("local fixture RPC diagnostic:", rpcErr.Message)
			}
			t.Fatalf("%v; local API calls=%d", err, calls.Load())
		}
		output := result.Output.(map[string]any)
		if native && output["codex_sandbox"] != "read-only" {
			t.Fatal("native Browser escaped read-only sandbox", output)
		}
		if output["summary"] != fmt.Sprintf("local protocol answer %d", n) {
			t.Fatal(output)
		}
		if n == 2 && (output["codex_session_resumed"] != true || output["codex_session_recovered"] != false) {
			t.Fatal("native rollouts failed to resume across private homes", output)
		}
	}
	expectedCalls := int32(2)
	if native {
		expectedCalls = 4
	}
	if calls.Load() != expectedCalls {
		t.Fatal("unexpected local API call count", calls.Load())
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("untrusted user/project MCP was started")
	}
	entries, err := os.ReadDir(filepath.Join(source, "openlinker-rpc-v1"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "attempt-") {
			t.Fatal("private home leaked")
		}
	}
}

func localCodeModeOutput(input []map[string]any, callID string) string {
	for _, item := range input {
		if item["type"] == "custom_tool_call_output" && item["call_id"] == callID {
			raw, _ := json.Marshal(item["output"])
			return string(raw)
		}
	}
	return "missing tool result"
}

func makeLiveBrowserMarketplace(t *testing.T, workspace string) string {
	t.Helper()
	root := t.TempDir()
	plugin := filepath.Join(root, "plugins", "openlinker")
	for _, dir := range []string{filepath.Join(root, ".agents", "plugins"), filepath.Join(plugin, ".codex-plugin")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(t.TempDir(), "browser-mcp")
	script := "#!/bin/sh\nexport OPENLINKER_CODEX_MCP_FIXTURE=1\nexec '" + strings.ReplaceAll(executable, "'", "'\\''") + "' -test.run=TestLiveBrowserMCPFixtureProcess\n"
	if err := os.WriteFile(helper, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string]any{
		filepath.Join(root, ".agents", "plugins", "marketplace.json"): map[string]any{"name": "openlinker-agent-runtime", "interface": map[string]any{"displayName": "Local Test"}, "plugins": []any{map[string]any{"name": "openlinker", "source": map[string]any{"source": "local", "path": "./plugins/openlinker"}, "policy": map[string]any{"installation": "AVAILABLE", "authentication": "ON_INSTALL"}}}},
		filepath.Join(plugin, ".codex-plugin", "plugin.json"):         map[string]any{"name": "openlinker", "version": "0.1.0", "description": "Local protocol fixture", "mcpServers": "./.mcp.json"},
		filepath.Join(plugin, ".mcp.json"):                            map[string]any{"mcpServers": map[string]any{"openlinker_browser": map[string]any{"command": helper, "cwd": workspace}}},
	}
	for path, value := range files {
		raw, _ := json.Marshal(value)
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}
func TestLiveBrowserMCPFixtureProcess(t *testing.T) {
	if os.Getenv("OPENLINKER_CODEX_MCP_FIXTURE") != "1" {
		return
	}
	read, write := json.NewDecoder(os.Stdin), json.NewEncoder(os.Stdout)
	for {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if read.Decode(&request) != nil {
			os.Exit(0)
		}
		if len(request.ID) == 0 {
			continue
		}
		result := map[string]any{}
		switch request.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "local-browser", "version": "1"}}
		case "tools/list":
			result = map[string]any{"tools": []any{map[string]any{"name": "browser_session", "description": "Local Browser fixture", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}}}}
		case "tools/call":
			result = map[string]any{"content": []any{map[string]any{"type": "text", "text": "local browser result"}}}
		}
		_ = write.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
	}
}
