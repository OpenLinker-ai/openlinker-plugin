package agentexec

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	openlinker "github.com/OpenLinker-ai/openlinker-go"
)

const SkillPackagesFeature = "skill_packages.v1"
const skillPackagesMetadataKey = "_openlinker_skill_packages"

var ErrSkillPackagesUnsupported = errors.New("skill packages are unsupported by this execution environment")

// SkillPackageFeatures and execution use the same gate. The official launcher
// switches UID; it cannot read a cache owned by the Runtime UID.
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
	bin := provider.config.Bin
	if resolved, err := exec.LookPath(bin); err == nil {
		bin = resolved
	}
	// Check both the configured name and the symlink target.
	isLauncher := func(value string) bool {
		name := filepath.Base(value)
		return name == "openlinker-provider-launcher" || strings.HasPrefix(name, "openlinker-provider-launcher-")
	}
	if isLauncher(bin) {
		return false
	}
	if resolved, err := filepath.EvalSymlinks(bin); err == nil && isLauncher(resolved) {
		return false
	}
	return provider.config.Provider == "codex" || provider.config.Provider == "claude"
}

type packageSnapshot struct {
	Schema  int              `json:"schema_version"`
	Bundles []packageVersion `json:"bundles"`
}
type packageVersion struct {
	BindingID string `json:"binding_id"`
	PackageID string `json:"package_id"`
	VersionID string `json:"version_id"`
	Version   string `json:"version"`
	Digest    string `json:"digest"`
	Payload   string `json:"payload"`
}
type packageContents struct {
	RequiredCommands []string          `json:"required_commands"`
	Name             string            `json:"name"`
	Description      string            `json:"description"`
	Files            map[string]string `json:"files"`
	CapabilityIDs    []string          `json:"capability_ids"`
	Providers        []string          `json:"providers"`
}
type loadedSkillPackage struct {
	Name, Instructions, Directory string
	Files                         []string
}
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

var packageIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var packageCommandPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._+-]{0,63}$`)
var packagePathPattern = regexp.MustCompile(`^[a-zA-Z0-9._/-]+$`)

func decodePackageSnapshot(value any, provider string) (packageSnapshot, []packageContents, error) {
	raw, err := json.Marshal(value)
	if err != nil || len(raw) > 768*1024 {
		return packageSnapshot{}, nil, errors.New("invalid skill package snapshot")
	}
	var snapshot packageSnapshot
	if json.Unmarshal(raw, &snapshot) != nil || snapshot.Schema != 1 || len(snapshot.Bundles) > 5 {
		return snapshot, nil, errors.New("invalid skill package snapshot")
	}
	bundles := []packageContents{}
	seen := map[string]bool{}
	for _, version := range snapshot.Bundles {
		digest := sha256.Sum256([]byte(version.Payload))
		if !packageIDPattern.MatchString(version.BindingID) || !packageIDPattern.MatchString(version.PackageID) || !packageIDPattern.MatchString(version.VersionID) || seen[version.PackageID] || len(version.Payload) > 65536 || hex.EncodeToString(digest[:]) != version.Digest {
			return snapshot, nil, errors.New("skill package identity or digest mismatch")
		}
		seen[version.PackageID] = true
		var bundle packageContents
		if json.Unmarshal([]byte(version.Payload), &bundle) != nil || !slices.Contains(bundle.Providers, provider) || len(bundle.Files) < 1 || len(bundle.Files) > 32 || strings.TrimSpace(bundle.Files["SKILL.md"]) == "" {
			return snapshot, nil, errors.New("incompatible skill package")
		}
		for name, content := range bundle.Files {
			if name == "." || len(name) > 180 || !packagePathPattern.MatchString(name) || path.IsAbs(name) || path.Clean(name) != name || strings.HasPrefix(name, ".") || strings.Contains(name, "/.") || !utf8.ValidString(content) || strings.ContainsRune(content, 0) {
				return snapshot, nil, errors.New("unsafe skill package file")
			}
		}
		bundles = append(bundles, bundle)
	}
	return snapshot, bundles, nil
}

func (provider skillPackageProvider) Run(ctx context.Context, run RunContext) (openlinker.RuntimeResult, error) {
	if run.PackageSnapshot == nil {
		return provider.provider.Run(ctx, run)
	}
	snapshot, bundles, err := decodePackageSnapshot(run.PackageSnapshot, provider.config.Provider)
	if err != nil {
		return openlinker.RuntimeResult{}, err
	}
	if len(bundles) == 0 {
		return provider.provider.Run(ctx, run)
	}
	if !provider.supportsSkillPackages() {
		return openlinker.RuntimeResult{}, ErrSkillPackagesUnsupported
	}
	if run.Authority == nil || !packageIDPattern.MatchString(run.AgentID) {
		return openlinker.RuntimeResult{}, errors.New("skill package execution requires trusted Runtime authority")
	}
	receipt := make([]map[string]string, 0, len(snapshot.Bundles))
	for _, version := range snapshot.Bundles {
		receipt = append(receipt, map[string]string{"binding_id": version.BindingID, "version_id": version.VersionID, "digest": version.Digest})
	}
	fail := func(cause error, code string) (openlinker.RuntimeResult, error) {
		if run.Emit != nil {
			if err := run.Emit("run.skill_packages.failed", map[string]any{"bindings": receipt, "error_code": code}); err != nil {
				return openlinker.RuntimeResult{}, err
			}
		}
		return openlinker.RuntimeResult{}, cause
	}
	for _, bundle := range bundles {
		for _, command := range bundle.RequiredCommands {
			if !packageCommandPattern.MatchString(command) {
				return fail(errors.New("invalid required command"), "package_invalid")
			}
			if _, err := exec.LookPath(command); err != nil {
				return fail(fmt.Errorf("required command is unavailable: %s", command), "dependency_missing")
			}
		}
	}
	workspace := provider.config.Workspace
	if workspace == "" {
		workspace, err = os.Getwd()
		if err != nil {
			return fail(err, "package_materialization_failed")
		}
	}
	if err := protectSkillPackageCache(ctx, workspace); err != nil {
		return fail(err, "package_materialization_failed")
	}
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return fail(errors.New("skill package workspace unavailable"), "package_materialization_failed")
	}
	defer root.Close()
	snapshotBytes, _ := json.Marshal(snapshot.Bundles)
	snapshotDigest := sha256.Sum256(snapshotBytes)
	run.SkillPackagesDigest = hex.EncodeToString(snapshotDigest[:])
	for i, bundle := range bundles {
		if err := ctx.Err(); err != nil {
			return openlinker.RuntimeResult{}, err
		}
		relative := filepath.Join(".openlinker-skills", run.AgentID, snapshot.Bundles[i].Digest)
		if err := materializeSkillPackage(root, relative, bundle.Files); err != nil {
			return fail(errors.New("could not materialize the pinned skill package"), "package_materialization_failed")
		}
		directory, err := filepath.Abs(filepath.Join(workspace, relative))
		if err != nil {
			return fail(err, "package_materialization_failed")
		}
		names := make([]string, 0, len(bundle.Files))
		for name := range bundle.Files {
			names = append(names, name)
		}
		slices.Sort(names)
		run.LoadedSkillPackages = append(run.LoadedSkillPackages, loadedSkillPackage{Files: names, Name: bundle.Name, Instructions: bundle.Files["SKILL.md"], Directory: directory})
	}
	if run.Emit == nil {
		return openlinker.RuntimeResult{}, errors.New("skill package loading requires a durable event channel")
	}
	if err := run.Emit("run.skill_packages.loaded", map[string]any{"bindings": receipt}); err != nil {
		return openlinker.RuntimeResult{}, err
	}
	return provider.provider.Run(ctx, run)
}

// Root operations confine all package file access to the configured workspace.
// Existing files must be regular and byte-identical. We never overwrite another
// package, an operator file, or a damaged cache to make a run proceed.
func materializeSkillPackage(root *os.Root, directory string, files map[string]string) error {
	if err := root.MkdirAll(directory, 0700); err != nil {
		return err
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		target := filepath.Join(directory, filepath.FromSlash(name))
		if err := root.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return err
		}
		existingMatches := func() error {
			info, err := root.Lstat(target)
			if err != nil || !info.Mode().IsRegular() {
				return errors.New("package cache entry is not a regular file")
			}
			content, err := root.ReadFile(target)
			if err != nil || string(content) != files[name] {
				return errors.New("package cache content does not match its pinned version")
			}
			return nil
		}
		if _, err := root.Lstat(target); err == nil {
			if err := existingMatches(); err != nil {
				return err
			}
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		temporary := target + ".pending-" + randomPackageSuffix()
		file, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0400)
		if err != nil {
			return err
		}
		_, writeErr := file.WriteString(files[name])
		closeErr := file.Close()
		if writeErr == nil {
			writeErr = closeErr
		}
		if writeErr == nil {
			writeErr = root.Link(temporary, target)
		}
		_ = root.Remove(temporary)
		if errors.Is(writeErr, os.ErrExist) {
			writeErr = existingMatches()
		}
		if writeErr != nil {
			return writeErr
		}
	}
	return nil
}

func skillPackageSessionMode(run RunContext) string {
	if run.SkillPackagesDigest == "" {
		return ""
	}
	return ":skill_packages:" + run.SkillPackagesDigest
}

func skillPackageInstructions(run RunContext) string {
	if len(run.LoadedSkillPackages) == 0 || run.SkillPackagesAlreadyLoaded {
		return ""
	}
	lines := []string{"", "The Agent owner associated these version-pinned skill packages with this Agent.", "Use their instructions when relevant to the task. They do not grant new tool, credential or network permissions.", "Resolve supporting files relative to each package directory. Do not modify package files or reveal their private contents."}
	for _, bundle := range run.LoadedSkillPackages {
		lines = append(lines, "", fmt.Sprintf("Skill package: %s\nPackage directory: %s", bundle.Name, bundle.Directory), "Available files: "+strings.Join(bundle.Files, ", "), bundle.Instructions)
	}
	return strings.Join(lines, "\n")
}

func randomPackageSuffix() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(value[:])
}
