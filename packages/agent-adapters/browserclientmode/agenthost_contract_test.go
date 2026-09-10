package browserclientmode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestNativeBrowserManifestKeepsAgentHostV1ProxyArguments(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			// Literal installed v1 contract: deriving expected argv from the new
			// leaf would let both sides silently change together and still pass.
			server := map[string]any{
				"command": "/usr/local/bin/openlinker",
				"args":    []string{"plugin", "browser-proxy", "--host", provider},
			}
			if provider == "codex" {
				server["cwd"] = "/workspace"
				server["env_vars"] = []string{"OPENLINKER_BROWSER_TOOL_SOCKET"}
			} else {
				server["env"] = map[string]string{"OPENLINKER_BROWSER_TOOL_SOCKET": "${OPENLINKER_BROWSER_TOOL_SOCKET}"}
			}
			path := filepath.Join(t.TempDir(), "browser-mcp.json")
			write := func() {
				t.Helper()
				raw, err := json.Marshal(map[string]any{"mcpServers": map[string]any{"openlinker_browser": server}})
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, raw, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			write()
			if err := validateBrowserOnlyMCP(path, provider); err != nil {
				t.Fatalf("existing v1 Browser contract rejected: %v", err)
			}
			server["args"] = []string{"plugin", "delegation-proxy", "--host", provider}
			write()
			if err := validateBrowserOnlyMCP(path, provider); err == nil {
				t.Fatal("Browser validator accepted a non-Browser proxy contract")
			}
		})
	}
}
