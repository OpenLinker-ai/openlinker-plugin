package agentexec

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/agentdelegation"
	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/providertest"
	openlinker "github.com/OpenLinker-ai/openlinker-go"
)

// Exercises the Plugin production consumer with the pinned official client,
// synthetic OAuth/API-key credentials and a loopback model. No live account is used.
func TestInstalledClaudeMCPAuthentication(t *testing.T) {
	bin := os.Getenv("OPENLINKER_TEST_NATIVE_CLAUDE_BIN")
	if bin == "" {
		t.Skip("set the pinned official Claude executable")
	}
	home := t.TempDir()
	conf := filepath.Join(home, ".claude")
	workspace := t.TempDir()
	if err := os.Mkdir(conf, 0700); err != nil {
		t.Fatal(err)
	}
	write := func(path string, value any) {
		t.Helper()
		raw, err := json.Marshal(value)
		if err == nil {
			err = os.WriteFile(path, raw, 0600)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	const cached = "synthetic-plugin-cached-auth"
	const apiKey = "synthetic-plugin-api-key"
	write(filepath.Join(conf, ".credentials.json"), map[string]any{"claudeAiOauth": map[string]any{"accessToken": cached, "refreshToken": "synthetic-refresh", "expiresAt": time.Now().Add(time.Hour).UnixMilli(), "scopes": []string{"user:inference", "user:profile"}, "subscriptionType": "pro", "rateLimitTier": "default_claude_pro"}})
	write(filepath.Join(home, ".claude.json"), map[string]any{"hasCompletedOnboarding": true, "oauthAccount": map[string]any{"accountUuid": nativeParentID, "organizationUuid": nativeTargetID, "emailAddress": "fixture@example.invalid"}})
	const ambient = "ambient-instruction-must-not-load"
	if err := os.WriteFile(filepath.Join(conf, "CLAUDE.md"), []byte(ambient), 0600); err != nil {
		t.Fatal(err)
	}
	hook := filepath.Join(home, "unexpected-hook")
	providertest.SeedClaudeAmbient(t, workspace, hook)
	write(filepath.Join(conf, "settings.json"), map[string]any{"hooks": map[string]any{"SessionStart": []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "touch '" + hook + "'"}}}}}})
	tool := agentdelegation.BoundTool{BindingID: nativeParentID, TargetID: nativeTargetID, Digest: strings.Repeat("a", 64), Name: "mcp_22222222222242228222222222222222", InputSchema: map[string]any{"type": "object"}}
	var mu sync.Mutex
	turn, requests, cachedRequests, keyRequests := 0, 0, 0, 0
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			fmt.Fprint(w, `{"models":[]}`)
			return
		}
		if strings.Contains(r.URL.Path, "count_tokens") {
			fmt.Fprint(w, `{"input_tokens":20}`)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		requests++
		bodies = append(bodies, string(raw))
		auth := r.Header.Get("Authorization") + r.Header.Get("X-Api-Key")
		if strings.Contains(auth, cached) {
			cachedRequests++
		}
		if strings.Contains(auth, apiKey) {
			keyRequests++
		}
		id := fmt.Sprintf("fixture_plugin_mcp_%d", turn)
		content := map[string]any{"type": "text", "text": "fixture complete"}
		if !strings.Contains(string(raw), id) {
			content = map[string]any{"type": "tool_use", "id": id, "name": "mcp__openlinker_delegation__" + tool.Name, "input": map[string]any{"arguments": map[string]any{}, "request_key": id}}
		}
		providertest.ClaudeFixtureEvents(w, requests, content, content["type"] == "tool_use")
	}))
	defer server.Close()
	proxy := filepath.Join(t.TempDir(), "host")
	providertest.WriteMCPFixture(t, proxy, "claude")
	config := ProviderConfig{Provider: "claude", Bin: bin, DelegationProxyBin: proxy, Workspace: workspace, SessionReuse: true, SessionStore: filepath.Join(t.TempDir(), "sessions.json"), Model: "claude-sonnet-4-6", Timeout: 30 * time.Second,
		Env: []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "CLAUDE_CONFIG_DIR=" + conf, "ANTHROPIC_BASE_URL=" + server.URL, "NO_PROXY=127.0.0.1,localhost,::1", "HTTP_PROXY=", "HTTPS_PROXY=", "ALL_PROXY="}, EnvAllowlist: []string{"ANTHROPIC_BASE_URL"}}
	calls, ready := 0, 0
	for i, session := range []string{"a", "b", "a"} {
		mu.Lock()
		turn = i
		mu.Unlock()
		if i == 1 {
			config.Env = append(config.Env, "ANTHROPIC_API_KEY="+apiKey)
		}
		provider, err := NewProvider(config)
		if err != nil {
			t.Fatal(err)
		}
		id := fmt.Sprintf("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa%d", i+1)
		run := RunContext{RunID: id, AgentID: "55555555-5555-4555-8555-555555555555", Authority: &openlinker.RuntimeAuthorityContext{PrincipalScopeID: "caller"}, Conversation: &ConversationContext{Source: "core", SessionKey: session, CurrentRunID: id}, MCPSnapshot: agentdelegation.BoundSnapshot{Schema: 1, Tools: []agentdelegation.BoundTool{tool}},
			Emit: func(kind string, _ any) error {
				if kind == "run.mcp_bindings.ready" {
					ready++
				}
				return nil
			},
			CallAgent: func(_ context.Context, target string, _ any, options openlinker.RuntimeCallOptions) (any, error) {
				calls++
				if target != tool.TargetID || options.Metadata == nil {
					t.Error("incorrect bound MCP call")
				}
				return openlinker.RuntimeRunSummary{RunID: nativeChildID, Status: openlinker.RuntimeRunRunning}, nil
			},
			ReadDelegatedRun: func(context.Context, string) (*openlinker.RuntimeDelegatedRun, error) {
				return &openlinker.RuntimeDelegatedRun{RuntimeRunSummary: openlinker.RuntimeRunSummary{RunID: nativeChildID, Status: openlinker.RuntimeRunSuccess}, Output: map[string]any{"marker": "TOOL-MARKER"}}, nil
			},
		}
		result, err := provider.Run(context.Background(), run)
		if err != nil || result.Error != nil {
			t.Fatalf("turn %d: result=%#v err=%v", i, result, err)
		}
		if result.Output.(map[string]any)["claude_session_resumed"] != (i == 2) {
			t.Fatalf("wrong continuation on turn %d: %#v", i, result.Output)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 3 || ready != 3 || cachedRequests == 0 || keyRequests == 0 || cachedRequests+keyRequests != requests {
		t.Fatalf("calls=%d ready=%d requests=%d cached=%d key=%d", calls, ready, requests, cachedRequests, keyRequests)
	}
	if _, err := os.Stat(hook); !os.IsNotExist(err) {
		t.Fatal("ambient hook was executed")
	}
	for _, body := range bodies {
		if strings.Contains(body, ambient) || strings.Contains(body, cached) || strings.Contains(body, apiKey) {
			t.Fatal("ambient instruction or synthetic credential reached model input")
		}
	}
	t.Logf("cached login and API key authenticated %d local requests; three MCP calls and A-B-A continuation passed", requests)
}
