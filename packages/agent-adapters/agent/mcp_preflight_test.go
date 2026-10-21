package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMCPPreflightFailsWithLocalCause(t *testing.T) {
	for _, failure := range []string{"proxy", "broker"} {
		t.Run(failure, func(t *testing.T) {
			get, env := authPreflightFixture(t, "claude", "", false)
			name := "OPENLINKER_AGENT_DELEGATION_PROXY_BIN"
			if failure == "broker" {
				name = "OPENLINKER_AGENT_DELEGATION_BROKER_ROOT"
			}
			previous := env[name]
			path := filepath.Join(t.TempDir(), "unavailable")
			if failure == "broker" {
				if err := os.WriteFile(path, []byte("file"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			env[name] = path
			_, err := resolveRuntime(get, "", "test")
			if err == nil || !strings.Contains(err.Error(), "MCP delivery preflight") {
				t.Fatalf("missing local cause: %v", err)
			}
			env[name] = previous
			r, err := resolveRuntime(get, "", "test")
			if err != nil {
				t.Fatal("retry after repair:", err)
			}
			defer r.workerLock.release()
			if !strings.Contains(strings.Join(r.optionalFeatures(false, false), ","), "mcp_bindings.v1") {
				t.Fatal("MCP capability disappeared")
			}
		})
	}
}
