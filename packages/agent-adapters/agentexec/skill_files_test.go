package agentexec

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/skillpackages"
	"github.com/OpenLinker-ai/openlinker-plugin/packages/agent-adapters/skillfiles"
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
			`mcp_servers.openlinker_skills.args=["plugin","skill-files","--host","codex","--root","/skills/agent/digest-a","--root","/skills/agent/digest-b","--file","/skills/agent/digest-a/SKILL.md","--file","/skills/agent/digest-a/references/proof.txt","--file","/skills/agent/digest-b/SKILL.md"]`,
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
			!slices.Equal(server.Args, []string{"plugin", "skill-files", "--host", "claude", "--root", "/skills/agent/digest-a", "--root", "/skills/agent/digest-b", "--file", "/skills/agent/digest-a/SKILL.md", "--file", "/skills/agent/digest-a/references/proof.txt", "--file", "/skills/agent/digest-b/SKILL.md"}) {
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
	stale.skillFiles = []string{"/skills/agent/old/SKILL.md"}
	cleared, _ := providerConfigForSkillFiles(stale, RunContext{})
	if len(cleared.skillFileRoots) != 0 || len(cleared.skillFiles) != 0 {
		t.Fatal("package directories from an earlier Run leaked into the next Run")
	}
}

// The server must serve the loader's verified manifest, including legal names
// that merely resemble temporary files, and nothing else found on disk.
func TestBrowserPackageServerFollowsLoadedManifest(t *testing.T) {
	workspace := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(workspace); err == nil {
		workspace = resolved
	}
	payload, _ := json.Marshal(packageContents{Name: "deploy", Description: "Deploy", Providers: []string{"codex"}, Files: map[string]string{
		"SKILL.md":                            "---\nname: deploy\ndescription: Deploy\n---\nread references/deploy.pending-review.md",
		"references/deploy.pending-review.md": "REVIEW-9a2b",
	}})
	digest := sha256.Sum256(payload)
	snapshot := packageSnapshot{Schema: 1, Bundles: []packageVersion{{BindingID: "11111111-1111-4111-8111-111111111111", PackageID: "22222222-2222-4222-8222-222222222222", VersionID: "33333333-3333-4333-8333-333333333333", Version: "1.0.0", Digest: hex.EncodeToString(digest[:]), Payload: string(payload)}}}
	loaded, err := skillpackages.Load(context.Background(), skillpackages.Request{Snapshot: snapshot, AgentID: "55555555-5555-4555-8555-555555555555", Trusted: true, Emit: func(string, any) error { return nil }}, "codex", workspace, skillpackages.Cache{})
	if err != nil || len(loaded.Packages) != 1 {
		t.Fatalf("load: %#v %v", loaded, err)
	}
	directory := loaded.Packages[0].Directory
	if err := os.Chmod(filepath.Join(directory, "references"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "references", "planted.md"), []byte("PLANTED"), 0o600); err != nil {
		t.Fatal(err)
	}
	config, _ := providerConfigForSkillFiles(browserSkillConfig("codex", "native"), RunContext{LoadedSkillPackages: loaded.Packages})
	args := skillFilesArguments(config, "codex")
	var roots, files []string
	for i := 0; i+1 < len(args); i++ {
		switch args[i] {
		case "--root":
			roots = append(roots, args[i+1])
		case "--file":
			files = append(files, args[i+1])
		}
	}
	server, err := skillfiles.New("codex", "test", roots, files)
	if err != nil {
		t.Fatal(err)
	}
	if text, err := server.Read(filepath.Join(directory, "references", "deploy.pending-review.md")); err != nil || text != "REVIEW-9a2b" {
		t.Fatalf("legal manifest file was not served: %q %v", text, err)
	}
	listing, err := server.Read(directory)
	if err != nil || !strings.Contains(listing, "deploy.pending-review.md") || strings.Contains(listing, "planted") {
		t.Fatalf("listing: %q %v", listing, err)
	}
	if text, err := server.Read(filepath.Join(directory, "references", "planted.md")); err == nil {
		t.Fatalf("file outside the manifest was served: %q", text)
	}
}

// Two pinned packages with identical content (32 files each) share one digest
// directory. Core and the loader accept them; the Browser file server must too.
func TestBrowserPackageServerAcceptsIdenticalPackages(t *testing.T) {
	workspace := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(workspace); err == nil {
		workspace = resolved
	}
	files := map[string]string{"SKILL.md": "---\nname: same\ndescription: Same\n---\nread references"}
	for i := 1; i < 32; i++ {
		files[fmt.Sprintf("references/file-%02d.md", i)] = fmt.Sprintf("FILE-%02d", i)
	}
	payload, _ := json.Marshal(packageContents{Name: "same", Description: "Same", Providers: []string{"codex"}, Files: files})
	digest := sha256.Sum256(payload)
	version := func(n string) packageVersion {
		return packageVersion{BindingID: "1111111" + n + "-1111-4111-8111-111111111111", PackageID: "2222222" + n + "-2222-4222-8222-222222222222", VersionID: "3333333" + n + "-3333-4333-8333-333333333333", Version: "1.0.0", Digest: hex.EncodeToString(digest[:]), Payload: string(payload)}
	}
	snapshot := packageSnapshot{Schema: 1, Bundles: []packageVersion{version("1"), version("2")}}
	loaded, err := skillpackages.Load(context.Background(), skillpackages.Request{Snapshot: snapshot, AgentID: "55555555-5555-4555-8555-555555555555", Trusted: true, Emit: func(string, any) error { return nil }}, "codex", workspace, skillpackages.Cache{})
	if err != nil || len(loaded.Packages) != 2 || loaded.Packages[0].Directory != loaded.Packages[1].Directory {
		t.Fatalf("identical packages should share one directory: %#v %v", loaded, err)
	}
	config, run := providerConfigForSkillFiles(browserSkillConfig("codex", "native"), RunContext{LoadedSkillPackages: loaded.Packages})
	if !run.SkillFilesTool || len(config.skillFileRoots) != 1 || len(config.skillFiles) != 32 {
		t.Fatalf("Host should pass the shared directory once: %d roots, %d files", len(config.skillFileRoots), len(config.skillFiles))
	}
	server, err := skillfiles.New("codex", "test", config.skillFileRoots, config.skillFiles)
	if err != nil {
		t.Fatalf("server rejected identical packages: %v", err)
	}
	if text, err := server.Read(filepath.Join(loaded.Packages[0].Directory, "references", "file-31.md")); err != nil || text != "FILE-31" {
		t.Fatalf("shared file: %q %v", text, err)
	}
}
