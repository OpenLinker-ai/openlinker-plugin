package agentexec

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/agentdelegation"
	openlinker "github.com/OpenLinker-ai/openlinker-go"
)

const nativeParentID = "11111111-1111-4111-8111-111111111111"
const nativeTargetID = "22222222-2222-4222-8222-222222222222"
const nativeChildID = "33333333-3333-4333-8333-333333333333"

func TestDelegationInjectionPreservesDefaultProviderEnvironment(t *testing.T) {
	t.Setenv("CODEX_API_KEY", "provider-key")
	t.Setenv("OPENLINKER_AGENT_TOKEN", "worker-key")
	config := providerConfigForDelegationRun(ProviderConfig{}, RunContext{DelegationSocket: "/private/socket", DelegationProxyBin: "/host"})
	environment := sanitizedEnvironment(config.Env, append([]string{"CODEX_API_KEY"}, config.EnvAllowlist...))
	joined := strings.Join(environment, "\n")
	if !strings.Contains(joined, "CODEX_API_KEY=provider-key") || !strings.Contains(joined, agentdelegation.SocketEnvironment+"=/private/socket") || strings.Contains(joined, "worker-key") {
		t.Fatal("delegation injection changed the provider/worker credential boundary")
	}
}

func TestNativeProvidersDelegateAndContinueThroughProxy(t *testing.T) {
	for _, name := range []string{"codex", "claude"} {
		t.Run(name, func(t *testing.T) {
			directory := t.TempDir()
			binary := filepath.Join(directory, "fake-native-cli")
			script := "#!/bin/sh\nif [ \"$*\" = \"plugin capabilities\" ]; then printf '%s\\n' '{\"protocol\":\"openlinker.agent-host.v1\",\"browser_proxy\":true,\"delegation_proxy\":true}'; exit 0; fi\nexec \"$TEST_EXECUTABLE\" -test.run=TestNativeDelegationProviderHelper -- \"$@\"\n"
			if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			provider, err := NewProvider(ProviderConfig{Provider: name, Bin: binary, Workspace: directory,
				Timeout: 10 * time.Second, DelegationTargets: []string{nativeTargetID}, DelegationProxyBin: binary,
				Env:          append(os.Environ(), "CODEX_HOME="+filepath.Join(directory, "native-home"), "TEST_NATIVE_HELPER=1", "TEST_NATIVE_PROVIDER="+name, "TEST_EXECUTABLE="+os.Args[0]),
				EnvAllowlist: []string{"TEST_NATIVE_HELPER", "TEST_NATIVE_PROVIDER", "TEST_EXECUTABLE"},
			})
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			result, err := provider.Run(context.Background(), RunContext{RunID: nativeParentID, Input: map[string]any{"prompt": "delegate review"},
				CallAgent: func(ctx context.Context, target string, input any, options openlinker.RuntimeCallOptions) (any, error) {
					calls++
					if target != nativeTargetID || input.(map[string]any)["prompt"] != "review this change" || options.IdempotencyKey == "" {
						t.Error("delegation was not scoped")
					}
					return openlinker.RuntimeRunSummary{RunID: nativeChildID, Status: openlinker.RuntimeRunRunning, DispatchState: openlinker.RuntimeDispatchPending}, nil
				},
				ReadDelegatedRun: func(ctx context.Context, id string) (*openlinker.RuntimeDelegatedRun, error) {
					if id != nativeChildID {
						t.Error("wrong child")
					}
					return &openlinker.RuntimeDelegatedRun{RuntimeRunSummary: openlinker.RuntimeRunSummary{RunID: id, Status: openlinker.RuntimeRunSuccess, DispatchState: openlinker.RuntimeDispatchTerminal}, Output: map[string]any{"summary": "child review passed"}}, nil
				},
			})
			if err != nil || result.Error != nil || calls != 1 {
				t.Fatalf("result=%#v calls=%d err=%v", result, calls, err)
			}
			raw, _ := json.Marshal(result.Output)
			if !strings.Contains(string(raw), "continued after child review passed") {
				t.Fatal(string(raw))
			}
		})
	}
}

// This subprocess acts as a native CLI and invokes the configured stdio helper.
// The helper transports real MCP bytes over the real Attempt socket.
func TestNativeDelegationProviderHelper(t *testing.T) {
	if os.Getenv("TEST_NATIVE_HELPER") != "1" {
		return
	}
	args := os.Args
	for index, arg := range args {
		if arg == "--" {
			args = args[index+1:]
			break
		}
	}
	if len(args) > 0 && args[0] == "plugin" {
		if err := agentdelegation.Proxy(context.Background(), os.Stdin, os.Stdout, os.Getenv(agentdelegation.SocketEnvironment)); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		os.Exit(0)
	}
	host := os.Getenv("TEST_NATIVE_PROVIDER")
	var rpc *rpcFixture
	if host == "codex" {
		rpc = startRPCFixture("delegation")
	} else {
		io.ReadAll(os.Stdin)
	}
	var binary string
	if host == "codex" {
		for _, arg := range args {
			if value, ok := strings.CutPrefix(arg, "mcp_servers.openlinker_delegation.command="); ok {
				json.Unmarshal([]byte(value), &binary)
			}
		}
	} else {
		for index, arg := range args {
			if arg == "--safe-mode" {
				fmt.Fprintln(os.Stderr, "safe-mode disables delegation MCP")
				os.Exit(2)
			}
			if arg == "--mcp-config" && index+1 < len(args) {
				var config struct {
					Servers map[string]struct {
						Command string `json:"command"`
					} `json:"mcpServers"`
				}
				json.Unmarshal([]byte(args[index+1]), &config)
				binary = config.Servers["openlinker_delegation"].Command
			}
		}
	}
	if binary == "" {
		fmt.Fprintln(os.Stderr, "delegation MCP was not injected")
		os.Exit(2)
	}
	command := exec.Command(binary, "plugin", "delegation-proxy", "--host", host)
	input, _ := command.StdinPipe()
	output, _ := command.StdoutPipe()
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	scanner := bufio.NewScanner(output)
	call := func(method string, params any) string {
		json.NewEncoder(input).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
		if !scanner.Scan() {
			fmt.Fprintln(os.Stderr, "MCP response absent")
			os.Exit(2)
		}
		return scanner.Text()
	}
	call("initialize", map[string]any{})
	response := call("tools/call", map[string]any{"name": "delegate_agent", "arguments": map[string]any{"target_agent_id": nativeTargetID, "input": map[string]any{"prompt": "review this change"}, "request_key": "review-1"}})
	if !strings.Contains(response, nativeChildID) {
		fmt.Fprintln(os.Stderr, response)
		os.Exit(2)
	}
	response = call("tools/call", map[string]any{"name": "wait_delegated_run", "arguments": map[string]any{"child_run_id": nativeChildID}})
	if !strings.Contains(response, "child review passed") {
		fmt.Fprintln(os.Stderr, response)
		os.Exit(2)
	}
	input.Close()
	if err := command.Wait(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if host == "codex" {
		rpc.Finish("continued after child review passed")
	} else {
		fmt.Println(`{"type":"result","result":"continued after child review passed"}`)
	}
	os.Exit(0)
}

func TestDelegationSessionFingerprintAndBrowserComposition(t *testing.T) {
	config := ProviderConfig{Provider: "claude", DelegationTargets: []string{nativeTargetID}, DelegationSocket: "/tmp/delegation.sock", DelegationProxyBin: "/bin/true"}
	first := providerSessionClientMode(config)
	rotated := config
	rotated.DelegationSocket = "/tmp/next.sock"
	if providerSessionClientMode(rotated) != first {
		t.Fatal("socket rotation invalidated session")
	}
	rotated.DelegationTargets = []string{nativeChildID}
	if providerSessionClientMode(rotated) == first {
		t.Fatal("target changes retained old session authority")
	}
	config.ExecutionProfile = "browser"
	config.BrowserClientMode = "mcp"
	config.BrowserPluginBin = "/bin/true"
	var payload struct {
		Servers map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(claudeRunMCPConfig(config)), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Servers) != 2 {
		t.Fatal("Browser MCP was replaced by delegation")
	}
	args := strings.Join(claudeArguments(config, "dontAsk", ""), " ")
	if strings.Contains(args, "bypassPermissions") || strings.Contains(args, "--safe-mode") || !strings.Contains(args, "mcp__openlinker_delegation__delegate_agent") {
		t.Fatal(args)
	}
	config.BrowserClientMode = "native"
	config.BrowserNativePlugin = "/trusted/plugin"
	if _, err := withDelegation(ClaudeProvider{Config: config}, config); err == nil {
		t.Fatal("strict MCP must not silently disable the native Browser plugin server")
	}
}
