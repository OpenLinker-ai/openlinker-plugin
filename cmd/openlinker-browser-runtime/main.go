//go:build !windows

package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserclient"
	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserprotocol"
	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserruntime"
)

var opsViewerRunIDPattern = regexp.MustCompile(
	`^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`,
)

const (
	defaultBrowserSocket       = "/browser-control/openlinker.browser.sock"
	defaultActiveLeaseFile     = "/browser-control/leases/active-lease.json"
	defaultEngineExecutable    = "/usr/bin/node"
	defaultEngineScript        = "/opt/openlinker/browser-engine/dist/main.js"
	defaultProfileStore        = "/browser-state/encrypted-profiles"
	defaultProfileWorkRoot     = "/browser-tmp/profiles"
	defaultProfileRootKey      = "/browser-key/profile-root-key"
	defaultProfileDirectory    = "/browser-tmp/profiles/active"
	defaultBrowserEngine       = "chromium"
	defaultBrowserDistribution = "playwright_chromium"
	defaultBrowserVersion      = "149.0.7827.0"
	defaultBrowserLocale       = "en-US"
	defaultBrowserTimezone     = "UTC"
	defaultFontContractVersion = "openlinker.browser.fonts.v1"
	defaultEgressLabel         = "default"
	defaultOfficialChromeLock  = "/opt/openlinker/native-chrome/assets.lock.json"
	defaultOpsObserverSocket   = "/browser-ops/openlinker.browser.ops-viewer.sock"
	defaultOpsCredential       = "/browser-ops/channel-credential"
	// The authenticated observation bridge lives beside the Worker's existing
	// browser-control mount. It deliberately does not reuse the operator ops
	// credential, whose authority is wider than observation needs.
	defaultObserverSocket     = "/browser-control/openlinker.browser.observer.sock"
	defaultObserverCredential = "/browser-control/observer-credential"
	defaultHealthcheckTimeout = 2 * time.Second
	maxCredentialBytes        = 4096
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "openlinker Browser Runtime:", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) > 1 {
		if len(os.Args) == 2 && os.Args[1] == "healthcheck" {
			return runHealthcheck()
		}
		if os.Args[1] == "ops-viewer-stream" {
			return runOpsViewerStream(os.Args[2:], os.Stdout)
		}
		return errors.New("Browser Runtime command is invalid")
	}
	opsObserverEnabled, err := booleanValue(
		"OPENLINKER_BROWSER_OPS_VIEWER_ENABLED",
		false,
	)
	if err != nil {
		return err
	}
	if !opsObserverEnabled {
		if err := removeDisabledOpsObserverArtifacts(); err != nil {
			return err
		}
	}
	observationEnabled, err := booleanValue(
		"OPENLINKER_BROWSER_AUTHENTICATED_OBSERVATION_ENABLED",
		false,
	)
	if err != nil {
		return err
	}
	if !observationEnabled {
		// Leaving the socket or credential behind would let a Worker believe the
		// bridge exists after the feature was turned off.
		if err := removeDisabledObservationArtifacts(); err != nil {
			return err
		}
	}
	engineOpsEnabled := engineOpsObservationEnabled(
		opsObserverEnabled,
		observationEnabled,
	)
	socketPath := strings.TrimSpace(os.Getenv("OPENLINKER_BROWSER_SOCKET"))
	if socketPath == "" {
		socketPath = defaultBrowserSocket
	}
	credential, err := loadOrCreateCredentialFile(os.Getenv("OPENLINKER_BROWSER_CHANNEL_CREDENTIAL_FILE"))
	if err != nil {
		return err
	}
	activeLeaseFile := strings.TrimSpace(os.Getenv("OPENLINKER_BROWSER_ACTIVE_LEASE_FILE"))
	if activeLeaseFile == "" {
		activeLeaseFile = defaultActiveLeaseFile
	}
	profileEnvironment, err := browserProfileEnvironment()
	if err != nil {
		return err
	}
	isolated, err := browserruntime.NewProfileEngine(browserruntime.ProfileEngineOptions{
		Process: browserruntime.ProcessEngineOptions{
			Command:     []string{defaultEngineExecutable, defaultEngineScript},
			Environment: browserEngineEnvironment(profileEnvironment, engineOpsEnabled),
		},
		Environment: profileEnvironment,
		StoreRoot:   value("OPENLINKER_BROWSER_PROFILE_STORE", defaultProfileStore),
		WorkRoot:    value("OPENLINKER_BROWSER_PROFILE_WORK_ROOT", defaultProfileWorkRoot),
		RootKeyFile: value(
			"OPENLINKER_BROWSER_PROFILE_ROOT_KEY_FILE",
			defaultProfileRootKey,
		),
	})
	if err != nil {
		return err
	}
	assets, unavailableReason := browserruntime.LoadOfficialChromeAssets(
		browserruntime.OfficialChromeAssetOptions{
			ManifestPath:     value("OPENLINKER_NATIVE_CHROME_ASSET_LOCK", defaultOfficialChromeLock),
			RequireRootOwned: true,
		},
	)
	var official browserruntime.BrowserBackend
	var officialEvidence browserprotocol.BackendSelectionEvidence
	if unavailableReason == "" {
		officialBackend, officialErr := browserruntime.NewOfficialChromeBackend(
			browserruntime.OfficialChromeBackendOptions{
				Assets:             assets,
				BaseEnvironment:    browserEngineEnvironment(profileEnvironment, engineOpsEnabled),
				Locale:             profileEnvironment.Evidence.BrowserLocale,
				Timezone:           profileEnvironment.Evidence.BrowserTimezone,
				FontContract:       profileEnvironment.Evidence.FontContractVersion,
				FontManifestSHA256: profileEnvironment.Evidence.FontManifestSHA256,
				EgressLabel:        value("OPENLINKER_BROWSER_EGRESS_LABEL", defaultEgressLabel),
				EgressProxy:        strings.TrimSpace(os.Getenv("OPENLINKER_BROWSER_EGRESS_PROXY")),
				StoreRoot:          filepath.Join(value("OPENLINKER_BROWSER_PROFILE_STORE", defaultProfileStore), "official_chrome_extension"),
				WorkRoot:           filepath.Join(value("OPENLINKER_BROWSER_PROFILE_WORK_ROOT", defaultProfileWorkRoot), "official_chrome_extension"),
				RootKeyFile:        value("OPENLINKER_BROWSER_PROFILE_ROOT_KEY_FILE", defaultProfileRootKey),
			},
		)
		if officialErr != nil {
			unavailableReason = "official_profile_preflight_failed"
		} else {
			official = officialBackend
			officialEvidence = browserprotocol.BackendSelectionEvidence{
				AssetManifestSHA256: assets.ManifestSHA256,
				ExtensionID:         assets.Lock.ExtensionID,
				ExtensionVersion:    assets.Lock.ExtensionVersion,
				NativeHostProtocol:  assets.Lock.NativeHostProtocol,
				ProfileGeneration:   assets.Lock.ProfileGeneration,
			}
		}
	}
	engine, err := browserruntime.NewBackendSelector(
		browserruntime.BackendSelectorOptions{
			Official:                  official,
			OfficialEvidence:          officialEvidence,
			OfficialUnavailableReason: unavailableReason,
			Isolated:                  isolated,
		},
	)
	if err != nil {
		return errors.Join(err, isolated.Close())
	}
	humanControlAvailable, err := booleanValue(
		"OPENLINKER_BROWSER_HUMAN_CONTROL_ENABLED",
		false,
	)
	if err != nil {
		return errors.Join(err, engine.Close())
	}
	server, err := browserruntime.NewServer(browserruntime.ServerOptions{
		SocketPath:            socketPath,
		ChannelCredential:     credential,
		Lease:                 browserruntime.FileLease{Path: activeLeaseFile},
		Engine:                engine,
		HumanControlAvailable: humanControlAvailable,
	})
	if err != nil {
		return errors.Join(err, engine.Close())
	}
	var opsServer *browserruntime.OpsObserverServer
	if opsObserverEnabled {
		opsCredential, credentialErr := loadOrCreateCredentialFile(
			value("OPENLINKER_BROWSER_OPS_CREDENTIAL_FILE", defaultOpsCredential),
		)
		if credentialErr != nil {
			return errors.Join(credentialErr, engine.Close())
		}
		opsServer, err = browserruntime.NewOpsObserverServer(
			browserruntime.OpsObserverServerOptions{
				SocketPath: value(
					"OPENLINKER_BROWSER_OPS_SOCKET",
					defaultOpsObserverSocket,
				),
				ChannelCredential: opsCredential,
				Observer:          engine,
			},
		)
		if err != nil {
			return errors.Join(err, engine.Close())
		}
	}
	var observerServer *browserruntime.OpsObserverServer
	if observationEnabled {
		observerCredential, credentialErr := loadOrCreateCredentialFile(
			value("OPENLINKER_BROWSER_OBSERVER_CREDENTIAL_FILE", defaultObserverCredential),
		)
		if credentialErr != nil {
			return errors.Join(credentialErr, engine.Close())
		}
		// One Runtime, one observation lease. Sharing the manager is what keeps
		// the operator Viewer and the authenticated bridge from both being
		// admitted; a second private manager would silently allow both.
		leases := browserruntime.NewOpsObserverLeaseManager(nil)
		if opsServer != nil {
			leases = opsServer.Leases()
		}
		observerServer, err = browserruntime.NewOpsObserverServer(
			browserruntime.OpsObserverServerOptions{
				SocketPath: value(
					"OPENLINKER_BROWSER_OBSERVER_SOCKET",
					defaultObserverSocket,
				),
				ChannelCredential: observerCredential,
				Observer:          engine,
				Leases:            leases,
			},
		)
		if err != nil {
			return errors.Join(err, engine.Close())
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	serveContext, cancelServe := context.WithCancel(ctx)
	defer cancelServe()
	serverCount := 1
	serveResults := make(chan error, 3)
	go func() { serveResults <- server.Serve(serveContext) }()
	if opsServer != nil {
		serverCount++
		go func() { serveResults <- opsServer.Serve(serveContext) }()
	}
	if observerServer != nil {
		serverCount++
		go func() { serveResults <- observerServer.Serve(serveContext) }()
	}
	var serveErr error
	completed := 0
	select {
	case <-ctx.Done():
	case serveErr = <-serveResults:
		completed = 1
		if serveErr == nil {
			serveErr = errors.New("Browser Runtime server stopped unexpectedly")
		}
	}
	cancelServe()
	_ = server.Close()
	if opsServer != nil {
		_ = opsServer.Close()
	}
	for completed < serverCount {
		serveErr = errors.Join(serveErr, <-serveResults)
		completed++
	}
	closeErr := engine.Close()
	return errors.Join(serveErr, closeErr)
}

func runHealthcheck() error {
	socketPath := strings.TrimSpace(os.Getenv("OPENLINKER_BROWSER_SOCKET"))
	if socketPath == "" {
		socketPath = defaultBrowserSocket
	}
	credential, err := readCredentialFile(
		os.Getenv("OPENLINKER_BROWSER_CHANNEL_CREDENTIAL_FILE"),
	)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultHealthcheckTimeout)
	defer cancel()
	if err := browserclient.CheckHealth(
		ctx,
		socketPath,
		credential,
		defaultHealthcheckTimeout,
	); err != nil {
		return err
	}
	observationEnabled, err := booleanValue(
		"OPENLINKER_BROWSER_AUTHENTICATED_OBSERVATION_ENABLED",
		false,
	)
	if err != nil {
		return err
	}
	if !observationEnabled {
		return nil
	}
	// With the bridge enabled the Worker declares the observation feature, so an
	// unserved listener would leave Core believing observation works while every
	// start fails. Probing here surfaces that at the deployment gate instead.
	observerCredential, err := readCredentialFile(
		value("OPENLINKER_BROWSER_OBSERVER_CREDENTIAL_FILE", defaultObserverCredential),
	)
	if err != nil {
		return err
	}
	return browserclient.ProbeObserverBridge(
		ctx,
		value("OPENLINKER_BROWSER_OBSERVER_SOCKET", defaultObserverSocket),
		observerCredential,
		defaultHealthcheckTimeout,
	)
}

func browserEngineEnvironment(
	environment browserruntime.ProfileEnvironment,
	opsObserverEnabled bool,
) []string {
	result := []string{
		"HOME=" + value("HOME", "/home/pwuser"),
		"TMPDIR=" + value("TMPDIR", "/browser-tmp"),
		"NO_PROXY=",
		"OPENLINKER_BROWSER_EGRESS_PROXY=" + strings.TrimSpace(os.Getenv("OPENLINKER_BROWSER_EGRESS_PROXY")),
		"OPENLINKER_BROWSER_PROFILE_DIR=" + value("OPENLINKER_BROWSER_PROFILE_DIR", defaultProfileDirectory),
		"OPENLINKER_BROWSER_ENGINE=" + environment.Evidence.BrowserEngine,
		"OPENLINKER_BROWSER_DISTRIBUTION=" + environment.Evidence.BrowserDistribution,
		"OPENLINKER_BROWSER_VERSION=" + environment.Evidence.BrowserVersion,
		"OPENLINKER_BROWSER_LOCALE=" + environment.Evidence.BrowserLocale,
		"OPENLINKER_BROWSER_TIMEZONE=" + environment.Evidence.BrowserTimezone,
		"OPENLINKER_BROWSER_FONT_CONTRACT_VERSION=" + environment.Evidence.FontContractVersion,
		"OPENLINKER_BROWSER_FONT_MANIFEST_SHA256=" + environment.Evidence.FontManifestSHA256,
		"OPENLINKER_BROWSER_PROFILE_GENERATION=" + strconv.FormatUint(
			environment.ProfileGeneration,
			10,
		),
		"OPENLINKER_BROWSER_MAX_ACTIONS_PER_ORIGIN_MINUTE=" + value(
			"OPENLINKER_BROWSER_MAX_ACTIONS_PER_ORIGIN_MINUTE",
			"120",
		),
		"OPENLINKER_BROWSER_MAX_NAVIGATIONS_PER_ORIGIN_MINUTE=" + value(
			"OPENLINKER_BROWSER_MAX_NAVIGATIONS_PER_ORIGIN_MINUTE",
			"20",
		),
		"PLAYWRIGHT_BROWSERS_PATH=" + value("PLAYWRIGHT_BROWSERS_PATH", "/ms-playwright"),
		"LANG=" + value("LANG", "C.UTF-8"),
	}
	if opsObserverEnabled {
		result = append(result, "OPENLINKER_BROWSER_OPS_OBSERVER_ENABLED=true")
	}
	return result
}

// Both read-only entry points depend on the same private Engine Ops socket.
// The operator Viewer and authenticated platform observation have independent
// admission and credentials, but neither can capture a frame without enabling
// this Engine capability.
func engineOpsObservationEnabled(
	opsViewerEnabled bool,
	authenticatedObservationEnabled bool,
) bool {
	return opsViewerEnabled || authenticatedObservationEnabled
}

func runOpsViewerStream(arguments []string, output io.Writer) error {
	runID, ttl, err := parseOpsViewerStreamArguments(arguments)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	leaseContext, cancelLease := context.WithTimeout(ctx, ttl)
	defer cancelLease()
	stream, err := browserclient.NewOpsObserverStream(
		leaseContext,
		browserclient.OpsObserverConfig{
			SocketPath: value(
				"OPENLINKER_BROWSER_OPS_SOCKET",
				defaultOpsObserverSocket,
			),
			CredentialFile: value(
				"OPENLINKER_BROWSER_OPS_CREDENTIAL_FILE",
				defaultOpsCredential,
			),
			RunID: runID,
			TTL:   ttl,
		},
	)
	if err != nil {
		return err
	}
	defer stream.Close()
	encoder := json.NewEncoder(output)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		requestContext, cancel := context.WithTimeout(
			leaseContext,
			browserprotocol.MaxOpsObserverDeadline,
		)
		response, observerErr := stream.Observe(
			requestContext,
			browserprotocol.OpsObserverFrameOperation,
		)
		cancel()
		if response.ContractID != "" {
			if err := encoder.Encode(response); err != nil {
				return errors.New("write Ops Viewer stream")
			}
		}
		if observerErr != nil {
			return observerErr
		}
		select {
		case <-leaseContext.Done():
			if errors.Is(leaseContext.Err(), context.DeadlineExceeded) {
				return nil
			}
			return leaseContext.Err()
		case <-ticker.C:
		}
	}
}

func parseOpsViewerStreamArguments(arguments []string) (string, time.Duration, error) {
	if len(arguments) != 4 || arguments[0] != "--run-id" || arguments[2] != "--ttl" {
		return "", 0, errors.New(
			"ops-viewer-stream requires --run-id UUID --ttl DURATION",
		)
	}
	if !opsViewerRunIDPattern.MatchString(arguments[1]) {
		return "", 0, errors.New("Ops Viewer Run ID is invalid")
	}
	ttl, err := time.ParseDuration(arguments[3])
	if err != nil || ttl < browserprotocol.MinOpsObserverTTL ||
		ttl > browserprotocol.MaxOpsObserverTTL {
		return "", 0, errors.New("Ops Viewer TTL must be between 1m and 30m")
	}
	return arguments[1], ttl, nil
}

// removeDisabledObservationArtifacts mirrors the Ops Viewer cleanup: a leftover
// socket or credential would let a Worker keep declaring the observation feature
// after the flag was turned off, so the disabled state has to be enforced on
// disk rather than only in configuration.
func removeDisabledObservationArtifacts() error {
	for _, artifact := range []struct {
		path       string
		credential bool
	}{
		{
			path: value("OPENLINKER_BROWSER_OBSERVER_SOCKET", defaultObserverSocket),
		},
		{
			path:       value("OPENLINKER_BROWSER_OBSERVER_CREDENTIAL_FILE", defaultObserverCredential),
			credential: true,
		},
	} {
		path := filepath.Clean(artifact.path)
		if !filepath.IsAbs(path) {
			return errors.New("Browser observation artifact path must be absolute")
		}
		info, err := os.Lstat(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("Browser observation disabled artifact is invalid")
		}
		stat, owned := info.Sys().(*syscall.Stat_t)
		if !owned || int(stat.Uid) != os.Geteuid() || info.Mode().Perm()&0o077 != 0 {
			return errors.New("Browser observation disabled artifact is not private")
		}
		if (artifact.credential && !info.Mode().IsRegular()) ||
			(!artifact.credential && info.Mode()&os.ModeSocket == 0) {
			return errors.New("Browser observation disabled artifact type is invalid")
		}
		if err := os.Remove(path); err != nil {
			return errors.New("remove disabled Browser observation artifact")
		}
	}
	return nil
}

func removeDisabledOpsObserverArtifacts() error {
	for _, artifact := range []struct {
		path       string
		credential bool
	}{
		{
			path: value("OPENLINKER_BROWSER_OPS_SOCKET", defaultOpsObserverSocket),
		},
		{
			path:       value("OPENLINKER_BROWSER_OPS_CREDENTIAL_FILE", defaultOpsCredential),
			credential: true,
		},
	} {
		path := filepath.Clean(artifact.path)
		if !filepath.IsAbs(path) {
			return errors.New("Ops Viewer artifact path must be absolute")
		}
		info, err := os.Lstat(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("Ops Viewer disabled artifact is invalid")
		}
		stat, owned := info.Sys().(*syscall.Stat_t)
		if !owned || int(stat.Uid) != os.Geteuid() || info.Mode().Perm()&0o077 != 0 {
			return errors.New("Ops Viewer disabled artifact is not private")
		}
		if (artifact.credential && !info.Mode().IsRegular()) ||
			(!artifact.credential && info.Mode()&os.ModeSocket == 0) {
			return errors.New("Ops Viewer disabled artifact type is invalid")
		}
		if err := os.Remove(path); err != nil {
			return errors.New("remove disabled Ops Viewer artifact")
		}
	}
	return nil
}

func browserProfileEnvironment() (browserruntime.ProfileEnvironment, error) {
	version := value("OPENLINKER_BROWSER_VERSION", defaultBrowserVersion)
	majorText, _, found := strings.Cut(version, ".")
	if !found {
		return browserruntime.ProfileEnvironment{}, errors.New("OPENLINKER_BROWSER_VERSION is invalid")
	}
	major, err := strconv.Atoi(majorText)
	if err != nil {
		return browserruntime.ProfileEnvironment{}, errors.New("OPENLINKER_BROWSER_VERSION is invalid")
	}
	generation, err := strconv.ParseUint(
		value("OPENLINKER_BROWSER_PROFILE_GENERATION", "1"),
		10,
		64,
	)
	if err != nil {
		return browserruntime.ProfileEnvironment{}, errors.New("OPENLINKER_BROWSER_PROFILE_GENERATION is invalid")
	}
	environment := browserruntime.ProfileEnvironment{
		ProfileGeneration: generation,
		EgressLabel: value(
			"OPENLINKER_BROWSER_EGRESS_LABEL",
			defaultEgressLabel,
		),
		Evidence: browserprotocol.EnvironmentEvidence{
			BrowserEngine: value(
				"OPENLINKER_BROWSER_ENGINE",
				defaultBrowserEngine,
			),
			BrowserDistribution: value(
				"OPENLINKER_BROWSER_DISTRIBUTION",
				defaultBrowserDistribution,
			),
			BrowserVersion:      version,
			BrowserMajorVersion: major,
			BrowserLocale: value(
				"OPENLINKER_BROWSER_LOCALE",
				defaultBrowserLocale,
			),
			BrowserTimezone: value(
				"OPENLINKER_BROWSER_TIMEZONE",
				defaultBrowserTimezone,
			),
			FontContractVersion: value(
				"OPENLINKER_BROWSER_FONT_CONTRACT_VERSION",
				defaultFontContractVersion,
			),
			FontManifestSHA256: strings.TrimSpace(
				os.Getenv("OPENLINKER_BROWSER_FONT_MANIFEST_SHA256"),
			),
		},
	}
	if err := environment.Validate(); err != nil {
		return browserruntime.ProfileEnvironment{}, err
	}
	return environment, nil
}

func value(name, fallback string) string {
	if configured := strings.TrimSpace(os.Getenv(name)); configured != "" {
		return configured
	}
	return fallback
}

func booleanValue(name string, fallback bool) (bool, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s must be true or false", name)
	}
	return value, nil
}

func readCredentialFile(rawPath string) (string, error) {
	path := strings.TrimSpace(rawPath)
	if path == "" {
		return "", errors.New("OPENLINKER_BROWSER_CHANNEL_CREDENTIAL_FILE is required")
	}
	path = filepath.Clean(path)
	if !filepath.IsAbs(path) {
		return "", errors.New("Browser channel credential path must be absolute")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", errors.New("Browser channel credential must be a regular non-symlink file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return "", errors.New("Browser channel credential file must have owner-only permissions")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() {
		return "", errors.New("Browser channel credential file must be owned by the current user")
	}
	if info.Size() < 32 || info.Size() > maxCredentialBytes {
		return "", errors.New("Browser channel credential length is invalid")
	}
	file, err := os.Open(path) // #nosec G304 -- the operator-selected path is validated above.
	if err != nil {
		return "", errors.New("open Browser channel credential file")
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maxCredentialBytes+1))
	if err != nil || len(raw) > maxCredentialBytes {
		return "", errors.New("read Browser channel credential file")
	}
	value := strings.TrimSuffix(string(raw), "\n")
	value = strings.TrimSuffix(value, "\r")
	if len(value) < 32 || len(value) > 512 || strings.ContainsAny(value, "\r\n\t ") {
		return "", errors.New("Browser channel credential value is invalid")
	}
	return value, nil
}

func loadOrCreateCredentialFile(rawPath string) (string, error) {
	path := strings.TrimSpace(rawPath)
	credential, err := readCredentialFile(path)
	if err == nil {
		return credential, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	parent := filepath.Dir(filepath.Clean(path))
	info, statErr := os.Lstat(parent)
	if statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 ||
		info.Mode().Perm()&0o077 != 0 {
		return "", errors.New("Browser channel credential directory is invalid")
	}
	stat, owned := info.Sys().(*syscall.Stat_t)
	if !owned || int(stat.Uid) != os.Geteuid() {
		return "", errors.New("Browser channel credential directory is not owned by the current user")
	}
	var secret [32]byte
	if _, err := io.ReadFull(rand.Reader, secret[:]); err != nil {
		return "", errors.New("generate Browser channel credential")
	}
	value := hex.EncodeToString(secret[:])
	clear(secret[:])
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) // #nosec G304 -- fixed validated private control path.
	if errors.Is(err, fs.ErrExist) {
		return readCredentialFile(path)
	}
	if err != nil {
		return "", errors.New("create Browser channel credential")
	}
	_, writeErr := file.WriteString(value + "\n")
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		_ = os.Remove(path)
		return "", errors.Join(writeErr, syncErr, closeErr)
	}
	directory, openErr := os.Open(parent) // #nosec G304 -- parent is the validated credential directory.
	if openErr != nil {
		return "", openErr
	}
	directorySyncErr := directory.Sync()
	directoryCloseErr := directory.Close()
	if directorySyncErr != nil || directoryCloseErr != nil {
		return "", errors.Join(directorySyncErr, directoryCloseErr)
	}
	return value, nil
}
