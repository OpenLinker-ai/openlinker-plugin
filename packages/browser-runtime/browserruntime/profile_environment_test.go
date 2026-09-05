//go:build !windows

package browserruntime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserprotocol"
)

func TestProfileEnvironmentRoundTripAndVersionOrdering(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	environment := fixtureProfileEnvironment()
	if err := writeProfileEnvironment(root, environment); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadProfileEnvironment(root)
	if err != nil {
		t.Fatal(err)
	}
	if loaded != environment {
		t.Fatalf("loaded environment = %#v, want %#v", loaded, environment)
	}
	for _, fixture := range []struct {
		left  string
		right string
		want  int
	}{
		{left: "149.0.1.0", right: "149.0.1", want: 0},
		{left: "149.0.2", right: "149.0.1.9", want: 1},
		{left: "148.9", right: "149.0", want: -1},
	} {
		got, err := compareBrowserVersions(fixture.left, fixture.right)
		if err != nil || got != fixture.want {
			t.Fatalf("compare(%q, %q) = %d, %v", fixture.left, fixture.right, got, err)
		}
	}
}

func TestProfileEnvironmentRejectsUnknownOrSymlinkState(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	metadata := filepath.Join(root, ".openlinker")
	if err := os.Mkdir(metadata, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(metadata, "environment.v1.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := loadProfileEnvironment(root); err == nil {
		t.Fatal("symlink environment state was accepted")
	}
}

func TestProfileEnvironmentRejectsDuplicateJSONKeys(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := writeProfileEnvironment(root, fixtureProfileEnvironment()); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, profileEnvironmentRelative)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	duplicate := strings.Replace(
		string(raw),
		`"contract_id":"openlinker.browser.profile-environment.v1",`,
		`"contract_id":"openlinker.browser.profile-environment.v1","contract_id":"openlinker.browser.profile-environment.v1",`,
		1,
	)
	if duplicate == string(raw) {
		t.Fatal("fixture did not contain the expected contract field")
	}
	if err := os.WriteFile(path, []byte(duplicate), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadProfileEnvironment(root); err == nil {
		t.Fatal("duplicate-key environment state was accepted")
	}
}

func fixtureProfileEnvironment() ProfileEnvironment {
	return ProfileEnvironment{
		ProfileGeneration: 1,
		EgressLabel:       "primary-sg",
		Evidence: browserprotocol.EnvironmentEvidence{
			BrowserEngine:       "chromium",
			BrowserDistribution: "playwright_chromium",
			BrowserVersion:      "149.0.7827.55",
			BrowserMajorVersion: 149,
			BrowserLocale:       "en-US",
			BrowserTimezone:     "UTC",
			FontContractVersion: "openlinker.browser.fonts.v1",
			FontManifestSHA256:  strings.Repeat("a", 64),
		},
	}
}
