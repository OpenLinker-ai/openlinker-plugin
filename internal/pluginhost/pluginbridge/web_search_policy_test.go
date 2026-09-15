package pluginbridge

import (
	"context"
	"encoding/json"
	"github.com/OpenLinker-ai/openlinker-plugin/internal/pluginhost/shared"
	"github.com/OpenLinker-ai/openlinker-plugin/packages/agent-adapters/agent"
	"path/filepath"
	"testing"
)

func TestConfigureAgentMCPReportsSearchEnvironmentOverride(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{"OPENLINKER_AGENT_CONFIG": filepath.Join(dir, "agent.json"), "OPENLINKER_PROVIDER": "claude", "OPENLINKER_CLAUDE_WEB_SEARCH": "invalid-other-provider-override", "OPENLINKER_CODEX_WEB_SEARCH": "false"}
	server := &Server{Host: "codex", IO: shared.IO{Getenv: func(k string) string { return env[k] }}}
	result, err := server.configureAgent(map[string]any{"provider": "codex", "agent_id": "11111111-1111-4111-8111-111111111111", "workspace": dir, "web_search": true})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(result.StructuredContent)
	var response struct {
		Policy struct {
			Provider, Source                  string
			Configured, Effective, Overridden bool
		} `json:"web_search"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatal(err)
	}
	if response.Policy.Provider != "codex" || response.Policy.Source != "OPENLINKER_CODEX_WEB_SEARCH" || !response.Policy.Configured || response.Policy.Effective || !response.Policy.Overridden {
		t.Fatalf("MCP lost actual serving provider/override: %s", raw)
	}
	service := &searchPolicyService{}
	server.Agent = service
	if _, err := server.callTool(context.Background(), "enable_agent_mode", nil); err != nil {
		t.Fatal(err)
	}
	if service.provider != response.Policy.Provider {
		t.Fatal("configure predicts a different provider than MCP enable")
	}
}

// Verify the actual MCP enable argument without enrolling a Worker.
type searchPolicyService struct{ provider string }

func (s *searchPolicyService) Status() agent.Status { return agent.Status{State: "stopped"} }
func (s *searchPolicyService) Enable(_ context.Context, provider string) error {
	s.provider = provider
	return nil
}
func (s *searchPolicyService) Disable(context.Context) error { return nil }
