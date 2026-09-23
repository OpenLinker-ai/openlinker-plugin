package agentexec

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func skillFilesRun() RunContext {
	return RunContext{LoadedSkillPackages: []loadedSkillPackage{
		{Name: "proof", Instructions: "---\nname: proof\n---\nread references/proof.txt", Directory: "/skills/agent/digest-a", Files: []string{"SKILL.md", "references/proof.txt"}},
		{Name: "style", Instructions: "---\nname: style\n---\nuse the guide", Directory: "/skills/agent/digest-b", Files: []string{"SKILL.md"}},
	}}
}

func browserSkillConfig(provider, mode string) ProviderConfig {
	return providerConfigForBrowserRun(ProviderConfig{
		Provider:                   provider,
		ExecutionProfile:           "browser",
		BrowserClientModeRequested: mode,
		BrowserClientMode:          mode,
		BrowserNativePlugin:        "/opt/openlinker/agent-runtime-plugin/" + provider,
	}, &BrowserRunContext{PluginBin: "/usr/local/bin/openlinker-plugin-host", ToolSocket: "/browser/tool.sock"})
}

func TestBrowserEntriesServePinnedPackageFilesReadOnly(t *testing.T) {
	for _, mode := range []string{"native", "mcp"} {
		config, run := providerConfigForSkillFiles(browserSkillConfig("codex", mode), skillFilesRun())
		if !run.SkillFilesTool {
			t.Fatalf("%s Browser entry did not enable the package file tool", mode)
		}
		codexArgs := strings.Join(codexAppServerArguments(config, "/workspace", "read-only"), " ")
		for _, expected := range []string{
			`mcp_servers.openlinker_skills.command="/usr/local/bin/openlinker-plugin-host"`,
			`mcp_servers.openlinker_skills.args=["plugin","skill-files","--host","codex","--root","/skills/agent/digest-a","--root","/skills/agent/digest-b"]`,
			`mcp_servers.openlinker_skills.enabled_tools=["read_skill_file"]`,
			"mcp_servers.openlinker_skills.env_vars=[]",
			"--disable shell_tool",
		} {
			if mode == "mcp" && expected == "--disable shell_tool" {
				continue
			}
			if !strings.Contains(codexArgs, expected) {
				t.Fatalf("%s Codex config is missing %q: %s", mode, expected, codexArgs)
			}
		}
		prompt := buildCodexPrompt(run, false, true)
		if !strings.Contains(prompt, "openlinker_skills read_skill_file") || !strings.Contains(prompt, "/skills/agent/digest-a") {
			t.Fatalf("%s prompt does not name the package file tool: %s", mode, prompt)
		}
		resumed := run
		resumed.SkillPackagesAlreadyLoaded = true
		if !strings.Contains(buildCodexPrompt(resumed, false, true), "read_skill_file") {
			t.Fatalf("%s resumed prompt lost the only way to reread package files", mode)
		}
	}

	for _, mode := range []string{"native", "mcp"} {
		config, _ := providerConfigForSkillFiles(browserSkillConfig("claude", mode), skillFilesRun())
		args := claudeArguments(config, "dontAsk", "")
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "--allowedTools mcp__openlinker_browser__browser_session,mcp__openlinker_skills__read_skill_file") {
			t.Fatalf("%s Claude does not allow exactly the package file tool: %s", mode, joined)
		}
		index := slices.Index(args, "--mcp-config")
		if index < 0 {
			t.Fatalf("%s Claude has no MCP config: %s", mode, joined)
		}
		var payload struct {
			MCPServers map[string]struct {
				Command string   `json:"command"`
				Args    []string `json:"args"`
			} `json:"mcpServers"`
		}
		if err := json.Unmarshal([]byte(args[index+1]), &payload); err != nil {
			t.Fatal(err)
		}
		server, ok := payload.MCPServers["openlinker_skills"]
		if !ok || server.Command != "/usr/local/bin/openlinker-plugin-host" ||
			!slices.Equal(server.Args, []string{"plugin", "skill-files", "--host", "claude", "--root", "/skills/agent/digest-a", "--root", "/skills/agent/digest-b"}) {
			t.Fatalf("%s Claude package server: %#v", mode, payload.MCPServers)
		}
		if mode == "native" && (!strings.Contains(joined, "--plugin-dir") || strings.Contains(joined, "--strict-mcp-config")) {
			t.Fatalf("native Claude must keep its Browser plugin alongside the package server: %s", joined)
		}
	}
}

func TestPackageFileServerOnlyForBrowserRunsWithPackages(t *testing.T) {
	standard, run := providerConfigForSkillFiles(ProviderConfig{Provider: "codex"}, skillFilesRun())
	if run.SkillFilesTool || len(standard.skillFileRoots) != 0 {
		t.Fatal("standard entries keep their own file tools and must not get the package server")
	}
	if strings.Contains(strings.Join(codexAppServerArguments(standard, "/workspace", "read-only"), " "), "openlinker_skills") {
		t.Fatal("standard Codex arguments changed")
	}
	if strings.Contains(buildCodexPrompt(run, false, false), "read_skill_file") {
		t.Fatal("standard prompt names a tool that is not configured")
	}
	browser, run := providerConfigForSkillFiles(browserSkillConfig("codex", "native"), RunContext{})
	if run.SkillFilesTool || strings.Contains(strings.Join(codexAppServerArguments(browser, "/workspace", "read-only"), " "), "openlinker_skills") {
		t.Fatal("Browser runs without packages must not start the package server")
	}
	stale := browserSkillConfig("claude", "mcp")
	stale.skillFileRoots = []string{"/skills/agent/old"}
	cleared, _ := providerConfigForSkillFiles(stale, RunContext{})
	if len(cleared.skillFileRoots) != 0 {
		t.Fatal("package directories from an earlier Run leaked into the next Run")
	}
}
