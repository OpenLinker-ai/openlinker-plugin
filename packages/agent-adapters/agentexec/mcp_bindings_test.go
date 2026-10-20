package agentexec

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/agentdelegation"
	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/providertest"
	openlinker "github.com/OpenLinker-ai/openlinker-go"
)

func TestMCPProviderFixtureProcess(t *testing.T) { providertest.MCPFixtureProcess() }
func TestPluginProvidersUseSelectedMCPTools(t *testing.T) {
	for _, name := range []string{"codex", "claude"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			binary := filepath.Join(dir, name)
			providertest.WriteMCPFixture(t, binary, name)
			provider, err := NewProvider(ProviderConfig{Provider: name, Bin: binary, DelegationProxyBin: binary, Workspace: dir, SessionReuse: true, SessionStore: filepath.Join(dir, "sessions.json"), Env: append(os.Environ(), "HOME="+dir, "CODEX_HOME="+filepath.Join(dir, ".codex"))})
			if err != nil {
				t.Fatal(err)
			}
			tool := agentdelegation.BoundTool{BindingID: nativeParentID, TargetID: nativeTargetID, Digest: strings.Repeat("a", 64), Name: "mcp_22222222222242228222222222222222", Title: "Lookup", InputSchema: map[string]any{"type": "object"}}
			calls, ready := 0, 0
			ids := []string{"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa1", "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa2", "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa3", "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa4"}
			for i, id := range ids {
				if i == 2 {
					tool.BindingID = nativeChildID
				}
				var snapshot any = agentdelegation.BoundSnapshot{Schema: 1, Tools: []agentdelegation.BoundTool{tool}}
				if i == 3 {
					snapshot = nil
				}
				run := RunContext{RunID: id, AgentID: "55555555-5555-4555-8555-555555555555", Authority: &openlinker.RuntimeAuthorityContext{PrincipalScopeID: "caller"}, Conversation: &ConversationContext{Source: "core", SessionKey: "same-conversation", CurrentRunID: id}, MCPSnapshot: snapshot,
					Emit: func(kind string, _ any) error {
						if kind == "run.mcp_bindings.ready" {
							ready++
						}
						return nil
					},
					CallAgent: func(_ context.Context, target string, input any, options openlinker.RuntimeCallOptions) (any, error) {
						calls++
						if target != tool.TargetID || input.(map[string]any)["text"] != "lookup" || options.Metadata == nil {
							t.Fatal("incorrect MCP callback")
						}
						return openlinker.RuntimeRunSummary{RunID: nativeChildID, Status: openlinker.RuntimeRunRunning}, nil
					},
					ReadDelegatedRun: func(context.Context, string) (*openlinker.RuntimeDelegatedRun, error) {
						return &openlinker.RuntimeDelegatedRun{RuntimeRunSummary: openlinker.RuntimeRunSummary{RunID: nativeChildID, Status: openlinker.RuntimeRunSuccess}, Output: map[string]any{"marker": "TOOL-MARKER"}}, nil
					},
				}
				result, err := provider.Run(context.Background(), run)
				if err != nil || result.Error != nil {
					t.Fatalf("result=%#v err=%v", result, err)
				}
				output := result.Output.(map[string]any)
				if output[name+"_session_resumed"] != (i == 1) {
					t.Fatalf("Run %d selection session mismatch: %#v", i, output)
				}
			}
			if calls != 3 || ready != 3 {
				t.Fatalf("calls=%d ready=%d", calls, ready)
			}
		})
	}
}
func TestSelectedMCPKeepsNativeBrowserAndNarrowTools(t *testing.T) {
	config := ProviderConfig{Provider: "claude", ExecutionProfile: "browser", BrowserClientMode: "native", BrowserNativePlugin: "/trusted/browser", DelegationSocket: "/private/run/mcp.sock", DelegationProxyBin: "/trusted/host", boundMCPTools: []agentdelegation.BoundTool{{Name: "mcp_22222222222242228222222222222222"}}}
	args := strings.Join(claudeArguments(config, "dontAsk", ""), " ")
	if !strings.Contains(args, "--plugin-dir /trusted/browser") || !strings.Contains(args, "--mcp-config") || strings.Contains(args, "--strict-mcp-config") || strings.Contains(args, "delegate_agent") || strings.Contains(args, "bypassPermissions") {
		t.Fatal(args)
	}
	if !strings.Contains(args, "mcp__openlinker_delegation__mcp_22222222222242228222222222222222") {
		t.Fatal("bound tool is not permitted")
	}
}
