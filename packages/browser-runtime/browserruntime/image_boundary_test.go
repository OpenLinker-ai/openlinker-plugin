//go:build !windows

package browserruntime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestBrowserImageIsSeparatePinnedAndHasNoProviderCredentialSurface(t *testing.T) {
	t.Parallel()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve browserruntime package path")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", "..", ".."))
	browserDockerfile, err := os.ReadFile(filepath.Join(root, "Dockerfile.browser"))
	if err != nil {
		t.Fatal(err)
	}
	browserSource := string(browserDockerfile)
	versionFixture, err := os.ReadFile(
		filepath.Join(root, "packages", "browser-runtime", "browser-engine", "browser-versions.json"),
	)
	if err != nil {
		t.Fatal(err)
	}
	var lockedBrowserVersions map[string]string
	if err := json.Unmarshal(versionFixture, &lockedBrowserVersions); err != nil {
		t.Fatal(err)
	}
	if len(lockedBrowserVersions) != 2 {
		t.Fatalf("Browser version fixture = %#v", lockedBrowserVersions)
	}
	for platform, lockedBrowserVersion := range lockedBrowserVersions {
		if (platform != "linux-amd64" && platform != "linux-arm64") ||
			lockedBrowserVersion == "" ||
			!strings.Contains(
				browserSource,
				"FROM browser-base AS browser-"+platform,
			) ||
			!strings.Contains(
				browserSource,
				"OPENLINKER_BROWSER_VERSION="+lockedBrowserVersion,
			) {
			t.Fatalf(
				"Dockerfile.browser does not use Browser version fixture %q=%q",
				platform,
				lockedBrowserVersion,
			)
		}
	}
	required := []string{
		"mcr.microsoft.com/playwright:v1.61.1-noble@sha256:5b8f294a",
		"useradd --uid 10001 --gid 10001",
		"USER 10001:10001",
		"NO_PROXY=",
		"OPENLINKER_BROWSER_CHANNEL_CREDENTIAL_FILE=/browser-control/channel-credential",
		"OPENLINKER_BROWSER_PROFILE_DIR=/browser-tmp/profiles/active",
		"OPENLINKER_BROWSER_PROFILE_STORE=/browser-state/encrypted-profiles",
		"OPENLINKER_BROWSER_PROFILE_ROOT_KEY_FILE=/browser-key/profile-root-key",
		"OPENLINKER_BROWSER_ENGINE=chromium",
		"OPENLINKER_BROWSER_DISTRIBUTION=playwright_chromium",
		"OPENLINKER_BROWSER_FONT_MANIFEST_SHA256=8a130568",
		"openlinker-browser-font-manifest",
		`ENTRYPOINT ["/usr/local/bin/openlinker-browser-runtime"]`,
	}
	for _, value := range required {
		if !strings.Contains(browserSource, value) {
			t.Errorf("Dockerfile.browser is missing %q", value)
		}
	}
	forbidden := []string{
		"EXPOSE ",
		"OPENAI_API_KEY",
		"ANTHROPIC_API_KEY",
		"CODEX_API_KEY",
		"OPENLINKER_AGENT_TOKEN",
		"docker.sock",
		"/Users/",
		"/home/pwuser/.config",
		"OPENLINKER_BROWSER_PROFILE_DIR=/browser-state/",
	}
	for _, value := range forbidden {
		if strings.Contains(browserSource, value) {
			t.Errorf("Dockerfile.browser contains forbidden surface %q", value)
		}
	}
	engineSource, err := os.ReadFile(
		filepath.Join(root, "packages", "browser-runtime", "browser-engine", "src", "engine.ts"),
	)
	if err != nil {
		t.Fatal(err)
	}
	documentTrackerSource, err := os.ReadFile(
		filepath.Join(root, "packages", "browser-runtime", "browser-engine", "src", "document-generation.ts"),
	)
	if err != nil {
		t.Fatal(err)
	}
	combinedBrowserSource := string(engineSource) + string(documentTrackerSource)
	for _, forbiddenSource := range []string{
		"connectOverCDP",
		"--remote-debugging-port",
		"--remote-debugging-address",
	} {
		if strings.Contains(combinedBrowserSource, forbiddenSource) {
			t.Errorf("Browser Engine contains forbidden CDP surface %q", forbiddenSource)
		}
	}
	if !strings.Contains(combinedBrowserSource, "newCDPSession") {
		t.Error("Browser Engine does not establish its private pipe-backed CDP session")
	}
	if !strings.Contains(
		combinedBrowserSource,
		`ignoreDefaultArgs: [`,
	) || !strings.Contains(
		combinedBrowserSource,
		`"--disable-back-forward-cache"`,
	) {
		t.Error("Browser Engine does not enable the real BFCache lifecycle contract")
	}
	if !strings.Contains(
		combinedBrowserSource,
		`launchOptions.channel =`,
	) {
		t.Error("Browser Engine does not select the pinned full Browser channel")
	}
	if !strings.Contains(combinedBrowserSource, "launchOptions.executablePath") {
		t.Error("Browser Engine does not support the image-locked Official Chrome executable")
	}
	chromeDockerfile, err := os.ReadFile(
		filepath.Join(root, "Dockerfile.browser.chrome"),
	)
	if err != nil {
		t.Fatal(err)
	}
	chromeSource := string(chromeDockerfile)
	for _, required := range []string{
		"ARG OPENLINKER_BROWSER_BASE\nFROM ${OPENLINKER_BROWSER_BASE}",
		`base_digest="${OPENLINKER_BROWSER_BASE##*@sha256:}"`,
		`test "${#base_digest}" -eq 64`,
		"ARG TARGETARCH",
		`test "${TARGETARCH}" = amd64`,
		"OPENLINKER_CHROME_ARTIFACT",
		"OPENLINKER_CHROME_LOCK",
		"OPENLINKER_BROWSER_PROFILE_GENERATION",
		"verify-chrome-lock.mjs",
		"/opt/google/chrome/chrome",
		"OPENLINKER_BROWSER_ENGINE=chrome",
		`org.opencontainers.image.openlinker.redistribution="not-approved"`,
	} {
		if !strings.Contains(chromeSource, required) {
			t.Errorf("operator Chrome Dockerfile is missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"OPENLINKER_BROWSER_BASE=openlinker-browser-runtime:dev",
		"apt-get",
		"curl ",
		"wget ",
		"google-chrome-stable",
		"EXPOSE ",
	} {
		if strings.Contains(chromeSource, forbidden) {
			t.Errorf("operator Chrome Dockerfile contains forbidden fetch or surface %q", forbidden)
		}
	}
	engineReadme, err := os.ReadFile(filepath.Join(root, "packages", "browser-runtime", "browser-engine", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(engineReadme), "--platform linux/amd64") {
		t.Error("operator Chrome build documentation does not select its only supported architecture")
	}

	providerDockerfile, err := os.ReadFile(filepath.Join(root, "Dockerfile.providers"))
	if err != nil {
		t.Fatal(err)
	}
	providerSource := string(providerDockerfile)
	if !strings.Contains(
		providerSource,
		`ENTRYPOINT ["/usr/local/bin/openlinker-runtime-entrypoint"]`,
	) {
		t.Error("Dockerfile.providers must start the privileged entrypoint directly so it can drop identity before execing tini")
	}
	if strings.Contains(
		providerSource,
		`ENTRYPOINT ["/usr/bin/tini", "-g", "--", "/usr/local/bin/openlinker-runtime-entrypoint"]`,
	) {
		t.Error("Dockerfile.providers must not leave a capability-limited root tini unable to signal the Runtime UID")
	}
	for _, value := range []string{
		"COPY packages/browser-runtime/browser-engine",
		"mcr.microsoft.com/playwright",
		"playwright-core",
	} {
		if strings.Contains(providerSource, value) {
			t.Errorf("Dockerfile.providers unexpectedly contains Browser engine dependency %q", value)
		}
	}

	composeSources := make(map[string]string)
	for _, name := range []string{
		"deploy/compose.codex.browser.yml",
		"deploy/compose.claude.browser.yml",
	} {
		raw, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		source := string(raw)
		composeSources[name] = source
		for _, required := range []string{
			"openlinker-browser-runtime:",
			"condition: service_healthy",
			"OPENLINKER_BROWSER_PROFILE_STORE: /browser-state/encrypted-profiles",
			"OPENLINKER_BROWSER_PROFILE_WORK_ROOT: /browser-tmp/profiles",
			"OPENLINKER_BROWSER_PROFILE_ROOT_KEY_FILE: /browser-key/profile-root-key",
			"OPENLINKER_BROWSER_OPS_VIEWER_ENABLED: ${OPENLINKER_BROWSER_OPS_VIEWER_ENABLED:-false}",
			"-browser-ops:/browser-ops",
			"-profile-key:/browser-key",
			"- agent-internal",
			"/browser-home:rw,noexec,nosuid,nodev,size=64m,uid=10001,gid=10001,mode=0700",
			"/tmp:rw,noexec,nosuid,nodev,size=64m,uid=10001,gid=10001,mode=0700",
			"stop_grace_period: 30s",
			"    init: true",
			`test: ["CMD", "/usr/local/bin/openlinker-browser-runtime", "healthcheck"]`,
			"      interval: 5s",
			"      timeout: 2s",
			"      retries: 30",
		} {
			if !strings.Contains(source, required) {
				t.Errorf("%s is missing %q", name, required)
			}
		}
		for _, forbidden := range []string{
			"CODEX_API_KEY",
			"ANTHROPIC_API_KEY",
			"OPENLINKER_AGENT_TOKEN",
			"egress-public",
			"ports:",
			"cap_add:",
			"docker.sock",
			"/browser-state/profile-root-key",
		} {
			if strings.Contains(source, forbidden) {
				t.Errorf("%s contains forbidden Browser boundary %q", name, forbidden)
			}
		}
		if strings.Count(source, "    init: true\n") != 1 {
			t.Errorf("%s must enable init only for Browser Runtime", name)
		}
		if strings.Count(source, "-browser-ops:/browser-ops") != 1 {
			t.Errorf("%s must mount the Ops volume only into Browser Runtime", name)
		}
	}
	codexHealth := browserRuntimeHealthContract(
		t,
		composeSources["deploy/compose.codex.browser.yml"],
	)
	claudeHealth := browserRuntimeHealthContract(
		t,
		composeSources["deploy/compose.claude.browser.yml"],
	)
	if codexHealth != claudeHealth {
		t.Fatalf("Codex and Claude Browser health contracts differ:\nCodex:\n%s\nClaude:\n%s", codexHealth, claudeHealth)
	}
	nativeChromeOverlay, err := os.ReadFile(
		filepath.Join(root, "deploy/compose.codex.native-chrome.yml"),
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"healthcheck:", "init:"} {
		if strings.Contains(string(nativeChromeOverlay), forbidden) {
			t.Errorf("native Chrome overlay overrides shared Browser Runtime %q", forbidden)
		}
	}

	for _, name := range []string{
		"deploy/compose.codex.yml",
		"deploy/compose.claude.yml",
	} {
		raw, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		source := string(raw)
		for _, browserOnly := range []string{
			"OPENLINKER_AGENT_EXECUTION_PROFILE",
			"/browser-control",
			"/browser-tool",
			"openlinker-browser-runtime",
		} {
			if strings.Contains(source, browserOnly) {
				t.Errorf("%s unexpectedly enables Browser-only surface %q", name, browserOnly)
			}
		}
	}
}

func browserRuntimeHealthContract(t *testing.T, source string) string {
	t.Helper()
	start := strings.Index(source, "    healthcheck:\n")
	if start < 0 {
		t.Fatal("Browser Runtime healthcheck is missing")
	}
	end := strings.Index(source[start:], "    pids_limit:")
	if end < 0 {
		t.Fatal("Browser Runtime healthcheck boundary is missing")
	}
	return source[start : start+end]
}
