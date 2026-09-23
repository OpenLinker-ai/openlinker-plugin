package agentexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/providerprocess"
	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/skillpackages"
	openlinker "github.com/OpenLinker-ai/openlinker-go"
	"github.com/OpenLinker-ai/openlinker-plugin/packages/agent-adapters/skillfiles"
)

const SkillPackagesFeature = skillpackages.Feature
const skillPackagesMetadataKey = skillpackages.MetadataKey

var ErrSkillPackagesUnsupported = errors.New("skill packages are unsupported by this execution environment")

func (handler Handler) SkillPackageFeatures() []string {
	provider, ok := handler.Provider.(skillPackageProvider)
	if !ok || !provider.supportsSkillPackages() {
		return nil
	}
	return []string{SkillPackagesFeature, "skill_packages." + provider.config.Provider + ".v1"}
}

func (provider skillPackageProvider) supportsSkillPackages() bool {
	if provider.config.DisableSkillPackages {
		return false
	}
	if skillPackageLauncher(provider.config.Bin) {
		return provider.sharedSkillCacheReady()
	}
	if provider.config.SkillPackageCache.GroupID != 0 && !provider.sharedSkillCacheReady() {
		return false
	}
	return provider.config.Provider == "codex" || provider.config.Provider == "claude"
}

func skillPackageLauncher(bin string) bool {
	if resolved, err := exec.LookPath(bin); err == nil {
		bin = resolved
	}
	// Check both the configured name and the symlink target.
	isLauncher := func(value string) bool {
		name := filepath.Base(value)
		return name == "openlinker-provider-launcher" || strings.HasPrefix(name, "openlinker-provider-launcher-")
	}
	if isLauncher(bin) {
		return true
	}
	if resolved, err := filepath.EvalSymlinks(bin); err == nil && isLauncher(resolved) {
		return true
	}
	return false
}

type loadedSkillPackage = skillpackages.Package

type skillPackageProvider struct {
	provider Provider
	config   ProviderConfig
}

func withSkillPackages(provider Provider, err error, config ProviderConfig) (Provider, error) {
	if err != nil {
		return nil, err
	}
	return skillPackageProvider{provider: provider, config: config}, nil
}
func (provider skillPackageProvider) sharedSkillCacheReady() bool {
	cache := provider.config.SkillPackageCache
	return cache.GroupID == 10003 && cache.Directory == "/skills" && skillpackages.CheckSharedCache(cache.Directory, cache.GroupID) == nil
}
func (provider skillPackageProvider) Run(ctx context.Context, run RunContext) (openlinker.RuntimeResult, error) {
	if run.PackageSnapshot != nil && !provider.supportsSkillPackages() {
		snapshot, _, err := skillpackages.Decode(run.PackageSnapshot, provider.config.Provider)
		if err != nil {
			return openlinker.RuntimeResult{}, err
		}
		if len(snapshot.Bundles) > 0 {
			return openlinker.RuntimeResult{}, ErrSkillPackagesUnsupported
		}
	}
	loaded, err := skillpackages.Load(ctx, skillpackages.Request{Snapshot: run.PackageSnapshot, AgentID: run.AgentID, Trusted: run.Authority != nil, Emit: run.Emit, CheckCommands: provider.checkCommands}, provider.config.Provider, provider.config.Workspace, provider.config.SkillPackageCache)
	if err != nil {
		return openlinker.RuntimeResult{}, err
	}
	run.SkillPackagesDigest, run.LoadedSkillPackages = loaded.Digest, loaded.Packages
	return provider.provider.Run(ctx, run)
}

func (provider skillPackageProvider) checkCommands(ctx context.Context, names []string) error {
	environment := provider.config.Env
	if environment == nil {
		environment = os.Environ()
	}
	if !skillPackageLauncher(provider.config.Bin) {
		return skillpackages.CheckCommands(ctx, names, environment, provider.config.Workspace, nil)
	}
	// The production launcher resolves names only after its full UID/group/
	// capability drop. It does not launch a model or execute the prerequisites.
	command := exec.CommandContext(ctx, provider.config.Bin, append([]string{"--openlinker-check-commands"}, names...)...)
	command.Dir = provider.config.Workspace
	command.Env = providerprocess.Environment(environment, nil)
	if out, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("Provider prerequisite check failed: %s", boundedText(strings.TrimSpace(string(out)), 300, err.Error()))
	}
	return nil
}
func skillPackageSessionMode(run RunContext) string {
	return skillpackages.SessionMode(run.SkillPackagesDigest)
}
func skillPackageInstructions(run RunContext) string {
	instructions := skillpackages.Instructions(run.LoadedSkillPackages, run.SkillPackagesAlreadyLoaded)
	if instructions == "" || !run.SkillFilesTool {
		return instructions
	}
	// Browser entries have no shell or file tool; every turn names the one
	// read-only path so a resumed or compacted session can still reread files.
	return instructions + "\nThis entry has no local file tool. Read package files only with the " +
		skillFilesServerName + " " + skillfiles.ToolName + " tool, passing an absolute path inside a package directory above; call it on a package directory to list its files."
}

const skillFilesServerName = "openlinker_skills"

// The Browser profile disables shell and file tools, so without this server
// the model could see SKILL.md in the prompt but never read supporting files.
func providerConfigForSkillFiles(config ProviderConfig, run RunContext) (ProviderConfig, RunContext) {
	config.skillFileRoots = nil
	run.SkillFilesTool = false
	if !browserProfileEnabled(config) || strings.TrimSpace(config.BrowserPluginBin) == "" || len(run.LoadedSkillPackages) == 0 {
		return config, run
	}
	for _, loaded := range run.LoadedSkillPackages {
		config.skillFileRoots = append(config.skillFileRoots, loaded.Directory)
	}
	run.SkillFilesTool = true
	return config, run
}

func skillFilesArguments(config ProviderConfig, host string) []string {
	args := []string{"plugin", "skill-files", "--host", host}
	for _, root := range config.skillFileRoots {
		args = append(args, "--root", root)
	}
	return args
}

func codexSkillFilesMCPArguments(config ProviderConfig) []string {
	if len(config.skillFileRoots) == 0 {
		return nil
	}
	command, _ := json.Marshal(config.BrowserPluginBin)
	arguments, _ := json.Marshal(skillFilesArguments(config, "codex"))
	tools, _ := json.Marshal([]string{skillfiles.ToolName})
	prefix := "mcp_servers." + skillFilesServerName + "."
	return []string{
		"-c", prefix + "command=" + string(command),
		"-c", prefix + "args=" + string(arguments),
		"-c", prefix + "env_vars=[]",
		"-c", prefix + "required=true",
		"-c", prefix + "enabled_tools=" + string(tools),
		"-c", prefix + `default_tools_approval_mode="approve"`,
	}
}

func claudeSkillFilesMCPServer(config ProviderConfig) map[string]any {
	return map[string]any{
		"type": "stdio", "command": config.BrowserPluginBin,
		"args": skillFilesArguments(config, "claude"), "env": map[string]string{},
	}
}

func claudeSkillFilesTool() string {
	return "mcp__" + skillFilesServerName + "__" + skillfiles.ToolName
}
