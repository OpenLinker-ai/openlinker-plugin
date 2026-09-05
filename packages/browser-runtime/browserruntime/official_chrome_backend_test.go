//go:build !windows

package browserruntime

import (
	"path/filepath"
	"slices"
	"testing"
)

func TestOfficialChromeEnvironmentPreservesLimitsAndOverridesEngineIdentity(
	t *testing.T,
) {
	environment := officialChromeEnvironment(
		[]string{
			"HOME=/browser-home",
			"OPENLINKER_BROWSER_MAX_ACTIONS_PER_ORIGIN_MINUTE=120",
			"OPENLINKER_BROWSER_ENGINE=chromium",
			"OPENLINKER_BROWSER_ENGINE=must-not-survive",
		},
		[]string{
			"OPENLINKER_BROWSER_ENGINE=chrome",
			"OPENLINKER_BROWSER_DISTRIBUTION=chrome_for_testing",
		},
	)
	for _, expected := range []string{
		"HOME=/browser-home",
		"OPENLINKER_BROWSER_MAX_ACTIONS_PER_ORIGIN_MINUTE=120",
		"OPENLINKER_BROWSER_ENGINE=chrome",
		"OPENLINKER_BROWSER_DISTRIBUTION=chrome_for_testing",
	} {
		if !slices.Contains(environment, expected) {
			t.Fatalf("official Chrome environment omitted %q: %#v", expected, environment)
		}
	}
	engineEntries := 0
	for _, entry := range environment {
		if entry == "OPENLINKER_BROWSER_ENGINE=chrome" {
			engineEntries++
		}
	}
	if engineEntries != 1 {
		t.Fatalf("official Chrome engine entries = %d: %#v", engineEntries, environment)
	}
}

func TestOfficialChromeBackendProducesValidProcessConfiguration(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	backend, err := NewOfficialChromeBackend(OfficialChromeBackendOptions{
		Assets: OfficialChromeAssets{
			Lock: OfficialChromeAssetLock{
				ChromePath:              "/opt/google/chrome/chrome",
				ChromeDistribution:      "chrome_for_testing",
				ChromeVersion:           "151.0.7922.77",
				ExtensionRoot:           "/opt/openlinker/native-chrome/extension",
				ExtensionID:             "abcdefghijklmnopabcdefghijklmnop",
				ExtensionVersion:        "1.2.3.4",
				ExtensionActivationPath: "/openlinker-runtime/index.html",
				NativeHostPath:          "/opt/openlinker/native-chrome/bin/openlinker-native-chrome-host",
				NativeHostProtocol:      "openlinker.native-chrome.v2",
				EnginePath:              "/opt/openlinker/native-chrome/bin/openlinker-native-chrome-engine",
				ProfileGeneration:       2,
			},
			ManifestSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
		BaseEnvironment: []string{
			"HOME=/browser-home",
			"TMPDIR=/browser-tmp",
			"NO_PROXY=",
			"OPENLINKER_BROWSER_EGRESS_PROXY=http://172.18.0.2:3128",
			"OPENLINKER_BROWSER_PROFILE_DIR=/browser-tmp/profiles/active",
			"OPENLINKER_BROWSER_MAX_ACTIONS_PER_ORIGIN_MINUTE=120",
			"OPENLINKER_BROWSER_MAX_NAVIGATIONS_PER_ORIGIN_MINUTE=20",
			"PLAYWRIGHT_BROWSERS_PATH=/ms-playwright",
			"LANG=C.UTF-8",
		},
		Locale:             "en-US",
		Timezone:           "UTC",
		FontContract:       "openlinker.browser.fonts.v1",
		FontManifestSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		EgressLabel:        "default",
		EgressProxy:        "http://172.18.0.2:3128",
		StoreRoot:          filepath.Join(root, "store"),
		WorkRoot:           filepath.Join(root, "work"),
		RootKeyFile:        filepath.Join(root, "key", "profile-root-key"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	process, err := backend.engine.processFactory(backend.engine.options.Process)
	if err != nil {
		t.Fatalf("%v: %#v", err, backend.engine.options.Process.Environment)
	}
	if err := process.Close(); err != nil {
		t.Fatal(err)
	}
}
