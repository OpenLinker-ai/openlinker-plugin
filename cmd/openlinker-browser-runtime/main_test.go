//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserruntime"
)

func TestReadCredentialFileAcceptsOwnerOnlyAbsoluteFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "channel-token")
	token := strings.Repeat("a", 64)
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	value, err := readCredentialFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if value != token {
		t.Fatalf("credential = %q, want token", value)
	}
}

func TestOpsViewerStreamArgumentsAndEngineEnvironmentAreClosed(t *testing.T) {
	t.Parallel()
	runID := "11111111-1111-4111-8111-111111111111"
	parsedRunID, ttl, err := parseOpsViewerStreamArguments([]string{
		"--run-id", runID, "--ttl", "10m",
	})
	if err != nil || parsedRunID != runID || ttl != 10*time.Minute {
		t.Fatalf("parsed stream arguments = %q, %v, %v", parsedRunID, ttl, err)
	}
	for _, arguments := range [][]string{
		{"--run-id", runID, "--ttl", "59s"},
		{"--run-id", runID, "--ttl", "31m"},
		{"--ttl", "10m", "--run-id", runID},
		{"--run-id", "not-a-uuid", "--ttl", "10m"},
	} {
		if _, _, err := parseOpsViewerStreamArguments(arguments); err == nil {
			t.Fatalf("unsafe stream arguments were accepted: %#v", arguments)
		}
	}
	environment := browserEngineEnvironment(browserruntime.ProfileEnvironment{
		ProfileGeneration: 1,
	}, true)
	if !containsEnvironment(environment, "OPENLINKER_BROWSER_OPS_OBSERVER_ENABLED=true") {
		t.Fatal("enabled Browser Engine environment is missing the Ops flag")
	}
	disabled := browserEngineEnvironment(browserruntime.ProfileEnvironment{
		ProfileGeneration: 1,
	}, false)
	if containsEnvironment(disabled, "OPENLINKER_BROWSER_OPS_OBSERVER_ENABLED=true") {
		t.Fatal("disabled Browser Engine environment exposes the Ops flag")
	}
}

func TestEitherObservationEntryPointEnablesEngineOps(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name              string
		opsViewer         bool
		authenticated     bool
		wantEngineEnabled bool
	}{
		{name: "both disabled"},
		{name: "operator Viewer", opsViewer: true, wantEngineEnabled: true},
		{name: "authenticated observation", authenticated: true, wantEngineEnabled: true},
		{name: "both enabled", opsViewer: true, authenticated: true, wantEngineEnabled: true},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := engineOpsObservationEnabled(test.opsViewer, test.authenticated); got != test.wantEngineEnabled {
				t.Fatalf("engine Ops enabled = %v, want %v", got, test.wantEngineEnabled)
			}
		})
	}
}

func containsEnvironment(environment []string, expected string) bool {
	for _, entry := range environment {
		if entry == expected {
			return true
		}
	}
	return false
}

func TestLoadOrCreateCredentialFileCreatesPrivateInternalCredential(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "channel-token")
	value, err := loadOrCreateCredentialFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(value) != 64 || strings.Trim(value, "0123456789abcdef") != "" {
		t.Fatalf("generated credential shape = %q", value)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("generated credential mode = %o", info.Mode().Perm())
	}
	reloaded, err := loadOrCreateCredentialFile(path)
	if err != nil || reloaded != value {
		t.Fatalf("reloaded credential = %q, %v", reloaded, err)
	}
}

func TestReadCredentialFileRejectsUnsafeSources(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	valid := filepath.Join(dir, "valid")
	if err := os.WriteFile(valid, []byte(strings.Repeat("a", 64)), 0o600); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(dir, "symlink")
	if err := os.Symlink(valid, symlink); err != nil {
		t.Fatal(err)
	}
	worldReadable := filepath.Join(dir, "world-readable")
	if err := os.WriteFile(worldReadable, []byte(strings.Repeat("a", 64)), 0o644); err != nil {
		t.Fatal(err)
	}
	whitespace := filepath.Join(dir, "whitespace")
	if err := os.WriteFile(whitespace, []byte(strings.Repeat("a", 32)+" "+strings.Repeat("b", 32)), 0o600); err != nil {
		t.Fatal(err)
	}
	leadingWhitespace := filepath.Join(dir, "leading-whitespace")
	if err := os.WriteFile(leadingWhitespace, []byte(" "+strings.Repeat("a", 64)), 0o600); err != nil {
		t.Fatal(err)
	}
	short := filepath.Join(dir, "short")
	if err := os.WriteFile(short, []byte("too-short"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{
		"missing":        filepath.Join(dir, "missing"),
		"relative":       "relative-token",
		"symlink":        symlink,
		"world-readable": worldReadable,
		"whitespace":     whitespace,
		"leading-space":  leadingWhitespace,
		"short":          short,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := readCredentialFile(path); err == nil {
				t.Fatal("readCredentialFile() succeeded, want error")
			}
		})
	}
}

func TestBrowserProfileEnvironmentIsOperatorOnlyAndStrict(t *testing.T) {
	fontSHA := strings.Repeat("a", 64)
	for _, name := range []string{
		"OPENLINKER_BROWSER_ENGINE",
		"OPENLINKER_BROWSER_DISTRIBUTION",
		"OPENLINKER_BROWSER_VERSION",
		"OPENLINKER_BROWSER_PROFILE_GENERATION",
		"OPENLINKER_BROWSER_LOCALE",
		"OPENLINKER_BROWSER_TIMEZONE",
		"OPENLINKER_BROWSER_FONT_CONTRACT_VERSION",
		"OPENLINKER_BROWSER_FONT_MANIFEST_SHA256",
		"OPENLINKER_BROWSER_EGRESS_LABEL",
	} {
		t.Setenv(name, "")
	}
	if _, err := browserProfileEnvironment(); err == nil {
		t.Fatal("missing font-manifest fixture was accepted")
	}
	t.Setenv("OPENLINKER_BROWSER_FONT_MANIFEST_SHA256", fontSHA)
	environment, err := browserProfileEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	if environment.Evidence.BrowserEngine != "chromium" ||
		environment.Evidence.BrowserVersion != defaultBrowserVersion ||
		environment.ProfileGeneration != 1 ||
		environment.EgressLabel != defaultEgressLabel {
		t.Fatalf("default Browser environment = %#v", environment)
	}

	t.Setenv("OPENLINKER_BROWSER_ENGINE", "chrome")
	if _, err := browserProfileEnvironment(); err == nil {
		t.Fatal("Chrome without an explicit compatible distribution was accepted")
	}
	t.Setenv("OPENLINKER_BROWSER_DISTRIBUTION", "chrome_for_testing")
	if _, err := browserProfileEnvironment(); err != nil {
		t.Fatalf("locked Chrome environment was rejected: %v", err)
	}
	t.Setenv("OPENLINKER_BROWSER_EGRESS_LABEL", "tenant-a")
	if environment, err := browserProfileEnvironment(); err != nil ||
		environment.EgressLabel != "tenant-a" {
		t.Fatalf("declared egress binding = %#v, %v", environment, err)
	}
	t.Setenv("OPENLINKER_BROWSER_EGRESS_LABEL", "site response")
	if _, err := browserProfileEnvironment(); err == nil {
		t.Fatal("invalid egress binding was accepted")
	}
}
