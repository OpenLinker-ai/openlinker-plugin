//go:build crossrepo

package browserclientmode

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentRuntimePluginCrossRepositoryArtifacts(t *testing.T) {
	tests := []struct {
		provider string
		env      string
	}{
		{
			provider: "codex",
			env:      "OPENLINKER_AGENT_RUNTIME_CODEX_PLUGIN_ROOT",
		},
		{
			provider: "claude",
			env:      "OPENLINKER_AGENT_RUNTIME_CLAUDE_PLUGIN_ROOT",
		},
	}
	for _, test := range tests {
		t.Run(test.provider, func(t *testing.T) {
			root := strings.TrimSpace(os.Getenv(test.env))
			if root == "" {
				t.Fatalf("%s is required by the cross-repository gate", test.env)
			}
			if !filepath.IsAbs(root) {
				t.Fatalf("%s must be an absolute path", test.env)
			}
			if err := validateAgentRuntimePlugin(
				test.provider,
				root,
				false,
			); err != nil {
				t.Fatalf(
					"%s Agent Runtime Plugin artifact violated the CLI contract: %v",
					test.provider,
					err,
				)
			}
		})
	}
}
