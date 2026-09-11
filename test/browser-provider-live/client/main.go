//go:build !windows

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	openlinker "github.com/OpenLinker-ai/openlinker-go"
	"github.com/OpenLinker-ai/openlinker-plugin/packages/agent-adapters/agentexec"
	"github.com/OpenLinker-ai/openlinker-plugin/packages/agent-adapters/browserclientmode"
	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserprotocol"
)

const (
	providerLiveTimeout = 8 * time.Minute
	maxSecretBytes      = 64 << 10
)

type lifecycleEvent struct {
	EventType string
	Payload   map[string]any
}

type lifecycleRecorder struct {
	mu     sync.Mutex
	events []lifecycleEvent
}

type liveResult struct {
	Status                         string `json:"status"`
	Provider                       string `json:"provider"`
	ProviderVersion                string `json:"provider_version"`
	BrowserClientModeRequested     string `json:"browser_client_mode_requested"`
	BrowserClientModeSelected      string `json:"browser_client_mode_selected"`
	BrowserTool                    string `json:"browser_tool"`
	BrowserToolSurfaceCount        int    `json:"browser_tool_surface_count"`
	BrowserContractID              string `json:"browser_contract_id"`
	BrowserInteractionPolicy       string `json:"browser_interaction_policy"`
	BrowserPolicyGeneration        int64  `json:"browser_interaction_policy_generation"`
	BrowserMutationOriginsSHA256   string `json:"browser_mutation_origins_sha256"`
	FixtureOrigin                  string `json:"fixture_origin"`
	ObservedMarkerSHA256           string `json:"observed_marker_sha256"`
	ProviderFinalResponseSHA256    string `json:"provider_final_response_sha256"`
	ReadyLifecycleEvidenceObserved bool   `json:"ready_lifecycle_evidence_observed"`
	ClosedLifecycleObserved        bool   `json:"closed_lifecycle_observed"`
}

func main() {
	if err := prepareRuntimeIdentity(); err != nil {
		fmt.Fprintln(os.Stderr, "Browser Provider live acceptance:", err)
		os.Exit(1)
	}
	mode := flag.String("mode", "", "Browser client mode: native or mcp")
	fixtureURL := flag.String("fixture-url", "", "controlled public HTTPS fixture URL")
	flag.Parse()

	secret, err := readSecret(os.Stdin)
	if err != nil {
		fail("", err)
	}
	result, err := runLive(
		strings.TrimSpace(*mode),
		strings.TrimSpace(*fixtureURL),
		secret,
	)
	if err != nil {
		fail(secret, err)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		fail(secret, errors.New("encode live acceptance evidence"))
	}
	if bytes.Contains(raw, []byte(secret)) {
		fail(secret, errors.New("live acceptance evidence contained the Provider credential"))
	}
	fmt.Println(string(raw))
}

func runLive(mode, fixtureURL, secret string) (liveResult, error) {
	provider := strings.ToLower(strings.TrimSpace(os.Getenv("OPENLINKER_BROWSER_LIVE_PROVIDER")))
	if provider != "codex" && provider != "claude" {
		return liveResult{}, errors.New("test image does not fix a supported Provider")
	}
	if mode != "native" && mode != "mcp" {
		return liveResult{}, errors.New("mode must be native or mcp")
	}
	origin, err := publicHTTPSOrigin(fixtureURL)
	if err != nil {
		return liveResult{}, err
	}
	origins, originsDigest, failure := browserprotocol.CanonicalMutationOrigins(
		"full",
		[]string{origin},
	)
	if failure != nil {
		return liveResult{}, failure
	}

	environment := setEnvironment(os.Environ(), map[string]string{
		"HOME":              "/provider",
		"CODEX_HOME":        "/provider",
		"CLAUDE_CONFIG_DIR": "/provider",
		"TMPDIR":            "/tmp",
	})
	secretName := "CODEX_API_KEY"
	pluginRoot := "/opt/openlinker/agent-runtime-plugin/codex"
	if provider == "claude" {
		secretName = "ANTHROPIC_API_KEY"
		pluginRoot = "/opt/openlinker/agent-runtime-plugin/claude"
	}
	environment = setEnvironment(environment, map[string]string{secretName: secret})

	selectedPlugin := ""
	if mode == "native" {
		selection, selectErr := browserclientmode.Select(browserclientmode.Options{
			Provider:         provider,
			Requested:        "native",
			PluginPath:       pluginRoot,
			RequireImmutable: true,
			RunHostCommand: func(arguments ...string) ([]byte, error) {
				return runProviderHostCommand(
					providerHostEnvironment(environment),
					arguments...,
				)
			},
		})
		if selectErr != nil {
			return liveResult{}, selectErr
		}
		if selection.Selected != "native" || selection.FallbackReason != "" {
			return liveResult{}, errors.New("strict native selection did not remain native")
		}
		selectedPlugin = selection.PluginPath
	}

	providerVersion, err := readProviderVersion(providerHostEnvironment(environment))
	if err != nil {
		return liveResult{}, err
	}
	config := agentexec.ProviderConfig{
		Provider:                   provider,
		Bin:                        "/usr/local/bin/openlinker-provider-launcher",
		Workspace:                  "/workspace",
		Model:                      providerModel(provider),
		Sandbox:                    "read-only",
		Permission:                 "dontAsk",
		AllowedTools:               []string{"mcp__openlinker_browser__browser_session"},
		Timeout:                    providerLiveTimeout,
		SessionReuse:               false,
		WebSearch:                  false,
		CodexApproval:              "never",
		CodexBaseURL:               strings.TrimSpace(os.Getenv("OPENLINKER_CODEX_BASE_URL")),
		Env:                        environment,
		ExecutionProfile:           "browser",
		BrowserInteractionPolicy:   "full",
		BrowserClientModeRequested: mode,
		BrowserClientMode:          mode,
		BrowserPluginBin:           "/usr/local/bin/openlinker-plugin-host",
		BrowserNativePlugin:        selectedPlugin,
		BrowserSocket:              "/browser-control/openlinker.browser.sock",
		BrowserCredentialFile:      "/browser-control/channel-credential",
		BrowserLeaseRoot:           "/browser-control/leases",
		BrowserBrokerRoot:          "/browser-tool",
	}
	providerRunner, err := agentexec.NewProvider(config)
	if err != nil {
		return liveResult{}, err
	}

	runID, err := newUUID()
	if err != nil {
		return liveResult{}, err
	}
	agentID, err := newUUID()
	if err != nil {
		return liveResult{}, err
	}
	runtimeSessionID, err := newUUID()
	if err != nil {
		return liveResult{}, err
	}
	runtimeAttachmentID, err := newUUID()
	if err != nil {
		return liveResult{}, err
	}
	deadline := time.Now().UTC().Add(providerLiveTimeout)
	recorder := &lifecycleRecorder{}
	prompt := strings.Join([]string{
		"Use only the browser_session tool.",
		"Call operation act with exactly one navigate action and semantic observation for this URL:",
		fixtureURL,
		"Read the exact text of the element named provider-live-marker from the resulting semantic page state.",
		"Do not use shell, curl, WebSearch, WebFetch, direct HTTP, or another tool.",
		"Return exactly that marker text and nothing else.",
	}, "\n")
	ctx, cancel := context.WithTimeout(context.Background(), providerLiveTimeout)
	defer cancel()
	providerResult, err := providerRunner.Run(ctx, agentexec.RunContext{
		RunID:             runID,
		AgentID:           agentID,
		AttemptDeadlineAt: deadline,
		RunDeadlineAt:     deadline,
		Authority: &openlinker.RuntimeAuthorityContext{
			PrincipalScopeID:                   "provider_live_" + strings.ReplaceAll(runID, "-", ""),
			RuntimeSessionID:                   runtimeSessionID,
			RuntimeSessionEpoch:                1,
			RuntimeAttachmentID:                runtimeAttachmentID,
			ExecutionProfile:                   "browser",
			BrowserInteractionPolicy:           "full",
			BrowserInteractionPolicyGeneration: 1,
			BrowserMutationOrigins:             origins,
			BrowserMutationOriginsSHA256:       originsDigest,
		},
		Input: prompt,
		Emit:  recorder.emit,
	})
	if err != nil {
		return liveResult{}, err
	}
	output, ok := providerResult.Output.(map[string]any)
	if !ok {
		return liveResult{}, errors.New("Provider returned a non-object result")
	}
	summary, _ := output["summary"].(string)
	observedMarker := strings.TrimSpace(summary)
	if !validMarker(observedMarker) {
		return liveResult{}, errors.New("Provider did not return a valid marker observed through browser_session")
	}
	selected := "direct_mcp"
	if mode == "native" {
		selected = "plugin_native"
	}
	if err := validateProviderEvidence(output, selected, originsDigest); err != nil {
		return liveResult{}, err
	}
	ready, closed := recorder.validate(selected, originsDigest)
	if !ready || !closed {
		return liveResult{}, errors.New("Provider lifecycle omitted required Browser evidence")
	}
	summaryDigest := sha256.Sum256([]byte(summary))
	summarySHA256 := hex.EncodeToString(summaryDigest[:])
	return liveResult{
		Status:                         "passed",
		Provider:                       provider,
		ProviderVersion:                providerVersion,
		BrowserClientModeRequested:     mode,
		BrowserClientModeSelected:      selected,
		BrowserTool:                    "browser_session",
		BrowserToolSurfaceCount:        1,
		BrowserContractID:              browserprotocol.ContractID,
		BrowserInteractionPolicy:       "full",
		BrowserPolicyGeneration:        1,
		BrowserMutationOriginsSHA256:   originsDigest,
		FixtureOrigin:                  origin,
		ObservedMarkerSHA256:           summarySHA256,
		ProviderFinalResponseSHA256:    summarySHA256,
		ReadyLifecycleEvidenceObserved: ready,
		ClosedLifecycleObserved:        closed,
	}, nil
}

func validateProviderEvidence(output map[string]any, selected, digest string) error {
	checks := map[string]any{
		"browser_execution_profile":             "isolated",
		"browser_tool":                          "browser_session",
		"browser_client_mode_selected":          selected,
		"browser_interaction_policy":            "full",
		"browser_mutation_origins_sha256":       digest,
		"browser_contract_id":                   browserprotocol.ContractID,
		"browser_interaction_policy_generation": int64(1),
	}
	for key, expected := range checks {
		actual := output[key]
		if number, ok := actual.(float64); ok && key == "browser_interaction_policy_generation" {
			actual = int64(number)
		}
		if actual != expected {
			return fmt.Errorf("Provider result omitted Browser evidence %s", key)
		}
	}
	return nil
}

func (recorder *lifecycleRecorder) emit(eventType string, raw any) error {
	payload, ok := raw.(map[string]any)
	if !ok {
		return errors.New("lifecycle payload is not an object")
	}
	copied := make(map[string]any, len(payload))
	for key, value := range payload {
		copied[key] = value
	}
	recorder.mu.Lock()
	recorder.events = append(recorder.events, lifecycleEvent{EventType: eventType, Payload: copied})
	recorder.mu.Unlock()
	return nil
}

func (recorder *lifecycleRecorder) validate(selected, digest string) (bool, bool) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	ready := false
	closed := false
	for _, event := range recorder.events {
		if event.EventType != "run.browser.lifecycle" {
			continue
		}
		switch event.Payload["phase"] {
		case "ready":
			ready = event.Payload["browser_client_mode_selected"] == selected &&
				event.Payload["browser_interaction_policy"] == "full" &&
				event.Payload["browser_mutation_origins_sha256"] == digest
		case "closed":
			closed = event.Payload["status"] == "success" &&
				event.Payload["browser_interaction_policy"] == "full" &&
				event.Payload["browser_mutation_origins_sha256"] == digest
		}
	}
	return ready, closed
}

func readSecret(reader io.Reader) (string, error) {
	raw, err := io.ReadAll(io.LimitReader(reader, maxSecretBytes+1))
	if err != nil {
		return "", errors.New("read Provider credential from stdin")
	}
	if len(raw) > maxSecretBytes {
		return "", errors.New("Provider credential exceeded the size limit")
	}
	secret := strings.TrimSpace(string(raw))
	if secret == "" || strings.ContainsAny(secret, "\r\n\x00") {
		return "", errors.New("Provider credential from stdin is invalid")
	}
	return secret, nil
}

func publicHTTPSOrigin(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" ||
		parsed.User != nil || parsed.Fragment != "" {
		return "", errors.New("fixture URL must be public HTTPS")
	}
	origin, _, failure := browserprotocol.CanonicalMutationOrigins(
		"full",
		[]string{"https://" + parsed.Host},
	)
	if failure != nil || len(origin) != 1 {
		return "", errors.New("fixture URL has an invalid public HTTPS origin")
	}
	return origin[0], nil
}

func validMarker(value string) bool {
	if len(value) < 24 || len(value) > 128 || !strings.HasPrefix(value, "provider-live-") {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') ||
			(character >= '0' && character <= '9') || character == '-' {
			continue
		}
		return false
	}
	return true
}

func readProviderVersion(environment []string) (string, error) {
	raw, err := runProviderHostCommand(environment, "--version")
	if err != nil {
		return "", err
	}
	version := strings.TrimSpace(string(raw))
	if version == "" || len(version) > 256 || strings.ContainsAny(version, "\r\n") {
		return "", errors.New("Provider returned invalid version evidence")
	}
	return version, nil
}

func runProviderHostCommand(environment []string, arguments ...string) ([]byte, error) {
	command := exec.Command("/usr/local/bin/openlinker-provider-launcher", arguments...) // #nosec G204 -- fixed test image binary and internally bounded arguments.
	command.Env = environment
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return nil, fmt.Errorf("Provider host command failed: %w: %s", err, bounded(stderr.String(), 300))
	}
	if stdout.Len() > 1<<20 {
		return nil, errors.New("Provider host command output exceeded the limit")
	}
	return stdout.Bytes(), nil
}

func providerHostEnvironment(environment []string) []string {
	blocked := map[string]struct{}{
		"CODEX_API_KEY":     {},
		"ANTHROPIC_API_KEY": {},
	}
	result := make([]string, 0, len(environment))
	for _, item := range environment {
		key, _, ok := strings.Cut(item, "=")
		if ok {
			if _, found := blocked[key]; found {
				continue
			}
		}
		result = append(result, item)
	}
	return result
}

func providerModel(provider string) string {
	if provider == "codex" {
		return strings.TrimSpace(os.Getenv("OPENLINKER_CODEX_MODEL"))
	}
	return strings.TrimSpace(os.Getenv("OPENLINKER_CLAUDE_MODEL"))
}

func setEnvironment(environment []string, values map[string]string) []string {
	result := make([]string, 0, len(environment)+len(values))
	for _, item := range environment {
		key, _, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		if _, replaced := values[key]; !replaced {
			result = append(result, item)
		}
	}
	for key, value := range values {
		result = append(result, key+"="+value)
	}
	return result
}

func newUUID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", errors.New("generate live acceptance identity")
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	return fmt.Sprintf(
		"%08x-%04x-%04x-%04x-%012x",
		value[0:4], value[4:6], value[6:8], value[8:10], value[10:16],
	), nil
}

func bounded(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}

func fail(secret string, err error) {
	message := err.Error()
	if secret != "" {
		message = strings.ReplaceAll(message, secret, "[REDACTED]")
	}
	fmt.Fprintln(os.Stderr, "Browser Provider live acceptance:", message)
	os.Exit(1)
}
