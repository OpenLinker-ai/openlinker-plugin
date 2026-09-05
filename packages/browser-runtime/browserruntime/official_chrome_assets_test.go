//go:build !windows

package browserruntime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

const openLinkerTestExtensionID = "abcdefghijklmnopabcdefghijklmnop"

func TestOfficialChromeAssetsRequireCanonicalLockedCompleteImageFiles(
	t *testing.T,
) {
	root := t.TempDir()
	lockPath := writeOfficialChromeAssetFixture(t, root, "linux", "amd64")
	assets, reason := LoadOfficialChromeAssets(OfficialChromeAssetOptions{
		ManifestPath:         lockPath,
		Platform:             "linux",
		Architecture:         "amd64",
		ExtensionInstallRoot: filepath.Join(root, "usr", "share", "chromium", "extensions"),
		ExtensionPolicyPath:  filepath.Join(root, "etc", "opt", "chrome_for_testing", "policies", "managed", "openlinker-native-chrome.json"),
		NativeMessagingPath:  filepath.Join(root, "etc", "opt", "chrome_for_testing", "native-messaging-hosts", "ai.openlinker.browser.json"),
	})
	if reason != "" {
		t.Fatalf("valid assets unavailable: %s", reason)
	}
	if assets.Lock.ExtensionID != openLinkerTestExtensionID ||
		len(assets.ManifestSHA256) != 64 {
		t.Fatalf("assets = %#v", assets)
	}

	if err := os.Chmod(assets.Lock.ChromePath, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(assets.Lock.ChromePath, []byte("tampered"), 0o555); err != nil {
		t.Fatal(err)
	}
	if _, reason := LoadOfficialChromeAssets(OfficialChromeAssetOptions{
		ManifestPath:         lockPath,
		Platform:             "linux",
		Architecture:         "amd64",
		ExtensionInstallRoot: filepath.Join(root, "usr", "share", "chromium", "extensions"),
		ExtensionPolicyPath:  filepath.Join(root, "etc", "opt", "chrome_for_testing", "policies", "managed", "openlinker-native-chrome.json"),
		NativeMessagingPath:  filepath.Join(root, "etc", "opt", "chrome_for_testing", "native-messaging-hosts", "ai.openlinker.browser.json"),
	}); reason != "official_assets_invalid" {
		t.Fatalf("tampered asset reason = %q", reason)
	}
}

func TestOfficialChromeAssetsRejectUnsupportedPlatformAndNonCanonicalLock(
	t *testing.T,
) {
	root := t.TempDir()
	lockPath := writeOfficialChromeAssetFixture(t, root, "linux", "amd64")
	if _, reason := LoadOfficialChromeAssets(OfficialChromeAssetOptions{
		ManifestPath:         lockPath,
		Platform:             "linux",
		Architecture:         "arm64",
		ExtensionInstallRoot: filepath.Join(root, "usr", "share", "chromium", "extensions"),
		ExtensionPolicyPath:  filepath.Join(root, "etc", "opt", "chrome_for_testing", "policies", "managed", "openlinker-native-chrome.json"),
		NativeMessagingPath:  filepath.Join(root, "etc", "opt", "chrome_for_testing", "native-messaging-hosts", "ai.openlinker.browser.json"),
	}); reason != "official_platform_unsupported" {
		t.Fatalf("unsupported platform reason = %q", reason)
	}
	raw, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	pretty := append([]byte("\n"), raw...)
	if err := os.Chmod(lockPath, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lockPath, pretty, 0o444); err != nil {
		t.Fatal(err)
	}
	if _, reason := LoadOfficialChromeAssets(OfficialChromeAssetOptions{
		ManifestPath:         lockPath,
		Platform:             "linux",
		Architecture:         "amd64",
		ExtensionInstallRoot: filepath.Join(root, "usr", "share", "chromium", "extensions"),
		ExtensionPolicyPath:  filepath.Join(root, "etc", "opt", "chrome_for_testing", "policies", "managed", "openlinker-native-chrome.json"),
		NativeMessagingPath:  filepath.Join(root, "etc", "opt", "chrome_for_testing", "native-messaging-hosts", "ai.openlinker.browser.json"),
	}); reason != "official_assets_invalid" {
		t.Fatalf("non-canonical lock reason = %q", reason)
	}
}

func writeOfficialChromeAssetFixture(
	t *testing.T,
	root,
	platform,
	architecture string,
) string {
	t.Helper()
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	root = resolvedRoot
	extensionRoot := filepath.Join(root, "extension")
	if err := os.Mkdir(extensionRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		filepath.Join(root, "chrome"):                                    []byte("chrome-binary"),
		filepath.Join(root, "chrome-sandbox"):                            []byte("chrome-sandbox"),
		filepath.Join(extensionRoot, "manifest.json"):                    []byte(`{"name":"Locked extension"}`),
		filepath.Join(extensionRoot, "openlinker-runtime", "index.html"): []byte("activation"),
		filepath.Join(extensionRoot, "extension.crx"):                    append([]byte("Cr24"), make([]byte, 12)...),
		filepath.Join(root, "native-host"):                               []byte("native-host-binary"),
		filepath.Join(root, "native-engine"):                             []byte("native-engine-binary"),
	}
	if err := os.Mkdir(filepath.Join(extensionRoot, "openlinker-runtime"), 0o755); err != nil {
		t.Fatal(err)
	}
	nativeManifestRoot := filepath.Join(root, "etc", "opt", "chrome_for_testing", "native-messaging-hosts")
	if err := os.MkdirAll(nativeManifestRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	nativeManifestPath := filepath.Join(nativeManifestRoot, "ai.openlinker.browser.json")
	nativeManifest := officialNativeMessagingManifest{
		Name:           openLinkerNativeHostName,
		Description:    "Locked test Native Host",
		Path:           filepath.Join(root, "native-host"),
		Type:           "stdio",
		AllowedOrigins: []string{"chrome-extension://" + openLinkerTestExtensionID + "/"},
	}
	nativeManifestRaw, err := json.Marshal(nativeManifest)
	if err != nil {
		t.Fatal(err)
	}
	files[nativeManifestPath] = nativeManifestRaw
	installRoot := filepath.Join(root, "usr", "share", "chromium", "extensions")
	policyRoot := filepath.Join(root, "etc", "opt", "chrome_for_testing", "policies", "managed")
	if err := os.MkdirAll(installRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(policyRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	installPath := filepath.Join(installRoot, openLinkerTestExtensionID+".json")
	installRaw, err := json.Marshal(officialExtensionInstallManifest{
		ExternalCRX:     filepath.Join(extensionRoot, "extension.crx"),
		ExternalVersion: "1.2.3.4",
	})
	if err != nil {
		t.Fatal(err)
	}
	files[installPath] = installRaw
	updatePath := filepath.Join(root, "extension-update.xml")
	files[updatePath] = []byte(
		`<?xml version="1.0" encoding="UTF-8"?>` +
			`<gupdate xmlns="http://www.google.com/update2/response" protocol="2.0">` +
			`<app appid="` + openLinkerTestExtensionID + `">` +
			`<updatecheck codebase="` + officialChromeFileURL(filepath.Join(extensionRoot, "extension.crx")) + `" version="1.2.3.4"/>` +
			`</app></gupdate>`,
	)
	policyPath := filepath.Join(policyRoot, "openlinker-native-chrome.json")
	policyRaw, err := json.Marshal(officialExtensionPolicy{
		ExtensionSettings: map[string]officialExtensionPolicyEntry{
			"*": {InstallationMode: "blocked"},
			openLinkerTestExtensionID: {
				InstallationMode: "allowed",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	files[policyPath] = policyRaw
	assets := make([]OfficialChromeAsset, 0, len(files))
	for path, content := range files {
		if err := os.WriteFile(path, content, 0o555); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(content)
		assets = append(assets, OfficialChromeAsset{
			Path:   path,
			SHA256: hex.EncodeToString(digest[:]),
		})
	}
	if err := os.Chmod(filepath.Join(root, "chrome-sandbox"), 0o4755); err != nil {
		t.Fatal(err)
	}
	for index := range assets {
		if assets[index].Path == filepath.Join(root, "chrome-sandbox") {
			info, err := os.Lstat(assets[index].Path)
			if err != nil || info.Mode()&os.ModeSetuid == 0 {
				t.Skip("test filesystem does not preserve the setuid mode bit")
			}
		}
	}
	for left := 0; left < len(assets); left++ {
		for right := left + 1; right < len(assets); right++ {
			if assets[right].Path < assets[left].Path {
				assets[left], assets[right] = assets[right], assets[left]
			}
		}
	}
	lock := OfficialChromeAssetLock{
		ContractID:               officialChromeAssetContractID,
		Platform:                 platform,
		Architecture:             architecture,
		ChromePath:               filepath.Join(root, "chrome"),
		ChromeDistribution:       "chrome_for_testing",
		ChromeVersion:            "150.0.1.2",
		ExtensionRoot:            extensionRoot,
		ExtensionID:              openLinkerTestExtensionID,
		ExtensionVersion:         "1.2.3.4",
		ExtensionActivationPath:  openLinkerActivationPath,
		ExtensionCRXPath:         filepath.Join(extensionRoot, "extension.crx"),
		ExtensionInstallManifest: installPath,
		ExtensionUpdateManifest:  updatePath,
		ExtensionPolicyPath:      policyPath,
		NativeHostPath:           filepath.Join(root, "native-host"),
		NativeHostProtocol:       "openlinker.native-chrome.v2",
		NativeMessagingManifest:  nativeManifestPath,
		EnginePath:               filepath.Join(root, "native-engine"),
		ProfileGeneration:        1,
		Capabilities:             append([]string(nil), requiredOfficialChromeCapabilities...),
		Assets:                   assets,
	}
	raw, err := json.Marshal(lock)
	if err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(root, "assets.lock.json")
	if err := os.WriteFile(lockPath, raw, 0o444); err != nil {
		t.Fatal(err)
	}
	return lockPath
}
