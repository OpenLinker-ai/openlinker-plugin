package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPackagedHostServesNativeAndBrowserMCPWithoutCLI(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "host")
	build := exec.Command("go", "build", "-mod=readonly", "-o", binary, ".")
	build.Env = append(os.Environ(), "GOWORK=off")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}
	for _, host := range []string{"codex", "claude"} {
		for _, surface := range []string{"serve", "browser-serve"} {
			t.Run(host+"/"+surface, func(t *testing.T) {
				command := exec.Command(binary, "plugin", surface, "--host", host)
				command.Dir = t.TempDir()
				command.Env = []string{"PATH=/nonexistent", "HOME=" + command.Dir, "OPENLINKER_AGENT_CONFIG=" + filepath.Join(command.Dir, "agent.json")}
				command.Stdin = strings.NewReader("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\"}\n{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/list\"}\n")
				output, err := command.Output()
				if err != nil {
					t.Fatalf("MCP: %v", err)
				}
				responses := map[int]struct {
					Result struct {
						Tools []struct {
							Name string `json:"name"`
						} `json:"tools"`
					} `json:"result"`
				}{}
				for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
					var response struct {
						ID     int `json:"id"`
						Result struct {
							Tools []struct {
								Name string `json:"name"`
							} `json:"tools"`
						} `json:"result"`
					}
					if err := json.Unmarshal([]byte(line), &response); err != nil {
						t.Fatal(err)
					}
					responses[response.ID] = struct {
						Result struct {
							Tools []struct {
								Name string `json:"name"`
							} `json:"tools"`
						} `json:"result"`
					}{Result: response.Result}
				}
				tools := responses[2].Result.Tools
				if len(responses) != 2 {
					t.Fatalf("missing protocol output: %s", output)
				}
				if surface == "browser-serve" {
					if len(tools) != 1 || tools[0].Name != "browser_session" {
						t.Fatalf("Browser tools: %s", output)
					}
				} else if len(tools) != 14 {
					t.Fatalf("native tools changed: %s", output)
				}
			})
		}
	}
}
