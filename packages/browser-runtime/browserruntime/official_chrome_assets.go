//go:build !windows

package browserruntime

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
)

const (
	officialChromeAssetContractID = "openlinker.native-chrome.assets.v1"
	legacyOpenAIExtensionID       = "hehggadaopoacecdllhhajmbjkdcmajg"
	openLinkerNativeHostName      = "ai.openlinker.browser"
	officialNativeHostProtocol    = "openlinker.native-chrome.v2"
	openLinkerActivationPath      = "/openlinker-runtime/index.html"
	maxOfficialChromeLockBytes    = 256 << 10
	maxOfficialChromeAssetBytes   = int64(1 << 30)
	maxOfficialChromeAssetsBytes  = int64(2 << 30)
)

var requiredOfficialChromeCapabilities = []string{
	"act",
	"back",
	"batch",
	"checkpoint",
	"click",
	"close",
	"forward",
	"full",
	"keypress",
	"navigate",
	"observe",
	"ops_observe_frame",
	"ops_observe_status",
	"policy_evidence",
	"restricted",
	"screenshot",
	"scroll",
	"select",
	"semantic",
	"type_non_secret",
	"wait",
}

type OfficialChromeAsset struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type OfficialChromeAssetLock struct {
	ContractID               string                `json:"contract_id"`
	Platform                 string                `json:"platform"`
	Architecture             string                `json:"architecture"`
	ChromePath               string                `json:"chrome_path"`
	ChromeDistribution       string                `json:"chrome_distribution"`
	ChromeVersion            string                `json:"chrome_version"`
	ExtensionRoot            string                `json:"extension_root"`
	ExtensionID              string                `json:"extension_id"`
	ExtensionVersion         string                `json:"extension_version"`
	ExtensionActivationPath  string                `json:"extension_activation_path"`
	ExtensionCRXPath         string                `json:"extension_crx_path"`
	ExtensionInstallManifest string                `json:"extension_install_manifest_path"`
	ExtensionUpdateManifest  string                `json:"extension_update_manifest_path"`
	ExtensionPolicyPath      string                `json:"extension_policy_path"`
	NativeHostPath           string                `json:"native_host_path"`
	NativeHostProtocol       string                `json:"native_host_protocol"`
	NativeMessagingManifest  string                `json:"native_messaging_manifest_path"`
	EnginePath               string                `json:"engine_path"`
	ProfileGeneration        uint64                `json:"profile_generation"`
	Capabilities             []string              `json:"capabilities"`
	Assets                   []OfficialChromeAsset `json:"assets"`
}

type OfficialChromeAssets struct {
	Lock           OfficialChromeAssetLock
	ManifestSHA256 string
}

type OfficialChromeAssetOptions struct {
	ManifestPath         string
	Platform             string
	Architecture         string
	RequireRootOwned     bool
	ExtensionInstallRoot string
	ExtensionPolicyPath  string
	NativeMessagingPath  string
}

func LoadOfficialChromeAssets(
	options OfficialChromeAssetOptions,
) (OfficialChromeAssets, string) {
	path := filepath.Clean(strings.TrimSpace(options.ManifestPath))
	if !filepath.IsAbs(path) {
		return OfficialChromeAssets{}, "official_assets_unavailable"
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return OfficialChromeAssets{}, "official_assets_unavailable"
	}
	if err != nil || !immutableOfficialChromeFile(info, options.RequireRootOwned) ||
		!immutableOfficialChromePath(path, options.RequireRootOwned) ||
		info.Size() <= 0 || info.Size() > maxOfficialChromeLockBytes {
		return OfficialChromeAssets{}, "official_assets_invalid"
	}
	raw, err := os.ReadFile(path) // #nosec G304 -- operator-selected absolute lock path is validated above.
	if err != nil || len(raw) > maxOfficialChromeLockBytes {
		return OfficialChromeAssets{}, "official_assets_invalid"
	}
	var lock OfficialChromeAssetLock
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&lock); err != nil {
		return OfficialChromeAssets{}, "official_assets_invalid"
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return OfficialChromeAssets{}, "official_assets_invalid"
	}
	canonical, err := json.Marshal(lock)
	if err != nil || !bytes.Equal(raw, canonical) {
		return OfficialChromeAssets{}, "official_assets_invalid"
	}
	platform := strings.TrimSpace(options.Platform)
	if platform == "" {
		platform = runtime.GOOS
	}
	architecture := strings.TrimSpace(options.Architecture)
	if architecture == "" {
		architecture = runtime.GOARCH
	}
	if lock.ContractID != officialChromeAssetContractID ||
		lock.Platform != "linux" || lock.Platform != platform ||
		lock.Architecture == "" || lock.Architecture != architecture {
		return OfficialChromeAssets{}, "official_platform_unsupported"
	}
	if !validOfficialChromeLock(lock) {
		return OfficialChromeAssets{}, "official_assets_invalid"
	}
	seen := make(map[string]struct{}, len(lock.Assets))
	var totalAssetBytes int64
	for _, asset := range lock.Assets {
		if _, duplicate := seen[asset.Path]; duplicate {
			return OfficialChromeAssets{}, "official_assets_invalid"
		}
		seen[asset.Path] = struct{}{}
		assetInfo, assetErr := os.Lstat(asset.Path)
		if assetErr != nil || assetInfo.Size() < 0 ||
			assetInfo.Size() > maxOfficialChromeAssetBytes ||
			totalAssetBytes > maxOfficialChromeAssetsBytes-assetInfo.Size() {
			return OfficialChromeAssets{}, "official_assets_invalid"
		}
		totalAssetBytes += assetInfo.Size()
		if !verifyOfficialChromeAsset(asset, options.RequireRootOwned) {
			return OfficialChromeAssets{}, "official_assets_invalid"
		}
	}
	chromeSandboxPath := filepath.Join(filepath.Dir(lock.ChromePath), "chrome-sandbox")
	activationPath := strings.SplitN(lock.ExtensionActivationPath, "?", 2)[0]
	activationFile := filepath.Join(
		lock.ExtensionRoot,
		strings.TrimPrefix(activationPath, "/"),
	)
	for _, required := range []string{
		lock.ChromePath,
		chromeSandboxPath,
		lock.ExtensionCRXPath,
		lock.ExtensionInstallManifest,
		lock.ExtensionUpdateManifest,
		lock.ExtensionPolicyPath,
		lock.NativeHostPath,
		lock.NativeMessagingManifest,
		lock.EnginePath,
		activationFile,
	} {
		if _, ok := seen[required]; !ok {
			return OfficialChromeAssets{}, "official_assets_invalid"
		}
	}
	for _, executable := range []string{
		lock.ChromePath,
		lock.NativeHostPath,
		lock.EnginePath,
	} {
		info, err := os.Lstat(executable)
		if err != nil || info.Mode().Perm()&0o111 == 0 {
			return OfficialChromeAssets{}, "official_assets_invalid"
		}
	}
	sandboxInfo, err := os.Lstat(chromeSandboxPath)
	if err != nil || sandboxInfo.Mode().Perm()&0o111 == 0 ||
		sandboxInfo.Mode()&os.ModeSetuid == 0 {
		return OfficialChromeAssets{}, "official_assets_invalid"
	}
	extensionManifest := filepath.Join(lock.ExtensionRoot, "manifest.json")
	if _, ok := seen[extensionManifest]; !ok {
		return OfficialChromeAssets{}, "official_assets_invalid"
	}
	if !validNativeMessagingManifest(lock) {
		return OfficialChromeAssets{}, "official_assets_invalid"
	}
	installRoot := strings.TrimSpace(options.ExtensionInstallRoot)
	if installRoot == "" {
		installRoot = officialChromeExtensionInstallRoot(lock.ChromeDistribution)
	}
	policyPath := strings.TrimSpace(options.ExtensionPolicyPath)
	if policyPath == "" {
		policyPath = officialChromePolicyPath(lock.ChromeDistribution)
	}
	nativeMessagingPath := strings.TrimSpace(options.NativeMessagingPath)
	if nativeMessagingPath == "" {
		nativeMessagingPath = officialChromeNativeMessagingPath(lock.ChromeDistribution)
	}
	if !filepath.IsAbs(installRoot) || filepath.Clean(installRoot) != installRoot ||
		!filepath.IsAbs(policyPath) || filepath.Clean(policyPath) != policyPath ||
		!filepath.IsAbs(nativeMessagingPath) ||
		filepath.Clean(nativeMessagingPath) != nativeMessagingPath ||
		lock.NativeMessagingManifest != nativeMessagingPath {
		return OfficialChromeAssets{}, "official_assets_invalid"
	}
	if !validExtensionInstallation(lock, installRoot, policyPath) {
		return OfficialChromeAssets{}, "official_assets_invalid"
	}
	manifestDigest := sha256.Sum256(raw)
	return OfficialChromeAssets{
		Lock:           lock,
		ManifestSHA256: hex.EncodeToString(manifestDigest[:]),
	}, ""
}

func validOfficialChromeLock(lock OfficialChromeAssetLock) bool {
	for _, path := range []string{
		lock.ChromePath,
		lock.ExtensionRoot,
		lock.ExtensionCRXPath,
		lock.ExtensionInstallManifest,
		lock.ExtensionUpdateManifest,
		lock.ExtensionPolicyPath,
		lock.NativeHostPath,
		lock.NativeMessagingManifest,
		lock.EnginePath,
	} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return false
		}
	}
	if lock.ProfileGeneration == 0 ||
		(lock.ChromeDistribution != "google_chrome" &&
			lock.ChromeDistribution != "chrome_for_testing") ||
		!validLockedVersion(lock.ChromeVersion) ||
		!validLockedVersion(lock.ExtensionVersion) ||
		!validExtensionID(lock.ExtensionID) ||
		lock.ExtensionID == legacyOpenAIExtensionID ||
		!validExtensionActivationPath(lock.ExtensionActivationPath) ||
		lock.ExtensionActivationPath != openLinkerActivationPath ||
		lock.NativeHostProtocol != officialNativeHostProtocol ||
		len(lock.Assets) < 7 || len(lock.Assets) > 4096 {
		return false
	}
	if len(lock.Capabilities) != len(requiredOfficialChromeCapabilities) ||
		!sort.StringsAreSorted(lock.Capabilities) {
		return false
	}
	for index, capability := range requiredOfficialChromeCapabilities {
		if lock.Capabilities[index] != capability {
			return false
		}
	}
	return true
}

func officialChromeExtensionInstallRoot(distribution string) string {
	if distribution == "chrome_for_testing" {
		return "/usr/share/chromium/extensions"
	}
	return "/opt/google/chrome/extensions"
}

func officialChromePolicyPath(distribution string) string {
	if distribution == "chrome_for_testing" {
		return "/etc/opt/chrome_for_testing/policies/managed/openlinker-native-chrome.json"
	}
	return "/etc/opt/chrome/policies/managed/openlinker-native-chrome.json"
}

func officialChromeNativeMessagingPath(distribution string) string {
	if distribution == "chrome_for_testing" {
		return "/etc/opt/chrome_for_testing/native-messaging-hosts/ai.openlinker.browser.json"
	}
	return "/etc/opt/chrome/native-messaging-hosts/ai.openlinker.browser.json"
}

type officialExtensionInstallManifest struct {
	ExternalCRX     string `json:"external_crx"`
	ExternalVersion string `json:"external_version"`
}

type officialExtensionPolicyEntry struct {
	InstallationMode  string `json:"installation_mode"`
	OverrideUpdateURL bool   `json:"override_update_url,omitempty"`
	UpdateURL         string `json:"update_url,omitempty"`
}

type officialExtensionPolicy struct {
	ExtensionSettings map[string]officialExtensionPolicyEntry `json:"ExtensionSettings"`
}

func validExtensionInstallation(
	lock OfficialChromeAssetLock,
	installRoot,
	policyPath string,
) bool {
	if lock.ExtensionCRXPath != filepath.Join(lock.ExtensionRoot, "extension.crx") ||
		lock.ExtensionInstallManifest != filepath.Join(installRoot, lock.ExtensionID+".json") ||
		lock.ExtensionUpdateManifest != filepath.Join(
			filepath.Dir(lock.ExtensionRoot),
			"extension-update.xml",
		) ||
		lock.ExtensionPolicyPath != policyPath {
		return false
	}
	crxInfo, err := os.Lstat(lock.ExtensionCRXPath)
	if err != nil || !crxInfo.Mode().IsRegular() || crxInfo.Size() < 16 ||
		crxInfo.Size() > 512<<20 {
		return false
	}
	crx, err := os.Open(lock.ExtensionCRXPath) // #nosec G304 -- path is covered by the verified asset lock.
	if err != nil {
		return false
	}
	header := make([]byte, 4)
	_, readErr := io.ReadFull(crx, header)
	closeErr := crx.Close()
	if readErr != nil || closeErr != nil || !bytes.Equal(header, []byte("Cr24")) {
		return false
	}
	var install officialExtensionInstallManifest
	if !readCanonicalOfficialChromeJSON(lock.ExtensionInstallManifest, &install, 16<<10) ||
		install.ExternalCRX != lock.ExtensionCRXPath ||
		install.ExternalVersion != lock.ExtensionVersion {
		return false
	}
	crxURL := officialChromeFileURL(lock.ExtensionCRXPath)
	expectedUpdateManifest := `<?xml version="1.0" encoding="UTF-8"?>` +
		`<gupdate xmlns="http://www.google.com/update2/response" protocol="2.0">` +
		`<app appid="` + lock.ExtensionID + `">` +
		`<updatecheck codebase="` + crxURL + `" version="` + lock.ExtensionVersion + `"/>` +
		`</app></gupdate>`
	updateManifest, err := os.ReadFile(lock.ExtensionUpdateManifest) // #nosec G304 -- path is covered by the verified asset lock.
	if err != nil || len(updateManifest) > 16<<10 ||
		!bytes.Equal(updateManifest, []byte(expectedUpdateManifest)) {
		return false
	}
	var policy officialExtensionPolicy
	if !readCanonicalOfficialChromeJSON(lock.ExtensionPolicyPath, &policy, 16<<10) ||
		len(policy.ExtensionSettings) != 2 ||
		policy.ExtensionSettings["*"] != (officialExtensionPolicyEntry{
			InstallationMode: "blocked",
		}) ||
		policy.ExtensionSettings[lock.ExtensionID] != (officialExtensionPolicyEntry{
			InstallationMode: "allowed",
		}) {
		return false
	}
	return true
}

func officialChromeFileURL(path string) string {
	return (&url.URL{Scheme: "file", Path: path}).String()
}

func readCanonicalOfficialChromeJSON(path string, target any, maximum int) bool {
	raw, err := os.ReadFile(path) // #nosec G304 -- path is covered by the verified asset lock.
	if err != nil || len(raw) == 0 || len(raw) > maximum {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return false
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return false
	}
	canonical, err := json.Marshal(target)
	return err == nil && bytes.Equal(raw, canonical)
}

type officialNativeMessagingManifest struct {
	Name           string   `json:"name"`
	Description    string   `json:"description"`
	Path           string   `json:"path"`
	Type           string   `json:"type"`
	AllowedOrigins []string `json:"allowed_origins"`
}

func validNativeMessagingManifest(lock OfficialChromeAssetLock) bool {
	raw, err := os.ReadFile(lock.NativeMessagingManifest) // #nosec G304 -- path is covered by the verified asset lock.
	if err != nil || len(raw) == 0 || len(raw) > 16<<10 {
		return false
	}
	var manifest officialNativeMessagingManifest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return false
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return false
	}
	canonical, err := json.Marshal(manifest)
	return err == nil && bytes.Equal(raw, canonical) &&
		manifest.Name == openLinkerNativeHostName &&
		manifest.Description != "" && len(manifest.Description) <= 256 &&
		manifest.Path == lock.NativeHostPath && manifest.Type == "stdio" &&
		len(manifest.AllowedOrigins) == 1 &&
		manifest.AllowedOrigins[0] == "chrome-extension://"+lock.ExtensionID+"/"
}

func validExtensionActivationPath(value string) bool {
	return strings.HasPrefix(value, "/") && len(value) <= 512 &&
		!strings.Contains(value, "..") && !strings.ContainsAny(value, "\x00\r\n")
}

func verifyOfficialChromeAsset(
	asset OfficialChromeAsset,
	requireRootOwned bool,
) bool {
	if !filepath.IsAbs(asset.Path) || filepath.Clean(asset.Path) != asset.Path ||
		len(asset.SHA256) != 64 || strings.ToLower(asset.SHA256) != asset.SHA256 {
		return false
	}
	if _, err := hex.DecodeString(asset.SHA256); err != nil {
		return false
	}
	info, err := os.Lstat(asset.Path)
	if err != nil || !immutableOfficialChromeFile(info, requireRootOwned) ||
		!immutableOfficialChromePath(asset.Path, requireRootOwned) {
		return false
	}
	file, err := os.Open(asset.Path) // #nosec G304 -- absolute path is locked by the canonical manifest.
	if err != nil {
		return false
	}
	digest := sha256.New()
	_, copyErr := io.Copy(digest, file)
	closeErr := file.Close()
	return copyErr == nil && closeErr == nil &&
		hex.EncodeToString(digest.Sum(nil)) == asset.SHA256
}

func immutableOfficialChromePath(path string, requireRootOwned bool) bool {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return false
	}
	if !requireRootOwned {
		return true
	}
	for directory := filepath.Dir(path); ; directory = filepath.Dir(directory) {
		info, err := os.Lstat(directory)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() ||
			info.Mode().Perm()&0o022 != 0 {
			return false
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != 0 || stat.Gid != 0 {
			return false
		}
		if directory == filepath.Dir(directory) {
			return true
		}
	}
}

func immutableOfficialChromeFile(info os.FileInfo, requireRootOwned bool) bool {
	if info == nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		info.Mode().Perm()&0o022 != 0 {
		return false
	}
	if !requireRootOwned {
		return true
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == 0 && stat.Gid == 0
}

func validLockedVersion(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	parts := strings.Split(value, ".")
	if len(parts) < 2 || len(parts) > 4 {
		return false
	}
	for _, part := range parts {
		if part == "" || len(part) > 8 {
			return false
		}
		for _, character := range part {
			if character < '0' || character > '9' {
				return false
			}
		}
	}
	return true
}

func validExtensionID(value string) bool {
	if len(value) != 32 {
		return false
	}
	for _, character := range value {
		if character < 'a' || character > 'p' {
			return false
		}
	}
	return true
}

func validBoundedOpaque(value string, maximum int) bool {
	if value == "" || len(value) > maximum {
		return false
	}
	for _, character := range value {
		if !(character == '.' || character == '-' || character == '_' ||
			(character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9')) {
			return false
		}
	}
	return true
}

func officialChromeAssetError(reason string) error {
	return fmt.Errorf("official Chrome assets are unavailable (%s)", reason)
}
