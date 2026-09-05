//go:build !windows

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image"
	_ "image/jpeg"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserclient"
	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserplugin"
	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserprotocol"
)

const (
	defaultControlRoot = "/browser-control"
	waitTimeout        = 45 * time.Second
	actionTimeout      = 55 * time.Second
	latencySampleCount = 40
)

type result struct {
	Status             string                   `json:"status"`
	Mode               string                   `json:"mode"`
	Origin             string                   `json:"origin,omitempty"`
	Title              string                   `json:"title,omitempty"`
	ScreenshotSHA256   string                   `json:"screenshot_sha256,omitempty"`
	ContinuationOrigin string                   `json:"continuation_origin,omitempty"`
	ActionLatency      *actionLatencyComparison `json:"action_latency,omitempty"`
	BackendSelected    string                   `json:"browser_backend_selected,omitempty"`
	ExtensionID        string                   `json:"browser_extension_id,omitempty"`
	ExtensionVersion   string                   `json:"browser_extension_version,omitempty"`
	ProfileGeneration  uint64                   `json:"browser_profile_generation,omitempty"`
	SessionRecovered   *bool                    `json:"browser_session_recovered,omitempty"`
}

type runtimeExpectation struct {
	BrowserEngine       string
	BrowserDistribution string
	BackendMode         string
	ExtensionID         string
	ExtensionVersion    string
	ProfileGeneration   uint64
}

type actionLatencyComparison struct {
	Action     string       `json:"action"`
	Restricted latencyStats `json:"restricted"`
	Full       latencyStats `json:"full"`
}

type latencyStats struct {
	Samples int     `json:"samples"`
	P50MS   float64 `json:"p50_ms"`
	P95MS   float64 `json:"p95_ms"`
	P99MS   float64 `json:"p99_ms"`
}

func main() {
	mode := flag.String(
		"mode",
		"full",
		"acceptance mode: full, mcp-evidence, or gateway-down",
	)
	publicURL := flag.String("public-url", "", "public HTTPS acceptance fixture URL")
	collectorURL := flag.String(
		"collector-url",
		"",
		"second public HTTPS origin used to prove mutation isolation",
	)
	primeURL := flag.String(
		"prime-url",
		"",
		"optional public HTTPS URL used to establish fixture routing state",
	)
	rebindURL := flag.String(
		"rebind-url",
		"http://make-1.1.1.1-rebindfor2m-127.0.0.1-rr-set-1-ttl.1u.ms/",
		"controlled DNS rebinding URL; empty disables this probe",
	)
	expectedBrowserVersion := flag.String(
		"expected-browser-version",
		"",
		"locked Browser version expected from preflight",
	)
	expectedFontSHA256 := flag.String(
		"expected-font-sha256",
		"",
		"locked font manifest expected from preflight",
	)
	expectedBrowserEngine := flag.String(
		"expected-browser-engine",
		"chromium",
		"locked Browser engine expected from preflight",
	)
	expectedBrowserDistribution := flag.String(
		"expected-browser-distribution",
		"playwright_chromium",
		"locked Browser distribution expected from preflight",
	)
	backendMode := flag.String("backend-mode", "", "strict Browser backend mode")
	expectedExtensionID := flag.String("expected-extension-id", "", "locked extension ID")
	expectedExtensionVersion := flag.String("expected-extension-version", "", "locked extension version")
	expectedProfileGeneration := flag.Uint64("expected-profile-generation", 0, "locked Profile generation")
	controlRoot := flag.String("control-root", defaultControlRoot, "Browser control volume")
	flag.Parse()

	output, err := run(
		strings.TrimSpace(*mode),
		strings.TrimSpace(*publicURL),
		strings.TrimSpace(*collectorURL),
		strings.TrimSpace(*primeURL),
		strings.TrimSpace(*rebindURL),
		strings.TrimSpace(*expectedBrowserVersion),
		strings.TrimSpace(*expectedFontSHA256),
		filepath.Clean(strings.TrimSpace(*controlRoot)),
		runtimeExpectation{
			BrowserEngine:       strings.TrimSpace(*expectedBrowserEngine),
			BrowserDistribution: strings.TrimSpace(*expectedBrowserDistribution),
			BackendMode:         strings.TrimSpace(*backendMode),
			ExtensionID:         strings.TrimSpace(*expectedExtensionID),
			ExtensionVersion:    strings.TrimSpace(*expectedExtensionVersion),
			ProfileGeneration:   *expectedProfileGeneration,
		},
	)
	if err != nil {
		fmt.Fprintln(os.Stderr, "browser image acceptance:", err)
		os.Exit(1)
	}
	raw, err := json.Marshal(output)
	if err != nil {
		fmt.Fprintln(os.Stderr, "browser image acceptance: encode result")
		os.Exit(1)
	}
	fmt.Println(string(raw))
}

func run(
	mode,
	publicURL,
	collectorURL,
	primeURL,
	rebindURL,
	expectedBrowserVersion,
	expectedFontSHA256,
	controlRoot string,
	expected runtimeExpectation,
) (result, error) {
	if mode != "full" && mode != "mcp-evidence" && mode != "gateway-down" {
		return result{}, errors.New("unsupported acceptance mode")
	}
	if mode != "mcp-evidence" {
		if failure := (browserprotocol.Action{
			Kind: browserprotocol.ActionNavigate,
			URL:  publicURL,
		}).Validate(); failure != nil {
			return result{}, errors.New("public fixture URL is invalid")
		}
	}
	if mode == "full" {
		publicFixtureOrigin, err := publicOrigin(publicURL)
		if err != nil {
			return result{}, errors.New("public fixture URL is invalid")
		}
		collectorOrigin, err := publicOrigin(collectorURL)
		if err != nil {
			return result{}, errors.New("collector fixture URL is invalid")
		}
		if collectorOrigin == publicFixtureOrigin {
			return result{}, errors.New("collector fixture must use a distinct public origin")
		}
	}
	if primeURL != "" {
		if failure := (browserprotocol.Action{
			Kind: browserprotocol.ActionNavigate,
			URL:  primeURL,
		}).Validate(); failure != nil {
			return result{}, errors.New("public fixture prime URL is invalid")
		}
	}
	if !filepath.IsAbs(controlRoot) || filepath.Clean(controlRoot) != controlRoot {
		return result{}, errors.New("control root must be an absolute clean path")
	}
	socketPath := filepath.Join(controlRoot, "openlinker.browser.sock")
	credentialPath := filepath.Join(controlRoot, "channel-credential")
	leasePath := filepath.Join(controlRoot, "leases", "active-lease.json")
	if err := waitForPrivateFile(credentialPath, waitTimeout); err != nil {
		return result{}, err
	}
	if err := waitForSocket(socketPath, waitTimeout); err != nil {
		return result{}, err
	}

	if mode == "gateway-down" {
		identity := identityFor(90, 90, 90, 1, 1)
		client, err := activateClient(socketPath, credentialPath, leasePath, identity)
		if err != nil {
			return result{}, err
		}
		_, failure := execute(client, browserprotocol.Action{
			Kind:        browserprotocol.ActionNavigate,
			Observation: browserprotocol.ObservationSemantic,
			URL:         publicURL,
		})
		if failure == nil ||
			failure.Code != browserprotocol.ErrorEgressUnavailable ||
			!failure.Recoverable {
			return result{}, fmt.Errorf(
				"Gateway-down navigation did not fail recoverably: %v",
				failure,
			)
		}
		return result{Status: "passed", Mode: mode}, nil
	}

	if mode == "mcp-evidence" {
		identity := identityFor(91, 91, 91, 1, 1)
		client, err := activateClient(
			socketPath,
			credentialPath,
			leasePath,
			identity,
		)
		if err != nil {
			return result{}, err
		}
		preflight, failure := execute(client, browserprotocol.Action{
			Kind:        browserprotocol.ActionPreflight,
			Observation: browserprotocol.ObservationSemantic,
			BackendMode: expected.BackendMode,
		})
		if failure != nil {
			return result{}, fmt.Errorf("MCP evidence preflight failed: %w", failure)
		}
		if err := validateEnvironmentEvidence(
			preflight.Environment,
			expectedBrowserVersion,
			expectedFontSHA256,
			expected,
		); err != nil {
			return result{}, err
		}
		if err := validateBackendSelection(preflight.BackendSelection, expected, false); err != nil {
			return result{}, err
		}
		if err := validateMCPEvidenceRecovery(
			client,
			*preflight.Environment,
			identity,
		); err != nil {
			return result{}, err
		}
		if _, failure := execute(client, browserprotocol.Action{
			Kind: browserprotocol.ActionClose,
		}); failure != nil {
			return result{}, fmt.Errorf("MCP evidence attachment close failed: %w", failure)
		}
		return result{Status: "passed", Mode: mode}, nil
	}

	firstIdentity := identityFor(1, 1, 1, 1, 1)
	first, err := activateClient(socketPath, credentialPath, leasePath, firstIdentity)
	if err != nil {
		return result{}, err
	}
	preflight, failure := execute(first, browserprotocol.Action{
		Kind:        browserprotocol.ActionPreflight,
		Observation: browserprotocol.ObservationSemantic,
		BackendMode: expected.BackendMode,
	})
	if failure != nil {
		return result{}, fmt.Errorf("preflight failed: %w", failure)
	}
	if err := validateEnvironmentEvidence(
		preflight.Environment,
		expectedBrowserVersion,
		expectedFontSHA256,
		expected,
	); err != nil {
		return result{}, err
	}
	if err := validateBackendSelection(preflight.BackendSelection, expected, false); err != nil {
		return result{}, err
	}
	if primeURL != "" {
		if _, failure := execute(first, browserprotocol.Action{
			Kind:        browserprotocol.ActionNavigate,
			Observation: browserprotocol.ObservationSemantic,
			URL:         primeURL,
		}); failure != nil {
			return result{}, fmt.Errorf("public fixture prime navigation failed: %w", failure)
		}
	}
	observation, failure := execute(first, browserprotocol.Action{
		Kind:        browserprotocol.ActionNavigate,
		Observation: browserprotocol.ObservationBoth,
		URL:         publicURL,
	})
	if failure != nil {
		return result{}, fmt.Errorf("public navigation failed: %w", failure)
	}
	if observation.Screenshot == nil || len(observation.Screenshot.Data) == 0 {
		return result{}, errors.New("public navigation returned no screenshot")
	}
	if observation.Viewport == nil ||
		observation.Viewport.Width != browserprotocol.BrowserViewportWidth ||
		observation.Viewport.Height != browserprotocol.BrowserViewportHeight ||
		observation.NavigationGeneration == 0 ||
		observation.Screenshot.Width != browserprotocol.BrowserViewportWidth ||
		observation.Screenshot.Height != browserprotocol.BrowserViewportHeight {
		return result{}, fmt.Errorf(
			"public navigation dimensions are invalid: %#v",
			observation,
		)
	}
	decodedScreenshot, _, err := image.DecodeConfig(
		bytes.NewReader(observation.Screenshot.Data),
	)
	if err != nil ||
		decodedScreenshot.Width != observation.Screenshot.Width ||
		decodedScreenshot.Height != observation.Screenshot.Height {
		return result{}, fmt.Errorf(
			"public screenshot encoded dimensions are invalid: decoded=%dx%d reported=%dx%d error=%v",
			decodedScreenshot.Width,
			decodedScreenshot.Height,
			observation.Screenshot.Width,
			observation.Screenshot.Height,
			err,
		)
	}
	expectedOrigin, err := publicOrigin(publicURL)
	if err != nil || observation.Origin != expectedOrigin {
		return result{}, fmt.Errorf(
			"public navigation origin mismatch: got %q want %q",
			observation.Origin,
			expectedOrigin,
		)
	}
	if observation.Title != "OpenLinker Browser Acceptance" {
		return result{}, fmt.Errorf("public fixture title mismatch: %q", observation.Title)
	}
	screenshotDigest := sha256.Sum256(observation.Screenshot.Data)

	waitMS := 2500
	observation, failure = execute(first, browserprotocol.Action{
		Kind:        browserprotocol.ActionWait,
		Observation: browserprotocol.ObservationSemantic,
		DurationMS:  &waitMS,
	})
	if failure != nil {
		return result{}, fmt.Errorf("fixture settle failed: %w", failure)
	}
	semantic, err := json.Marshal([]json.RawMessage{
		observation.AXTree,
		observation.DOMDiff,
	})
	if err != nil {
		return result{}, errors.New("encode fixture semantics")
	}
	for _, marker := range []string{
		"post=blocked",
		"websocket=blocked",
		"service_worker=blocked",
		"webrtc_probe=complete",
		"webtransport_probe=complete",
	} {
		if !strings.Contains(string(semantic), marker) {
			return result{}, fmt.Errorf(
				"fixture did not prove %s (pending=%t allowed=%t)",
				marker,
				strings.Contains(
					string(semantic),
					strings.Replace(marker, "blocked", "pending", 1),
				),
				strings.Contains(
					string(semantic),
					strings.Replace(marker, "blocked", "allowed", 1),
				),
			)
		}
	}

	clickX, clickY := 240, 140
	observation, failure = execute(first, browserprotocol.Action{
		Kind:        browserprotocol.ActionClick,
		Observation: browserprotocol.ObservationBoth,
		X:           &clickX,
		Y:           &clickY,
	})
	if failure != nil {
		return result{}, fmt.Errorf(
			"safe search focus failed: code=%s recoverable=%t category=%s state=%s generation=%d",
			failure.Code,
			failure.Recoverable,
			failure.TargetCategory,
			failure.PageStateID,
			failure.NavigationGeneration,
		)
	}
	if observation.ClickEffect != browserprotocol.ClickEffectFocused ||
		observation.TargetCategory != browserprotocol.TargetCategoryTextInput {
		return result{}, fmt.Errorf(
			"safe search click effect = %q/%q",
			observation.ClickEffect,
			observation.TargetCategory,
		)
	}
	observation, failure = execute(first, browserprotocol.Action{
		Kind:        browserprotocol.ActionTypeNonSecret,
		Observation: browserprotocol.ObservationSemantic,
		Text:        "openlinker-browser",
	})
	if failure != nil {
		return result{}, fmt.Errorf("safe search typing failed: %w", failure)
	}
	interactionState, err := json.Marshal([]json.RawMessage{
		observation.AXTree,
		observation.DOMDiff,
	})
	if err != nil {
		return result{}, errors.New("encode search interaction state")
	}
	for _, marker := range []string{
		"pointerdown=0",
		"mousedown=0",
		"click=0",
	} {
		if !strings.Contains(string(interactionState), marker) {
			return result{}, fmt.Errorf("focus-only input dispatched %s", marker)
		}
	}
	observation, failure = execute(first, browserprotocol.Action{
		Kind:        browserprotocol.ActionKeypress,
		Observation: browserprotocol.ObservationSemantic,
		Key:         "Enter",
	})
	if failure != nil {
		return result{}, fmt.Errorf("safe GET search submit failed: %w", failure)
	}
	searchState, err := json.Marshal([]json.RawMessage{
		observation.AXTree,
		observation.DOMDiff,
	})
	if err != nil ||
		observation.Title != "OpenLinker Search Result" ||
		!strings.Contains(string(searchState), "query=openlinker-browser") {
		return result{}, fmt.Errorf(
			"safe GET search result is invalid: title=%q state=%s",
			observation.Title,
			searchState,
		)
	}
	if _, failure := execute(first, browserprotocol.Action{
		Kind:        browserprotocol.ActionNavigate,
		Observation: browserprotocol.ObservationSemantic,
		URL:         publicURL,
	}); failure != nil {
		return result{}, fmt.Errorf("return from search result failed: %w", failure)
	}

	for _, target := range []string{
		"http://127.0.0.1/",
		"http://[::1]/",
		"http://10.0.0.1/",
		"http://172.16.0.1/",
		"http://192.168.0.1/",
		"http://[fc00::1]/",
		"http://[fe80::1]/",
		"http://2130706433/",
		"http://169.254.169.254/",
	} {
		_, failure := first.Execute(context.Background(), browserprotocol.Action{
			Kind: browserprotocol.ActionNavigate,
			URL:  target,
		})
		if failure == nil || failure.Code != browserprotocol.ErrorProtocolInvalid {
			return result{}, fmt.Errorf("private literal was not rejected: %s", target)
		}
	}
	for _, target := range []string{
		"http://127.0.0.1.nip.io/",
		"http://100.64.0.1.nip.io/",
		"http://169.254.169.254.nip.io/",
	} {
		if err := expectTargetBlocked(first, target); err != nil {
			return result{}, err
		}
	}
	if err := expectTargetBlocked(
		first,
		strings.TrimRight(publicURL, "/")+"/redirect-private",
	); err != nil {
		return result{}, err
	}
	if rebindURL != "" {
		if err := expectRebindingBlocked(first, rebindURL); err != nil {
			return result{}, err
		}
	}

	if _, failure := execute(first, browserprotocol.Action{
		Kind:        browserprotocol.ActionNavigate,
		Observation: browserprotocol.ObservationSemantic,
		URL:         publicURL,
	}); failure != nil {
		return result{}, fmt.Errorf("return to fixture failed: %w", failure)
	}
	if _, failure := execute(first, browserprotocol.Action{
		Kind:        browserprotocol.ActionCheckpoint,
		Observation: browserprotocol.ObservationNone,
	}); failure != nil {
		return result{}, fmt.Errorf("checkpoint failed: %w", failure)
	}
	if _, failure := execute(first, browserprotocol.Action{
		Kind:        browserprotocol.ActionClose,
		Observation: browserprotocol.ObservationNone,
	}); failure != nil {
		return result{}, fmt.Errorf("close failed: %w", failure)
	}
	if _, failure := execute(first, browserprotocol.Action{
		Kind:        browserprotocol.ActionScreenshot,
		Observation: browserprotocol.ObservationSemantic,
	}); failure == nil ||
		(failure.Code != browserprotocol.ErrorIdentityMismatch &&
			failure.Code != browserprotocol.ErrorStaleControlEpoch) {
		return result{}, fmt.Errorf("closed attachment was reusable: %v", failure)
	}

	resumedIdentity := identityFor(2, 1, 2, 1, 2)
	resumed, err := activateClient(
		socketPath,
		credentialPath,
		leasePath,
		resumedIdentity,
	)
	if err != nil {
		return result{}, err
	}
	resumedPreflight, failure := execute(resumed, browserprotocol.Action{
		Kind:        browserprotocol.ActionPreflight,
		Observation: browserprotocol.ObservationSemantic,
	})
	if failure != nil {
		return result{}, fmt.Errorf("same-Session continuation preflight failed: %w", failure)
	}
	if err := validateBackendSelection(resumedPreflight.BackendSelection, expected, true); err != nil {
		return result{}, err
	}
	resumedObservation, failure := execute(resumed, browserprotocol.Action{
		Kind:        browserprotocol.ActionScreenshot,
		Observation: browserprotocol.ObservationSemantic,
	})
	if failure != nil {
		return result{}, fmt.Errorf("same-Session continuation failed: %w", failure)
	}
	if resumedObservation.Origin != expectedOrigin ||
		resumedObservation.Title != "OpenLinker Browser Acceptance" {
		return result{}, fmt.Errorf(
			"same-Session continuation restored the wrong page: %q %q",
			resumedObservation.Origin,
			resumedObservation.Title,
		)
	}
	if _, failure := execute(resumed, browserprotocol.Action{
		Kind:        browserprotocol.ActionClose,
		Observation: browserprotocol.ObservationNone,
	}); failure != nil {
		return result{}, fmt.Errorf("resumed attachment close failed: %w", failure)
	}

	isolatedIdentity := identityFor(3, 2, 3, 1, 3)
	isolated, err := activateClient(
		socketPath,
		credentialPath,
		leasePath,
		isolatedIdentity,
	)
	if err != nil {
		return result{}, err
	}
	if expected.BackendMode != "" {
		isolatedPreflight, failure := execute(isolated, browserprotocol.Action{
			Kind:        browserprotocol.ActionPreflight,
			Observation: browserprotocol.ObservationSemantic,
		})
		if failure != nil {
			return result{}, fmt.Errorf(
				"different-Session Native Host preflight failed: %w",
				failure,
			)
		}
		if err := validateBackendSelection(
			isolatedPreflight.BackendSelection,
			expected,
			true,
		); err != nil {
			return result{}, err
		}
	}
	isolatedObservation, failure := execute(isolated, browserprotocol.Action{
		Kind:        browserprotocol.ActionScreenshot,
		Observation: browserprotocol.ObservationSemantic,
	})
	if failure != nil {
		return result{}, fmt.Errorf("different-Session blank start failed: %w", failure)
	}
	if isolatedObservation.Origin != "" || isolatedObservation.Title != "" {
		return result{}, fmt.Errorf(
			"different Session inherited page state: %q %q",
			isolatedObservation.Origin,
			isolatedObservation.Title,
		)
	}
	if _, failure := execute(isolated, browserprotocol.Action{
		Kind:        browserprotocol.ActionClose,
		Observation: browserprotocol.ObservationNone,
	}); failure != nil {
		return result{}, fmt.Errorf("isolated attachment close failed: %w", failure)
	}
	if err := runReliabilityFixtures(
		socketPath,
		credentialPath,
		leasePath,
		publicURL,
	); err != nil {
		return result{}, err
	}
	if err := runFullInteractionPolicyFixtures(
		socketPath,
		credentialPath,
		leasePath,
		publicURL,
		collectorURL,
	); err != nil {
		return result{}, err
	}
	if err := runPolicyFencingFixtures(
		socketPath,
		credentialPath,
		leasePath,
		publicURL,
		collectorURL,
	); err != nil {
		return result{}, err
	}
	actionLatency, err := runActionLatencyComparison(
		socketPath,
		credentialPath,
		leasePath,
		publicURL,
	)
	if err != nil {
		return result{}, err
	}

	output := result{
		Status:             "passed",
		Mode:               mode,
		Origin:             observation.Origin,
		Title:              observation.Title,
		ScreenshotSHA256:   hex.EncodeToString(screenshotDigest[:]),
		ContinuationOrigin: resumedObservation.Origin,
		ActionLatency:      &actionLatency,
	}
	if selection := preflight.BackendSelection; selection != nil {
		output.BackendSelected = selection.SelectedBackend
		output.ExtensionID = selection.ExtensionID
		output.ExtensionVersion = selection.ExtensionVersion
		output.ProfileGeneration = selection.ProfileGeneration
		recovered := resumedPreflight.BackendSelection != nil &&
			resumedPreflight.BackendSelection.SessionRecovered
		output.SessionRecovered = &recovered
	}
	return output, nil
}

func runPolicyFencingFixtures(
	socketPath,
	credentialPath,
	leasePath,
	publicURL,
	collectorURL string,
) error {
	expectedOrigin, err := publicOrigin(publicURL)
	if err != nil {
		return err
	}
	collectorOrigin, err := publicOrigin(collectorURL)
	if err != nil {
		return err
	}

	policyIdentity, err := fullIdentityFor(
		120,
		120,
		120,
		1,
		120,
		[]string{expectedOrigin},
	)
	if err != nil {
		return err
	}
	policyClient, err := activateClient(
		socketPath,
		credentialPath,
		leasePath,
		policyIdentity,
	)
	if err != nil {
		return err
	}
	if err := navigateFixture(
		policyClient,
		strings.TrimRight(publicURL, "/")+"/plain",
		"policy-generation fencing fixture",
	); err != nil {
		return err
	}
	replacementPolicy := policyIdentity
	replacementPolicy.BrowserInteractionPolicyGeneration++
	if err := writeLease(leasePath, browserclient.Lease{
		ContractID: browserclient.LeaseContractID,
		ExpiresAt:  time.Now().UTC().Add(10 * time.Minute),
		Identity:   replacementPolicy,
	}); err != nil {
		return err
	}
	if _, failure := execute(policyClient, browserprotocol.Action{
		Kind:        browserprotocol.ActionScreenshot,
		Observation: browserprotocol.ObservationSemantic,
	}); failure == nil || failure.Code != browserprotocol.ErrorIdentityMismatch {
		return fmt.Errorf("stale policy generation was not fenced: %v", failure)
	}

	originIdentity, err := fullIdentityFor(
		130,
		130,
		130,
		1,
		130,
		[]string{expectedOrigin},
	)
	if err != nil {
		return err
	}
	originClient, err := activateClient(
		socketPath,
		credentialPath,
		leasePath,
		originIdentity,
	)
	if err != nil {
		return err
	}
	if _, failure := execute(originClient, browserprotocol.Action{
		Kind:        browserprotocol.ActionPreflight,
		Observation: browserprotocol.ObservationSemantic,
	}); failure != nil {
		return fmt.Errorf("origin-digest fencing preflight failed: %w", failure)
	}
	replacementOrigins, err := fullIdentityFor(
		130,
		130,
		130,
		1,
		130,
		[]string{expectedOrigin, collectorOrigin},
	)
	if err != nil {
		return err
	}
	if err := writeLease(leasePath, browserclient.Lease{
		ContractID: browserclient.LeaseContractID,
		ExpiresAt:  time.Now().UTC().Add(10 * time.Minute),
		Identity:   replacementOrigins,
	}); err != nil {
		return err
	}
	if _, failure := execute(originClient, browserprotocol.Action{
		Kind:        browserprotocol.ActionScreenshot,
		Observation: browserprotocol.ObservationSemantic,
	}); failure == nil || failure.Code != browserprotocol.ErrorIdentityMismatch {
		return fmt.Errorf("stale mutation-origin digest was not fenced: %v", failure)
	}
	return nil
}

func runActionLatencyComparison(
	socketPath,
	credentialPath,
	leasePath,
	publicURL string,
) (actionLatencyComparison, error) {
	expectedOrigin, err := publicOrigin(publicURL)
	if err != nil {
		return actionLatencyComparison{}, fmt.Errorf(
			"action latency fixture origin: %w",
			err,
		)
	}
	restrictedIdentity := identityFor(140, 140, 140, 1, 140)
	fullIdentity, err := fullIdentityFor(
		150,
		150,
		150,
		1,
		150,
		[]string{expectedOrigin},
	)
	if err != nil {
		return actionLatencyComparison{}, err
	}

	measure := func(
		label string,
		identity browserprotocol.Identity,
	) (latencyStats, error) {
		client, err := activateClient(
			socketPath,
			credentialPath,
			leasePath,
			identity,
		)
		if err != nil {
			return latencyStats{}, err
		}
		if err := navigateFixture(
			client,
			strings.TrimRight(publicURL, "/")+"/plain",
			label+" action-latency fixture",
		); err != nil {
			return latencyStats{}, err
		}

		samples := make([]time.Duration, 0, latencySampleCount)
		for sample := 0; sample < latencySampleCount; sample++ {
			started := time.Now()
			_, failure := execute(client, browserprotocol.Action{
				Kind:        browserprotocol.ActionScreenshot,
				Observation: browserprotocol.ObservationSemantic,
			})
			elapsed := time.Since(started)
			if failure != nil {
				return latencyStats{}, fmt.Errorf(
					"%s action-latency sample %d failed: %w",
					label,
					sample+1,
					failure,
				)
			}
			samples = append(samples, elapsed)
		}
		if _, failure := execute(client, browserprotocol.Action{
			Kind:        browserprotocol.ActionClose,
			Observation: browserprotocol.ObservationNone,
		}); failure != nil {
			return latencyStats{}, fmt.Errorf(
				"%s action-latency attachment close failed: %w",
				label,
				failure,
			)
		}
		return summarizeLatency(samples), nil
	}

	restricted, err := measure("restricted", restrictedIdentity)
	if err != nil {
		return actionLatencyComparison{}, err
	}
	full, err := measure("full", fullIdentity)
	if err != nil {
		return actionLatencyComparison{}, err
	}
	return actionLatencyComparison{
		Action:     "read_only_semantic_observation",
		Restricted: restricted,
		Full:       full,
	}, nil
}

func summarizeLatency(samples []time.Duration) latencyStats {
	if len(samples) == 0 {
		return latencyStats{}
	}
	ordered := append([]time.Duration(nil), samples...)
	sort.Slice(ordered, func(left, right int) bool {
		return ordered[left] < ordered[right]
	})
	percentileMS := func(percentile int) float64 {
		// Nearest-rank percentile: ceil(percentile * N / 100), one-based.
		index := (percentile*len(ordered)+99)/100 - 1
		return float64(ordered[index]) / float64(time.Millisecond)
	}
	return latencyStats{
		Samples: len(ordered),
		P50MS:   percentileMS(50),
		P95MS:   percentileMS(95),
		P99MS:   percentileMS(99),
	}
}

func runFullInteractionPolicyFixtures(
	socketPath,
	credentialPath,
	leasePath,
	publicURL,
	collectorURL string,
) error {
	base := strings.TrimRight(publicURL, "/")
	collectorBase := strings.TrimRight(collectorURL, "/")
	expectedOrigin, err := publicOrigin(publicURL)
	if err != nil {
		return fmt.Errorf("full interaction fixture origin: %w", err)
	}
	collectorOrigin, err := publicOrigin(collectorURL)
	if err != nil {
		return fmt.Errorf("full interaction collector origin: %w", err)
	}

	restrictedIdentity := identityFor(50, 50, 50, 1, 50)
	restricted, err := activateClient(
		socketPath,
		credentialPath,
		leasePath,
		restrictedIdentity,
	)
	if err != nil {
		return err
	}
	restrictedObservation, failure := execute(restricted, browserprotocol.Action{
		Kind:        browserprotocol.ActionNavigate,
		Observation: browserprotocol.ObservationSemantic,
		URL:         base + "/full/toggle",
	})
	if failure != nil || restrictedObservation.Title != "Full Toggle Fixture - unstarred" {
		return fmt.Errorf("restricted full-policy fixture navigation failed: %w", failure)
	}
	buttonX, buttonY := 240, 140
	_, failure = execute(restricted, browserprotocol.Action{
		Kind:        browserprotocol.ActionClick,
		Observation: browserprotocol.ObservationSemantic,
		X:           &buttonX,
		Y:           &buttonY,
	})
	if failure == nil || failure.Code != browserprotocol.ErrorHighImpactActionBlocked {
		return fmt.Errorf("restricted policy activated a button: %v", failure)
	}
	if _, failure = execute(restricted, browserprotocol.Action{
		Kind:        browserprotocol.ActionClose,
		Observation: browserprotocol.ObservationNone,
	}); failure != nil {
		return fmt.Errorf("restricted full-policy fixture close failed: %w", failure)
	}

	blockedIdentity, err := fullIdentityFor(
		60,
		60,
		60,
		1,
		60,
		[]string{"https://not-authorized.example"},
	)
	if err != nil {
		return err
	}
	blocked, err := activateClient(
		socketPath,
		credentialPath,
		leasePath,
		blockedIdentity,
	)
	if err != nil {
		return err
	}
	if _, failure = execute(blocked, browserprotocol.Action{
		Kind:        browserprotocol.ActionNavigate,
		Observation: browserprotocol.ObservationSemantic,
		URL:         base + "/plain",
	}); failure != nil {
		return fmt.Errorf("unallowlisted-origin fixture navigation failed: %w", failure)
	}
	inputX, inputY := 240, 140
	if _, failure = execute(blocked, browserprotocol.Action{
		Kind:        browserprotocol.ActionClick,
		Observation: browserprotocol.ObservationSemantic,
		X:           &inputX,
		Y:           &inputY,
	}); failure != nil {
		return fmt.Errorf("unallowlisted-origin input focus failed: %w", failure)
	}
	_, failure = execute(blocked, browserprotocol.Action{
		Kind:        browserprotocol.ActionTypeNonSecret,
		Observation: browserprotocol.ObservationSemantic,
		Text:        "must-not-dispatch",
	})
	if failure == nil ||
		failure.Code != browserprotocol.ErrorMutationOriginBlocked ||
		failure.Recoverable ||
		failure.RetrySameAction == nil || *failure.RetrySameAction ||
		failure.AttachmentUsable == nil || !*failure.AttachmentUsable ||
		failure.FreshObservationRequired == nil || *failure.FreshObservationRequired ||
		failure.ObservedOrigin != expectedOrigin {
		return fmt.Errorf("unallowlisted mutation was not rejected before dispatch: %v", failure)
	}
	if _, failure = execute(blocked, browserprotocol.Action{
		Kind:        browserprotocol.ActionClose,
		Observation: browserprotocol.ObservationNone,
	}); failure != nil {
		return fmt.Errorf("unallowlisted-origin fixture close failed: %w", failure)
	}

	fullIdentity, err := fullIdentityFor(
		70,
		70,
		70,
		1,
		70,
		[]string{expectedOrigin},
	)
	if err != nil {
		return err
	}
	full, err := activateClient(
		socketPath,
		credentialPath,
		leasePath,
		fullIdentity,
	)
	if err != nil {
		return err
	}
	toggleObservation, failure := execute(full, browserprotocol.Action{
		Kind:        browserprotocol.ActionNavigate,
		Observation: browserprotocol.ObservationSemantic,
		URL:         base + "/full/toggle",
	})
	if failure != nil || toggleObservation.Title != "Full Toggle Fixture - unstarred" {
		return fmt.Errorf("durable toggle initial state failed: title=%q failure=%v", toggleObservation.Title, failure)
	}
	if err := clickFixture(full, 240, 140, "durable toggle"); err != nil {
		return err
	}
	toggleObservation, failure = execute(full, browserprotocol.Action{
		Kind:        browserprotocol.ActionNavigate,
		Observation: browserprotocol.ObservationSemantic,
		URL:         base + "/full/toggle",
	})
	if failure != nil || toggleObservation.Title != "Full Toggle Fixture - starred" {
		return fmt.Errorf("durable toggle reload state failed: title=%q failure=%v", toggleObservation.Title, failure)
	}

	if err := navigateFixture(full, base+"/full/methods", "method matrix"); err != nil {
		return err
	}
	for _, fixture := range []struct {
		method string
		y      int
	}{
		{method: "post", y: 120},
		{method: "put", y: 200},
		{method: "patch", y: 280},
		{method: "delete", y: 360},
	} {
		if err := clickFixtureAndRequireMarkers(
			full,
			240,
			fixture.y,
			fixture.method+" mutation",
			"method_"+fixture.method+"=allowed",
		); err != nil {
			return fmt.Errorf("%s mutation: %w", fixture.method, err)
		}
	}

	if err := navigateFixture(full, base+"/full/controls", "control matrix"); err != nil {
		return err
	}
	if err := clickFixtureAndRequireMarkers(
		full,
		240,
		120,
		"custom control",
		"custom_events=pointerdown,mousedown,pointerup,mouseup,click",
		"custom=activated",
	); err != nil {
		return err
	}
	if err := clickFixtureAndRequireMarkers(
		full,
		180,
		200,
		"checkbox control",
		"checkbox=true",
	); err != nil {
		return err
	}
	if err := clickFixtureAndRequireMarkers(
		full,
		180,
		270,
		"radio control",
		"radio=true",
	); err != nil {
		return err
	}
	if err := clickFixture(full, 240, 340, "select focus"); err != nil {
		return err
	}
	selectObservation, failure := execute(full, browserprotocol.Action{
		Kind:        browserprotocol.ActionSelect,
		Observation: browserprotocol.ObservationSemantic,
		Value:       "second",
	})
	if failure != nil || !observationContains(selectObservation, "select=second") {
		return fmt.Errorf("select control mutation failed: %v", failure)
	}
	if err := clickFixture(full, 240, 410, "Space control click"); err != nil {
		return err
	}
	spaceObservation, failure := execute(full, browserprotocol.Action{
		Kind:        browserprotocol.ActionKeypress,
		Observation: browserprotocol.ObservationSemantic,
		Key:         "Space",
	})
	if failure != nil || !observationContains(spaceObservation, "space_clicks=2") {
		return fmt.Errorf("Space control activation failed: %v", failure)
	}
	if err := clickFixture(full, 240, 490, "form input focus"); err != nil {
		return err
	}
	if _, failure = execute(full, browserprotocol.Action{
		Kind:        browserprotocol.ActionTypeNonSecret,
		Observation: browserprotocol.ObservationSemantic,
		Text:        "fixture",
	}); failure != nil {
		return fmt.Errorf("form input typing failed: %w", failure)
	}
	formObservation, failure := execute(full, browserprotocol.Action{
		Kind:        browserprotocol.ActionKeypress,
		Observation: browserprotocol.ObservationSemantic,
		Key:         "Enter",
	})
	if failure != nil || !observationContains(formObservation, "form=allowed") {
		return fmt.Errorf("Enter form submission failed: %v", failure)
	}

	if err := navigateFixture(full, base+"/full/scroll", "scroll and batch matrix"); err != nil {
		return err
	}
	scrollY, waitMS := 480, 100
	batchObservation, failure := execute(full, browserprotocol.Action{
		Kind:        browserprotocol.ActionBatch,
		Observation: browserprotocol.ObservationSemantic,
		Actions: []browserprotocol.Action{
			{Kind: browserprotocol.ActionScroll, DeltaY: &scrollY},
			{Kind: browserprotocol.ActionWait, DurationMS: &waitMS},
			{Kind: browserprotocol.ActionScreenshot},
		},
	})
	if failure != nil || !observationContains(batchObservation, "scroll_state=moved") {
		return fmt.Errorf("ordered scroll batch failed: %v", failure)
	}
	oneMS := 1
	_, failure = execute(full, browserprotocol.Action{
		Kind:        browserprotocol.ActionBatch,
		Observation: browserprotocol.ObservationSemantic,
		Actions: []browserprotocol.Action{
			{Kind: browserprotocol.ActionWait, DurationMS: &oneMS},
			{Kind: browserprotocol.ActionSelect, Value: "must-not-dispatch"},
		},
	})
	if failure == nil ||
		failure.Code != browserprotocol.ErrorActionRejected ||
		failure.ActionIndex == nil || *failure.ActionIndex != 1 ||
		failure.CompletedActions == nil || *failure.CompletedActions != 1 {
		return fmt.Errorf("failed batch did not report its exact prefix: %v", failure)
	}

	if err := navigateFixture(full, base+"/full/typing", "typing event matrix"); err != nil {
		return err
	}
	if err := clickFixture(full, 240, 120, "ASCII input focus"); err != nil {
		return err
	}
	asciiObservation, failure := execute(full, browserprotocol.Action{
		Kind:        browserprotocol.ActionTypeNonSecret,
		Observation: browserprotocol.ObservationSemantic,
		Text:        "abc",
	})
	asciiEvents := "ascii_events=keydown,keypress,beforeinput,input,keyup,keydown,keypress,beforeinput,input,keyup,keydown,keypress,beforeinput,input,keyup"
	if failure != nil ||
		!observationContains(asciiObservation, "ascii=abc") ||
		!observationContains(asciiObservation, asciiEvents) {
		return fmt.Errorf(
			"ASCII event sequence failed: failure=%v observation=%s",
			failure,
			debugObservation(asciiObservation),
		)
	}
	if err := clickFixture(full, 240, 210, "non-layout input focus"); err != nil {
		return err
	}
	nonLayoutObservation, failure := execute(full, browserprotocol.Action{
		Kind:        browserprotocol.ActionTypeNonSecret,
		Observation: browserprotocol.ObservationSemantic,
		Text:        "中🙂",
	})
	if failure != nil ||
		!observationContains(nonLayoutObservation, "unicode=中🙂") ||
		!observationContains(nonLayoutObservation, "unicode_events=beforeinput,input,beforeinput,input") ||
		observationContains(nonLayoutObservation, "unicode_events=keydown") ||
		observationContains(nonLayoutObservation, "unicode_events=keypress") ||
		observationContains(nonLayoutObservation, "unicode_events=keyup") {
		return fmt.Errorf(
			"non-layout event sequence failed: failure=%v observation=%s",
			failure,
			debugObservation(nonLayoutObservation),
		)
	}
	if err := clickFixture(full, 240, 300, "focus-drift source"); err != nil {
		return err
	}
	_, failure = execute(full, browserprotocol.Action{
		Kind:        browserprotocol.ActionTypeNonSecret,
		Observation: browserprotocol.ObservationSemantic,
		Text:        "AB",
	})
	if !isUnknownMutation(failure, "text_entry_target_changed_after_unit") ||
		failure.AttemptedUnits == nil || *failure.AttemptedUnits != 1 ||
		failure.UndispatchedUnits == nil || *failure.UndispatchedUnits != 1 {
		return fmt.Errorf("per-unit focus invariant returned invalid uncertainty: %v", failure)
	}
	typingState, failure := execute(full, browserprotocol.Action{
		Kind:        browserprotocol.ActionScreenshot,
		Observation: browserprotocol.ObservationSemantic,
	})
	if failure != nil ||
		!observationContains(typingState, "drift_source=A") ||
		observationContains(typingState, "drift_target=B") {
		return fmt.Errorf("per-unit focus invariant leaked remaining text: %v", failure)
	}

	networkURL := base + "/full/network?collector_origin=" + url.QueryEscape(collectorOrigin)
	if err := navigateFixture(full, networkURL, "cross-origin network fixture"); err != nil {
		return err
	}
	_, failure = execute(full, browserprotocol.Action{
		Kind:        browserprotocol.ActionClick,
		Observation: browserprotocol.ObservationSemantic,
		X:           intPointer(240),
		Y:           intPointer(140),
	})
	if !isMutationOriginBlocked(failure, expectedOrigin, true) {
		return fmt.Errorf("cross-origin fetch/WebSocket was not blocked: %v", failure)
	}

	framesURL := base + "/full/frames?collector_origin=" + url.QueryEscape(collectorOrigin)
	if err := navigateFixture(full, framesURL, "child-frame fixture"); err != nil {
		return err
	}
	if err := waitForObservationMarker(full, "collector_frame=loaded", "collector child frame"); err != nil {
		return err
	}
	if err := clickFixture(full, 240, 125, "allowlisted child frame"); err != nil {
		return err
	}
	_, failure = execute(full, browserprotocol.Action{
		Kind:        browserprotocol.ActionClick,
		Observation: browserprotocol.ObservationSemantic,
		X:           intPointer(240),
		Y:           intPointer(315),
	})
	if !isMutationOriginBlocked(failure, collectorOrigin, false) {
		return fmt.Errorf("collector child-frame mutation was not blocked: %v", failure)
	}

	if err := navigateFixture(full, collectorBase+"/full/collector", "collector top-level fixture"); err != nil {
		return err
	}
	_, failure = execute(full, browserprotocol.Action{
		Kind:        browserprotocol.ActionClick,
		Observation: browserprotocol.ObservationSemantic,
		X:           intPointer(240),
		Y:           intPointer(140),
	})
	if !isMutationOriginBlocked(failure, collectorOrigin, false) {
		return fmt.Errorf("collector button mutation was not blocked: %v", failure)
	}
	if err := clickFixture(full, 240, 230, "collector input focus"); err != nil {
		return err
	}
	_, failure = execute(full, browserprotocol.Action{
		Kind:        browserprotocol.ActionTypeNonSecret,
		Observation: browserprotocol.ObservationSemantic,
		Text:        "must-not-dispatch",
	})
	if !isMutationOriginBlocked(failure, collectorOrigin, false) {
		return fmt.Errorf("collector text mutation was not blocked: %v", failure)
	}
	if err := navigateFixture(full, collectorBase+"/full/collector", "collector select reset"); err != nil {
		return err
	}
	_, failure = execute(full, browserprotocol.Action{
		Kind:        browserprotocol.ActionSelect,
		Observation: browserprotocol.ObservationSemantic,
		Value:       "two",
	})
	if !isMutationOriginBlocked(failure, collectorOrigin, false) {
		return fmt.Errorf("collector select mutation was not blocked: %v", failure)
	}

	if err := navigateFixture(full, base+"/full/websocket", "WebSocket mutation fixture"); err != nil {
		return err
	}
	if err := clickFixture(full, 240, 140, "same-origin WebSocket mutation"); err != nil {
		return err
	}
	if err := waitForObservationMarker(
		full,
		"websocket_update=updated",
		"same-origin WebSocket mutation",
	); err != nil {
		return err
	}

	if err := navigateFixture(full, base+"/full/uncertain", "response-loss fixture"); err != nil {
		return err
	}
	_, failure = execute(full, browserprotocol.Action{
		Kind:        browserprotocol.ActionClick,
		Observation: browserprotocol.ObservationSemantic,
		X:           intPointer(240),
		Y:           intPointer(140),
	})
	if !isUnknownMutation(failure, "page_mutation_request_failed") {
		return fmt.Errorf("response-loss mutation did not become outcome unknown: %v", failure)
	}
	uncertainState, terminalFailure := execute(full, browserprotocol.Action{
		Kind:        browserprotocol.ActionScreenshot,
		Observation: browserprotocol.ObservationSemantic,
	})
	if terminalFailure != nil || uncertainState.Title != "Full Uncertain Fixture" {
		return fmt.Errorf("response-loss attachment was not usable: %v", terminalFailure)
	}
	if _, failure = execute(full, browserprotocol.Action{
		Kind:        browserprotocol.ActionClose,
		Observation: browserprotocol.ObservationNone,
	}); failure != nil {
		return fmt.Errorf("full-policy fixture close failed: %w", failure)
	}

	loginIdentity, err := fullIdentityFor(
		80,
		80,
		80,
		1,
		80,
		[]string{expectedOrigin},
	)
	if err != nil {
		return err
	}
	login, err := activateClient(socketPath, credentialPath, leasePath, loginIdentity)
	if err != nil {
		return err
	}
	if err := navigateFixture(login, base+"/full/login", "Profile login fixture"); err != nil {
		return err
	}
	loginState, failure := execute(login, browserprotocol.Action{
		Kind:        browserprotocol.ActionNavigate,
		Observation: browserprotocol.ObservationSemantic,
		URL:         base + "/full/session",
	})
	if failure != nil || loginState.Title != "Full Session Fixture - ready" {
		return fmt.Errorf("Profile login state before checkpoint failed: title=%q failure=%v", loginState.Title, failure)
	}
	if _, failure = execute(login, browserprotocol.Action{
		Kind:        browserprotocol.ActionCheckpoint,
		Observation: browserprotocol.ObservationNone,
	}); failure != nil {
		return fmt.Errorf("Profile login checkpoint failed: %w", failure)
	}
	if _, failure = execute(login, browserprotocol.Action{
		Kind:        browserprotocol.ActionClose,
		Observation: browserprotocol.ObservationNone,
	}); failure != nil {
		return fmt.Errorf("Profile login attachment close failed: %w", failure)
	}
	resumedIdentity, err := fullIdentityFor(
		81,
		81,
		81,
		1,
		81,
		[]string{expectedOrigin},
	)
	if err != nil {
		return err
	}
	resumedIdentity.BrowserSessionID = loginIdentity.BrowserSessionID
	resumed, err := activateClient(socketPath, credentialPath, leasePath, resumedIdentity)
	if err != nil {
		return err
	}
	resumedState, failure := execute(resumed, browserprotocol.Action{
		Kind:        browserprotocol.ActionScreenshot,
		Observation: browserprotocol.ObservationSemantic,
	})
	if failure != nil || resumedState.Title != "Full Session Fixture - ready" {
		return fmt.Errorf(
			"Profile login state did not survive continuation: title=%q failure=%v observation=%s",
			resumedState.Title,
			failure,
			debugObservation(resumedState),
		)
	}
	if _, failure = execute(resumed, browserprotocol.Action{
		Kind:        browserprotocol.ActionClose,
		Observation: browserprotocol.ObservationNone,
	}); failure != nil {
		return fmt.Errorf("resumed Profile attachment close failed: %w", failure)
	}
	return nil
}

func navigateFixture(client *browserclient.Client, target, label string) error {
	if _, failure := execute(client, browserprotocol.Action{
		Kind:        browserprotocol.ActionNavigate,
		Observation: browserprotocol.ObservationSemantic,
		URL:         target,
	}); failure != nil {
		return fmt.Errorf("%s navigation failed: %w", label, failure)
	}
	return nil
}

func clickFixture(client *browserclient.Client, x, y int, label string) error {
	if _, failure := execute(client, browserprotocol.Action{
		Kind:        browserprotocol.ActionClick,
		Observation: browserprotocol.ObservationSemantic,
		X:           &x,
		Y:           &y,
	}); failure != nil {
		return fmt.Errorf("%s click failed: %#v", label, failure)
	}
	return nil
}

func waitForObservationMarker(
	client *browserclient.Client,
	marker,
	label string,
) error {
	waitMS := 1000
	var observation browserprotocol.Observation
	for attempt := 0; attempt < 5; attempt++ {
		var failure *browserprotocol.Failure
		observation, failure = execute(client, browserprotocol.Action{
			Kind:        browserprotocol.ActionWait,
			Observation: browserprotocol.ObservationSemantic,
			DurationMS:  &waitMS,
		})
		if failure != nil {
			return fmt.Errorf("%s observation wait failed: %#v", label, failure)
		}
		if observationContains(observation, marker) {
			return nil
		}
	}
	return fmt.Errorf(
		"%s observation does not contain %q: %s",
		label,
		marker,
		debugObservation(observation),
	)
}

func clickFixtureAndRequireMarkers(
	client *browserclient.Client,
	x,
	y int,
	label string,
	markers ...string,
) error {
	observation, failure := execute(client, browserprotocol.Action{
		Kind:        browserprotocol.ActionClick,
		Observation: browserprotocol.ObservationSemantic,
		X:           &x,
		Y:           &y,
	})
	if failure != nil {
		return fmt.Errorf("%s click failed: %#v", label, failure)
	}
	waitMS := 350
	after, waitFailure := execute(client, browserprotocol.Action{
		Kind:        browserprotocol.ActionWait,
		Observation: browserprotocol.ObservationSemantic,
		DurationMS:  &waitMS,
	})
	if waitFailure != nil {
		return fmt.Errorf("%s observation wait failed: %#v", label, waitFailure)
	}
	for _, marker := range markers {
		if !observationContains(observation, marker) &&
			!observationContains(after, marker) {
			return fmt.Errorf("%s observation does not contain %q", label, marker)
		}
	}
	return nil
}

func isMutationOriginBlocked(
	failure *browserprotocol.Failure,
	origin string,
	freshObservationRequired bool,
) bool {
	return failure != nil &&
		failure.Code == browserprotocol.ErrorMutationOriginBlocked &&
		!failure.Recoverable &&
		failure.RetrySameAction != nil && !*failure.RetrySameAction &&
		failure.AttachmentUsable != nil && *failure.AttachmentUsable &&
		failure.FreshObservationRequired != nil &&
		*failure.FreshObservationRequired == freshObservationRequired &&
		failure.ObservedOrigin == origin
}

func isUnknownMutation(
	failure *browserprotocol.Failure,
	reason string,
) bool {
	return failure != nil &&
		failure.Code == browserprotocol.ErrorMutationOutcomeUnknown &&
		failure.MutationOutcomeReason == reason &&
		!failure.Recoverable &&
		failure.RetrySameAction != nil && !*failure.RetrySameAction &&
		failure.AttachmentUsable != nil && *failure.AttachmentUsable &&
		failure.FreshObservationRequired != nil && *failure.FreshObservationRequired
}

func intPointer(value int) *int {
	return &value
}

func validateEnvironmentEvidence(
	evidence *browserprotocol.EnvironmentEvidence,
	expectedVersion,
	expectedFontSHA256 string,
	expected runtimeExpectation,
) error {
	if expectedVersion == "" || expectedFontSHA256 == "" {
		return errors.New("locked Browser and font versions are required")
	}
	if evidence == nil {
		return errors.New("preflight returned no Browser environment evidence")
	}
	if evidence.BrowserEngine != expected.BrowserEngine ||
		evidence.BrowserDistribution != expected.BrowserDistribution ||
		evidence.BrowserVersion != expectedVersion ||
		evidence.BrowserMajorVersion <= 0 ||
		!strings.HasPrefix(
			evidence.BrowserVersion,
			fmt.Sprintf("%d.", evidence.BrowserMajorVersion),
		) ||
		evidence.BrowserLocale != "en-US" ||
		evidence.BrowserTimezone != "UTC" ||
		evidence.FontContractVersion != "openlinker.browser.fonts.v1" ||
		evidence.FontManifestSHA256 != expectedFontSHA256 {
		return fmt.Errorf(
			"preflight Browser environment evidence is invalid: %#v",
			evidence,
		)
	}
	return nil
}

func validateBackendSelection(
	evidence *browserprotocol.BackendSelectionEvidence,
	expected runtimeExpectation,
	requireRecovered bool,
) error {
	if expected.BackendMode == "" {
		return nil
	}
	if evidence == nil || evidence.Validate() != nil {
		return errors.New("preflight returned no valid Browser backend evidence")
	}
	if evidence.RequestedMode != "openlinker-native-chrome" ||
		evidence.SelectedBackend != "official_chrome_extension" ||
		evidence.FallbackReason != "" ||
		evidence.ExtensionID != expected.ExtensionID ||
		evidence.ExtensionVersion != expected.ExtensionVersion ||
		evidence.ProfileGeneration != expected.ProfileGeneration ||
		(requireRecovered && !evidence.SessionRecovered) {
		return fmt.Errorf("native Browser backend evidence is invalid: %#v", evidence)
	}
	return nil
}

func runReliabilityFixtures(
	socketPath,
	credentialPath,
	leasePath,
	publicURL string,
) error {
	base := strings.TrimRight(publicURL, "/")
	suspectedIdentity := identityFor(10, 10, 10, 1, 10)
	suspected, err := activateClient(
		socketPath,
		credentialPath,
		leasePath,
		suspectedIdentity,
	)
	if err != nil {
		return err
	}
	_, failure := execute(suspected, browserprotocol.Action{
		Kind:        browserprotocol.ActionNavigate,
		Observation: browserprotocol.ObservationSemantic,
		URL:         base + "/challenge-suspected",
	})
	if err := expectChallengeFailure(
		failure,
		browserprotocol.ErrorChallengeSuspected,
		true,
	); err != nil {
		return fmt.Errorf("suspected challenge classification: %w", err)
	}
	clickX, clickY := 240, 140
	_, failure = execute(suspected, browserprotocol.Action{
		Kind:        browserprotocol.ActionClick,
		Observation: browserprotocol.ObservationSemantic,
		X:           &clickX,
		Y:           &clickY,
	})
	if err := expectChallengeFailure(
		failure,
		browserprotocol.ErrorChallengeSuspected,
		true,
	); err != nil {
		return fmt.Errorf("same-document history released challenge gate: %w", err)
	}
	suspectedObservation, failure := execute(
		suspected,
		browserprotocol.Action{
			Kind:        browserprotocol.ActionScreenshot,
			Observation: browserprotocol.ObservationSemantic,
		},
	)
	if failure != nil ||
		!observationContains(suspectedObservation, "same_document_history=advanced") {
		return fmt.Errorf(
			"suspected challenge same-document fixture is invalid: %v",
			failure,
		)
	}
	if _, failure = execute(suspected, browserprotocol.Action{
		Kind:        browserprotocol.ActionNavigate,
		Observation: browserprotocol.ObservationSemantic,
		URL:         base + "/plain",
	}); failure != nil {
		return fmt.Errorf("clean cross-document navigation failed: %w", failure)
	}
	_, failure = execute(suspected, browserprotocol.Action{
		Kind:        browserprotocol.ActionBack,
		Observation: browserprotocol.ObservationSemantic,
	})
	if err := expectChallengeFailure(
		failure,
		browserprotocol.ErrorChallengeSuspected,
		true,
	); err != nil {
		return fmt.Errorf("BFCache challenge restore was not reclassified: %w", err)
	}
	restoredObservation, failure := execute(
		suspected,
		browserprotocol.Action{
			Kind:        browserprotocol.ActionScreenshot,
			Observation: browserprotocol.ObservationSemantic,
		},
	)
	if failure != nil ||
		!observationContains(restoredObservation, "pageshow_persisted=true") {
		return fmt.Errorf(
			"real Chromium did not exercise the BFCache restore fixture: %v",
			failure,
		)
	}
	if _, failure = execute(suspected, browserprotocol.Action{
		Kind:        browserprotocol.ActionForward,
		Observation: browserprotocol.ObservationSemantic,
	}); failure != nil {
		return fmt.Errorf("clean forward navigation did not release challenge gate: %w", failure)
	}
	if _, failure = execute(suspected, browserprotocol.Action{
		Kind:        browserprotocol.ActionClose,
		Observation: browserprotocol.ObservationNone,
	}); failure != nil {
		return fmt.Errorf("suspected challenge attachment close failed: %w", failure)
	}

	deniedIdentity := identityFor(20, 20, 20, 1, 20)
	denied, err := activateClient(
		socketPath,
		credentialPath,
		leasePath,
		deniedIdentity,
	)
	if err != nil {
		return err
	}
	for attempt := 1; attempt <= 4; attempt++ {
		_, failure = execute(denied, browserprotocol.Action{
			Kind:        browserprotocol.ActionNavigate,
			Observation: browserprotocol.ObservationSemantic,
			URL:         base + "/access-denied",
		})
		if failure == nil ||
			failure.Code != browserprotocol.ErrorAccessDenied ||
			failure.ConsecutiveAccessDenials == nil ||
			*failure.ConsecutiveAccessDenials != min(3, attempt) ||
			failure.OriginBlockedForAttachment != (attempt >= 3) {
			return fmt.Errorf(
				"access-denial attempt %d returned invalid evidence: %v",
				attempt,
				failure,
			)
		}
	}
	if _, failure = execute(denied, browserprotocol.Action{
		Kind:        browserprotocol.ActionClose,
		Observation: browserprotocol.ObservationNone,
	}); failure != nil {
		return fmt.Errorf("access-denial attachment close failed: %w", failure)
	}

	requiredIdentity := identityFor(30, 30, 30, 1, 30)
	required, err := activateClient(
		socketPath,
		credentialPath,
		leasePath,
		requiredIdentity,
	)
	if err != nil {
		return err
	}
	_, failure = execute(required, browserprotocol.Action{
		Kind:        browserprotocol.ActionNavigate,
		Observation: browserprotocol.ObservationSemantic,
		URL:         base + "/challenge-required",
	})
	if err := expectChallengeFailure(
		failure,
		browserprotocol.ErrorChallengeRequired,
		false,
	); err != nil {
		return fmt.Errorf("required challenge classification: %w", err)
	}
	if _, terminalFailure := execute(required, browserprotocol.Action{
		Kind:        browserprotocol.ActionScreenshot,
		Observation: browserprotocol.ObservationSemantic,
	}); terminalFailure == nil {
		return errors.New("required challenge attachment remained reusable")
	}
	recoveredIdentity := identityFor(31, 31, 31, 1, 31)
	recovered, err := activateClient(
		socketPath,
		credentialPath,
		leasePath,
		recoveredIdentity,
	)
	if err != nil {
		return err
	}
	if _, failure = execute(recovered, browserprotocol.Action{
		Kind:        browserprotocol.ActionPreflight,
		Observation: browserprotocol.ObservationSemantic,
	}); failure != nil {
		return fmt.Errorf("Runtime did not recover after required challenge: %w", failure)
	}
	if _, failure = execute(recovered, browserprotocol.Action{
		Kind:        browserprotocol.ActionClose,
		Observation: browserprotocol.ObservationNone,
	}); failure != nil {
		return fmt.Errorf("recovered attachment close failed: %w", failure)
	}

	rateIdentity := identityFor(40, 40, 40, 1, 40)
	rateLimited, err := activateClient(
		socketPath,
		credentialPath,
		leasePath,
		rateIdentity,
	)
	if err != nil {
		return err
	}
	_, failure = execute(rateLimited, browserprotocol.Action{
		Kind:        browserprotocol.ActionNavigate,
		Observation: browserprotocol.ObservationSemantic,
		URL:         base + "/rate-limited",
	})
	if failure == nil ||
		failure.Code != browserprotocol.ErrorRateLimited ||
		failure.RetryAfterMS == nil ||
		*failure.RetryAfterMS < 1 ||
		*failure.RetryAfterMS > 2_000 {
		return fmt.Errorf("429 fixture returned invalid Retry-After evidence: %v", failure)
	}
	_, failure = execute(rateLimited, browserprotocol.Action{
		Kind:        browserprotocol.ActionScreenshot,
		Observation: browserprotocol.ObservationSemantic,
	})
	if failure == nil ||
		failure.Code != browserprotocol.ErrorOriginRateLimited ||
		failure.RetryAfterMS == nil {
		return fmt.Errorf("429 retry budget did not reject locally: %v", failure)
	}
	time.Sleep(2100 * time.Millisecond)
	if _, failure = execute(rateLimited, browserprotocol.Action{
		Kind:        browserprotocol.ActionClose,
		Observation: browserprotocol.ObservationNone,
	}); failure != nil {
		return fmt.Errorf("rate-limited attachment close failed: %w", failure)
	}
	return nil
}

func expectChallengeFailure(
	failure *browserprotocol.Failure,
	code browserprotocol.ErrorCode,
	recoverable bool,
) error {
	if failure == nil ||
		failure.Code != code ||
		failure.SiteOutcome != code ||
		failure.Recoverable != recoverable ||
		failure.ClassifierRulesVersion != browserprotocol.ChallengeClassifierRulesVersion {
		return fmt.Errorf("invalid challenge failure: %v", failure)
	}
	return nil
}

func observationContains(
	observation browserprotocol.Observation,
	marker string,
) bool {
	encoded, err := json.Marshal([]json.RawMessage{
		observation.AXTree,
		observation.DOMDiff,
	})
	return err == nil && strings.Contains(string(encoded), marker)
}

func debugObservation(observation browserprotocol.Observation) string {
	raw, err := json.Marshal(map[string]any{
		"title":    observation.Title,
		"ax_tree":  observation.AXTree,
		"dom_diff": observation.DOMDiff,
	})
	if err != nil {
		return "<encode failed>"
	}
	const limit = 8 * 1024
	if len(raw) > limit {
		return string(raw[:limit]) + "..."
	}
	return string(raw)
}

func identityFor(
	run, session, attachment, sessionEpoch, controlEpoch int,
) browserprotocol.Identity {
	// Each independent acceptance scenario owns a principal budget scope. Runs
	// in the same decade intentionally share one scope (for example the initial
	// and resumed Profile runs), while unrelated scenarios cannot consume one
	// another's production-default per-origin budget.
	budgetScope := run / 10
	return browserprotocol.Identity{
		RunID:                              fmt.Sprintf("00000000-0000-4000-8000-%012d", run),
		AgentID:                            fmt.Sprintf("00000000-0000-4000-8000-%012d", 100+budgetScope),
		PrincipalScopeID:                   fmt.Sprintf("ps1_browser_image_acceptance_scope_%d", budgetScope),
		BrowserSessionID:                   fmt.Sprintf("00000000-0000-4000-8000-%012d", session+100),
		SessionEpoch:                       uint64(sessionEpoch),
		AttachmentID:                       fmt.Sprintf("00000000-0000-4000-8000-%012d", attachment+200),
		ControlEpoch:                       uint64(controlEpoch),
		Controller:                         browserprotocol.ControllerAgent,
		BrowserInteractionPolicy:           "restricted",
		BrowserInteractionPolicyGeneration: 1,
		BrowserMutationOrigins:             []string{},
		BrowserMutationOriginsSHA256:       browserprotocol.RestrictedMutationOriginsSHA256,
	}
}

func fullIdentityFor(
	run, session, attachment, sessionEpoch, controlEpoch int,
	origins []string,
) (browserprotocol.Identity, error) {
	canonical, digest, failure := browserprotocol.CanonicalMutationOrigins(
		"full",
		origins,
	)
	if failure != nil {
		return browserprotocol.Identity{}, failure
	}
	identity := identityFor(run, session, attachment, sessionEpoch, controlEpoch)
	identity.BrowserInteractionPolicy = "full"
	identity.BrowserInteractionPolicyGeneration = 2
	identity.BrowserMutationOrigins = canonical
	identity.BrowserMutationOriginsSHA256 = digest
	return identity, nil
}

func activateClient(
	socketPath,
	credentialPath,
	leasePath string,
	identity browserprotocol.Identity,
) (*browserclient.Client, error) {
	lease := browserclient.Lease{
		ContractID: browserclient.LeaseContractID,
		ExpiresAt:  time.Now().UTC().Add(10 * time.Minute),
		Identity:   identity,
	}
	if err := writeLease(leasePath, lease); err != nil {
		return nil, err
	}
	client, err := browserclient.New(browserclient.Config{
		SocketPath:     socketPath,
		CredentialFile: credentialPath,
		LeaseFile:      leasePath,
		Timeout:        actionTimeout,
	})
	if err != nil {
		return nil, fmt.Errorf("create Browser client: %w", err)
	}
	return client, nil
}

func execute(
	client *browserclient.Client,
	action browserprotocol.Action,
) (browserprotocol.Observation, *browserprotocol.Failure) {
	ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
	defer cancel()
	return client.Execute(ctx, action)
}

func validateMCPEvidenceRecovery(
	client *browserclient.Client,
	environment browserprotocol.EnvironmentEvidence,
	identity browserprotocol.Identity,
) error {
	for _, host := range []string{"codex", "claude"} {
		for session := 1; session <= 2; session++ {
			evidenceResults, err := runMCPEvidenceSession(
				client,
				environment,
				identity,
				host,
			)
			if err != nil {
				return fmt.Errorf("%s MCP evidence session %d failed: %w", host, session, err)
			}
			if len(evidenceResults) != 2 {
				return fmt.Errorf(
					"%s MCP evidence session %d returned %d tool results",
					host,
					session,
					len(evidenceResults),
				)
			}
			evidenceCount := 0
			for _, result := range evidenceResults {
				evidence, ok := result["attachment_evidence"].(map[string]any)
				if !ok {
					continue
				}
				evidenceCount++
				if err := validateMCPAttachmentEvidence(evidence, environment, identity); err != nil {
					return fmt.Errorf("%s MCP evidence session %d: %w", host, session, err)
				}
			}
			if evidenceCount != 1 {
				return fmt.Errorf(
					"%s MCP evidence session %d emitted attachment evidence %d times",
					host,
					session,
					evidenceCount,
				)
			}
		}
	}
	return nil
}

func runMCPEvidenceSession(
	client *browserclient.Client,
	environment browserprotocol.EnvironmentEvidence,
	identity browserprotocol.Identity,
	host string,
) ([]map[string]any, error) {
	server := &browserplugin.Server{
		Host: host,
		IO: browserplugin.IO{
			Getenv: func(string) string { return "" },
		},
		ClientFactory: func() (browserplugin.Executor, error) {
			return client, nil
		},
		EvidenceSupplier: func() (browserplugin.EvidenceSnapshot, error) {
			return browserplugin.EvidenceSnapshot{
				Environment:                        environment,
				BrowserSessionID:                   identity.BrowserSessionID,
				SessionEpoch:                       identity.SessionEpoch,
				ControlEpoch:                       identity.ControlEpoch,
				BrowserInteractionPolicy:           identity.BrowserInteractionPolicy,
				BrowserInteractionPolicyGeneration: identity.BrowserInteractionPolicyGeneration,
				BrowserMutationOrigins:             append([]string{}, identity.BrowserMutationOrigins...),
				BrowserMutationOriginsSHA256:       identity.BrowserMutationOriginsSHA256,
			}, nil
		},
	}
	input := strings.NewReader(
		"{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\",\"params\":{}}\n" +
			"{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/call\",\"params\":{\"name\":\"browser_session\",\"arguments\":{\"operation\":\"observe\",\"observation\":\"semantic\"}}}\n" +
			"{\"jsonrpc\":\"2.0\",\"id\":3,\"method\":\"tools/call\",\"params\":{\"name\":\"browser_session\",\"arguments\":{\"operation\":\"observe\",\"observation\":\"semantic\"}}}\n" +
			"{\"jsonrpc\":\"2.0\",\"id\":4,\"method\":\"tools/list\",\"params\":{}}\n",
	)
	var output bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
	defer cancel()
	if err := server.Serve(ctx, input, &output); err != nil {
		return nil, err
	}
	results := make([]map[string]any, 0, 2)
	toolsValidated := false
	for _, line := range strings.Split(strings.TrimSpace(output.String()), "\n") {
		var response map[string]any
		if err := json.Unmarshal([]byte(line), &response); err != nil {
			return nil, errors.New("decode Browser MCP response")
		}
		id, ok := response["id"].(float64)
		if !ok {
			continue
		}
		resultValue, ok := response["result"].(map[string]any)
		if !ok {
			return nil, errors.New("Browser MCP tool response has no result")
		}
		if id == 4 {
			tools, ok := resultValue["tools"].([]any)
			if !ok || len(tools) != 1 {
				return nil, fmt.Errorf("%s MCP must expose exactly one Browser tool", host)
			}
			tool, ok := tools[0].(map[string]any)
			if !ok || tool["name"] != "browser_session" {
				return nil, fmt.Errorf("%s MCP exposed an unexpected Browser tool", host)
			}
			toolsValidated = true
			continue
		}
		if id != 2 && id != 3 {
			continue
		}
		structured, ok := resultValue["structuredContent"].(map[string]any)
		if !ok {
			return nil, errors.New("Browser MCP tool response has no structured content")
		}
		results = append(results, structured)
	}
	if !toolsValidated {
		return nil, fmt.Errorf("%s MCP did not return tools/list", host)
	}
	return results, nil
}

func validateMCPAttachmentEvidence(
	actual map[string]any,
	expected browserprotocol.EnvironmentEvidence,
	identity browserprotocol.Identity,
) error {
	expectedOrigins := make([]any, 0, len(identity.BrowserMutationOrigins))
	for _, origin := range identity.BrowserMutationOrigins {
		expectedOrigins = append(expectedOrigins, origin)
	}
	expectedValues := map[string]any{
		"browser_engine":                        expected.BrowserEngine,
		"browser_distribution":                  expected.BrowserDistribution,
		"browser_major_version":                 float64(expected.BrowserMajorVersion),
		"browser_locale":                        expected.BrowserLocale,
		"browser_timezone":                      expected.BrowserTimezone,
		"font_contract_version":                 expected.FontContractVersion,
		"font_manifest_sha256":                  expected.FontManifestSHA256,
		"browser_interaction_policy":            identity.BrowserInteractionPolicy,
		"browser_interaction_policy_generation": float64(identity.BrowserInteractionPolicyGeneration),
		"browser_mutation_origins":              expectedOrigins,
		"browser_mutation_origins_sha256":       identity.BrowserMutationOriginsSHA256,
		"browser_contract_id":                   browserprotocol.ContractID,
	}
	if len(actual) != len(expectedValues) {
		return fmt.Errorf("attachment evidence field count = %d", len(actual))
	}
	for field, expectedValue := range expectedValues {
		if !reflect.DeepEqual(actual[field], expectedValue) {
			return fmt.Errorf(
				"attachment evidence %s = %#v, want %#v",
				field,
				actual[field],
				expectedValue,
			)
		}
	}
	if _, exists := actual["browser_version"]; exists {
		return errors.New("attachment evidence exposed the full Browser version")
	}
	return nil
}

func expectTargetBlocked(client *browserclient.Client, target string) error {
	_, failure := execute(client, browserprotocol.Action{
		Kind:        browserprotocol.ActionNavigate,
		Observation: browserprotocol.ObservationSemantic,
		URL:         target,
	})
	if failure == nil ||
		failure.Code != browserprotocol.ErrorTargetBlocked ||
		failure.Recoverable {
		return fmt.Errorf("target was not blocked permanently: %s: %v", target, failure)
	}
	return nil
}

func expectRebindingBlocked(client *browserclient.Client, target string) error {
	for attempt := 0; attempt < 6; attempt++ {
		attemptURL, err := rebindAttemptURL(target, attempt)
		if err != nil {
			return err
		}
		_, failure := execute(client, browserprotocol.Action{
			Kind:        browserprotocol.ActionNavigate,
			Observation: browserprotocol.ObservationSemantic,
			URL:         attemptURL,
		})
		if failure != nil &&
			failure.Code == browserprotocol.ErrorTargetBlocked &&
			!failure.Recoverable {
			return nil
		}
		time.Sleep(1200 * time.Millisecond)
	}
	return errors.New("controlled DNS rebinding target was not blocked")
}

func rebindAttemptURL(target string, attempt int) (string, error) {
	parsed, err := url.Parse(target)
	if err != nil || parsed.Scheme != "http" || parsed.Hostname() == "" {
		return "", errors.New("controlled DNS rebinding URL is invalid")
	}
	query := parsed.Query()
	query.Set("openlinker_rebind_attempt", fmt.Sprintf("%d", attempt+1))
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func writeLease(path string, lease browserclient.Lease) error {
	if failure := lease.Validate(time.Now().UTC()); failure != nil {
		return failure
	}
	directory := filepath.Dir(path)
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("Browser lease directory is invalid")
	}
	raw, err := json.Marshal(lease)
	if err != nil {
		return errors.New("encode Browser lease")
	}
	raw = append(raw, '\n')
	temporary, err := os.CreateTemp(directory, ".acceptance-lease-")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	keep := false
	defer func() {
		_ = temporary.Close()
		if !keep {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	if _, err := temporary.Write(raw); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	keep = true
	handle, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer handle.Close()
	return handle.Sync()
}

func waitForPrivateFile(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		info, err := os.Lstat(path)
		if err == nil &&
			info.Mode().IsRegular() &&
			info.Mode()&os.ModeSymlink == 0 &&
			info.Mode().Perm()&0o077 == 0 &&
			info.Size() > 0 {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for private file %s", filepath.Base(path))
}

func waitForSocket(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		info, err := os.Lstat(path)
		if err == nil && info.Mode()&os.ModeSocket != 0 {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return errors.New("timed out waiting for Browser Runtime socket")
}

func publicOrigin(raw string) (string, error) {
	action := browserprotocol.Action{Kind: browserprotocol.ActionNavigate, URL: raw}
	if failure := action.Validate(); failure != nil {
		return "", failure
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return "", errors.New("public URL is invalid")
	}
	return parsed.Scheme + "://" + parsed.Host, nil
}
