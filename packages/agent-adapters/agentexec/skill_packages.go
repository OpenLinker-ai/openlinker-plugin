package agentexec

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/providerprocess"
	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/skillpackages"
	openlinker "github.com/OpenLinker-ai/openlinker-go"
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
	return skillpackages.Instructions(run.LoadedSkillPackages, run.SkillPackagesAlreadyLoaded)
}
