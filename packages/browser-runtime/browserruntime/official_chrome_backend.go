//go:build !windows

package browserruntime

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserprotocol"
)

type OfficialChromeBackendOptions struct {
	Assets             OfficialChromeAssets
	BaseEnvironment    []string
	Locale             string
	Timezone           string
	FontContract       string
	FontManifestSHA256 string
	EgressLabel        string
	EgressProxy        string
	StoreRoot          string
	WorkRoot           string
	RootKeyFile        string
}

type OfficialChromeBackend struct {
	engine *ProfileEngine
}

func NewOfficialChromeBackend(
	options OfficialChromeBackendOptions,
) (*OfficialChromeBackend, error) {
	lock := options.Assets.Lock
	environment := ProfileEnvironment{
		ProfileGeneration: lock.ProfileGeneration,
		EgressLabel:       options.EgressLabel,
		Evidence: browserprotocol.EnvironmentEvidence{
			BrowserEngine:       "chrome",
			BrowserDistribution: lock.ChromeDistribution,
			BrowserVersion:      lock.ChromeVersion,
			BrowserMajorVersion: mustOfficialChromeMajor(lock.ChromeVersion),
			BrowserLocale:       options.Locale,
			BrowserTimezone:     options.Timezone,
			FontContractVersion: options.FontContract,
			FontManifestSHA256:  options.FontManifestSHA256,
		},
	}
	if err := environment.Validate(); err != nil {
		return nil, err
	}
	engine, err := NewProfileEngine(ProfileEngineOptions{
		Process: ProcessEngineOptions{
			Command: []string{lock.EnginePath},
			Environment: officialChromeEnvironment(options.BaseEnvironment, []string{
				"OPENLINKER_NATIVE_CHROME_BINARY=" + lock.ChromePath,
				"OPENLINKER_NATIVE_CHROME_EXTENSION_ROOT=" + lock.ExtensionRoot,
				"OPENLINKER_NATIVE_CHROME_EXTENSION_ID=" + lock.ExtensionID,
				"OPENLINKER_NATIVE_CHROME_EXTENSION_VERSION=" + lock.ExtensionVersion,
				"OPENLINKER_NATIVE_CHROME_ACTIVATION_PATH=" + lock.ExtensionActivationPath,
				"OPENLINKER_NATIVE_CHROME_HOST=" + lock.NativeHostPath,
				"OPENLINKER_NATIVE_CHROME_PROTOCOL=" + lock.NativeHostProtocol,
				"OPENLINKER_NATIVE_CHROME_ASSET_MANIFEST_SHA256=" + options.Assets.ManifestSHA256,
				"OPENLINKER_NATIVE_CHROME_ENABLED=true",
				"OPENLINKER_NATIVE_CHROME_REQUIRE_ORIGIN=true",
				"OPENLINKER_NATIVE_CHROME_SOCKET=" + filepath.Join(options.WorkRoot, "native-host.sock"),
				"OPENLINKER_BROWSER_EXECUTABLE_PATH=" + lock.ChromePath,
				"OPENLINKER_BROWSER_EGRESS_PROXY=" + options.EgressProxy,
				"OPENLINKER_BROWSER_PROFILE_DIR=" + filepath.Join(options.WorkRoot, "active"),
				"OPENLINKER_BROWSER_ENGINE=chrome",
				"OPENLINKER_BROWSER_DISTRIBUTION=" + lock.ChromeDistribution,
				"OPENLINKER_BROWSER_VERSION=" + lock.ChromeVersion,
				"OPENLINKER_BROWSER_LOCALE=" + options.Locale,
				"OPENLINKER_BROWSER_TIMEZONE=" + options.Timezone,
				"OPENLINKER_BROWSER_FONT_CONTRACT_VERSION=" + options.FontContract,
				"OPENLINKER_BROWSER_FONT_MANIFEST_SHA256=" + options.FontManifestSHA256,
				"OPENLINKER_BROWSER_PROFILE_GENERATION=" + strconv.FormatUint(lock.ProfileGeneration, 10),
			}),
		},
		Environment: environment,
		StoreRoot:   options.StoreRoot,
		WorkRoot:    options.WorkRoot,
		RootKeyFile: options.RootKeyFile,
	})
	if err != nil {
		return nil, err
	}
	return &OfficialChromeBackend{engine: engine}, nil
}

func (backend *OfficialChromeBackend) AbortStartup() error {
	if backend == nil || backend.engine == nil {
		return nil
	}
	return backend.engine.AbortActive()
}

func (backend *OfficialChromeBackend) Execute(
	ctx context.Context,
	identity browserprotocol.Identity,
	action browserprotocol.Action,
) (browserprotocol.Observation, *browserprotocol.Failure) {
	return backend.engine.Execute(ctx, identity, action)
}

func (backend *OfficialChromeBackend) ExecuteViewer(
	ctx context.Context,
	identity browserprotocol.Identity,
	operation browserprotocol.ViewerOperation,
	input *browserprotocol.ViewerInput,
) (*browserprotocol.ViewerFrame, *browserprotocol.Failure) {
	return backend.engine.ExecuteViewer(ctx, identity, operation, input)
}

func (backend *OfficialChromeBackend) ObserveOps(
	ctx context.Context,
	runID string,
	operation browserprotocol.OpsObserverOperation,
) (browserprotocol.OpsObserverObservation, bool, *browserprotocol.OpsObserverError) {
	if backend == nil || backend.engine == nil {
		return browserprotocol.OpsObserverObservation{}, false,
			browserprotocol.NewOpsObserverError(
				browserprotocol.OpsObserverRunNotActive,
				"requested Run is not active",
			)
	}
	return backend.engine.ObserveOps(ctx, runID, operation)
}

func (backend *OfficialChromeBackend) Close() error {
	if backend == nil || backend.engine == nil {
		return nil
	}
	return backend.engine.Close()
}

func (backend *OfficialChromeBackend) ProfileSelectionEvidence() (uint64, bool, bool) {
	if backend == nil || backend.engine == nil {
		return 0, false, false
	}
	return backend.engine.ProfileSelectionEvidence()
}

func (backend *OfficialChromeBackend) StartupFallbackReason(
	failure *browserprotocol.Failure,
) string {
	if failure == nil {
		return "official_chrome_start_failed"
	}
	switch failure.Code {
	case browserprotocol.ErrorEgressUnavailable:
		return "official_egress_preflight_failed"
	case browserprotocol.ErrorProfileLocked,
		browserprotocol.ErrorProfileCorrupt,
		browserprotocol.ErrorProfileEnvironmentMismatch,
		browserprotocol.ErrorProfileEngineDowngrade,
		browserprotocol.ErrorProfileEngineUpgrade:
		return "official_profile_preflight_failed"
	case browserprotocol.ErrorProtocolInvalid, browserprotocol.ErrorOutputInvalid:
		return "official_protocol_mismatch"
	default:
		return "official_chrome_start_failed"
	}
}

func mustOfficialChromeMajor(version string) int {
	major, _ := browserMajorVersion(version)
	return major
}

func officialChromeEnvironment(base, overrides []string) []string {
	result := append([]string(nil), base...)
	for _, override := range overrides {
		name, _, found := strings.Cut(override, "=")
		if !found || name == "" {
			continue
		}
		filtered := result[:0]
		for _, existing := range result {
			existingName, _, existingFound := strings.Cut(existing, "=")
			if !existingFound || existingName != name {
				filtered = append(filtered, existing)
			}
		}
		result = append(filtered, override)
	}
	return result
}
