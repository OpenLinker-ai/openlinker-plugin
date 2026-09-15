package agent

import (
	"bytes"
	"encoding/json"
	"github.com/OpenLinker-ai/openlinker-plugin/internal/pluginhost/shared"
	"path/filepath"
	"testing"
)

func TestConfigureCommandReportsSearchEnvironmentOverride(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	env := map[string]string{"OPENLINKER_AGENT_CONFIG": filepath.Join(dir, "agent.json"), "OPENLINKER_CODEX_WEB_SEARCH": "false", "OPENLINKER_AGENT_WEB_SEARCH": "true", "CODEX_API_KEY": "private-key-must-not-appear"}
	command := newConfigureCommand(shared.IO{Getenv: func(k string) string { return env[k] }, Stdout: &out})
	command.SetArgs([]string{"--provider=codex", "--agent-id=11111111-1111-4111-8111-111111111111", "--workspace=" + dir, "--web-search=true"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	var response struct {
		Configured bool `json:"configured"`
		Policy     struct {
			Configured, Effective, Overridden bool
			Source                            string
		} `json:"web_search"`
	}
	if err := json.Unmarshal(out.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !response.Configured || !response.Policy.Configured || response.Policy.Effective || !response.Policy.Overridden || response.Policy.Source != "OPENLINKER_CODEX_WEB_SEARCH" {
		t.Fatalf("missing effective search override: %s", out.Bytes())
	}
	if bytes.Contains(out.Bytes(), []byte(env["CODEX_API_KEY"])) {
		t.Fatal("configure leaked credential")
	}
}
