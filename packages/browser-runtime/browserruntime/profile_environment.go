//go:build !windows

package browserruntime

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserprotocol"
)

const (
	profileEnvironmentContractID = "openlinker.browser.profile-environment.v1"
	profileEnvironmentRelative   = ".openlinker/environment.v1.json"
	maxProfileEnvironmentBytes   = 4 << 10
)

type ProfileEnvironment struct {
	Evidence          browserprotocol.EnvironmentEvidence
	ProfileGeneration uint64
	// EgressLabel is operator-owned generation-binding metadata. It stays
	// inside the encrypted Profile payload and is never exposed to the Engine,
	// page, model, or a user-facing Run event.
	EgressLabel string
}

type profileEnvironmentRecord struct {
	ContractID          string `json:"contract_id"`
	ProfileGeneration   uint64 `json:"profile_generation"`
	EgressLabel         string `json:"egress_label"`
	BrowserEngine       string `json:"browser_engine"`
	BrowserDistribution string `json:"browser_distribution"`
	BrowserVersion      string `json:"browser_version"`
	BrowserLocale       string `json:"browser_locale"`
	BrowserTimezone     string `json:"browser_timezone"`
	FontContractVersion string `json:"font_contract_version"`
	FontManifestSHA256  string `json:"font_manifest_sha256"`
}

var egressLabelPattern = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,62}$`)

func (environment ProfileEnvironment) Validate() error {
	if environment.ProfileGeneration == 0 {
		return errors.New("Browser Profile generation must be positive")
	}
	if failure := environment.Evidence.Validate(); failure != nil {
		return errors.New("Browser Profile environment is invalid")
	}
	if !egressLabelPattern.MatchString(environment.EgressLabel) {
		return errors.New("Browser Profile egress label is invalid")
	}
	return nil
}

func (environment ProfileEnvironment) record() profileEnvironmentRecord {
	return profileEnvironmentRecord{
		ContractID:          profileEnvironmentContractID,
		ProfileGeneration:   environment.ProfileGeneration,
		EgressLabel:         environment.EgressLabel,
		BrowserEngine:       environment.Evidence.BrowserEngine,
		BrowserDistribution: environment.Evidence.BrowserDistribution,
		BrowserVersion:      environment.Evidence.BrowserVersion,
		BrowserLocale:       environment.Evidence.BrowserLocale,
		BrowserTimezone:     environment.Evidence.BrowserTimezone,
		FontContractVersion: environment.Evidence.FontContractVersion,
		FontManifestSHA256:  environment.Evidence.FontManifestSHA256,
	}
}

func (record profileEnvironmentRecord) environment() (ProfileEnvironment, error) {
	major, err := browserMajorVersion(record.BrowserVersion)
	if err != nil {
		return ProfileEnvironment{}, err
	}
	environment := ProfileEnvironment{
		ProfileGeneration: record.ProfileGeneration,
		EgressLabel:       record.EgressLabel,
		Evidence: browserprotocol.EnvironmentEvidence{
			BrowserEngine:       record.BrowserEngine,
			BrowserDistribution: record.BrowserDistribution,
			BrowserVersion:      record.BrowserVersion,
			BrowserMajorVersion: major,
			BrowserLocale:       record.BrowserLocale,
			BrowserTimezone:     record.BrowserTimezone,
			FontContractVersion: record.FontContractVersion,
			FontManifestSHA256:  record.FontManifestSHA256,
		},
	}
	if record.ContractID != profileEnvironmentContractID {
		return ProfileEnvironment{}, errors.New("Browser Profile environment contract is unsupported")
	}
	if err := environment.Validate(); err != nil {
		return ProfileEnvironment{}, err
	}
	return environment, nil
}

func (environment ProfileEnvironment) sameGenerationBinding(
	other ProfileEnvironment,
) bool {
	return environment.ProfileGeneration == other.ProfileGeneration &&
		environment.EgressLabel == other.EgressLabel &&
		environment.Evidence.BrowserEngine == other.Evidence.BrowserEngine &&
		environment.Evidence.BrowserDistribution == other.Evidence.BrowserDistribution &&
		environment.Evidence.BrowserLocale == other.Evidence.BrowserLocale &&
		environment.Evidence.BrowserTimezone == other.Evidence.BrowserTimezone &&
		environment.Evidence.FontContractVersion == other.Evidence.FontContractVersion &&
		environment.Evidence.FontManifestSHA256 == other.Evidence.FontManifestSHA256
}

func compareBrowserVersions(left, right string) (int, error) {
	leftParts, err := parsedBrowserVersion(left)
	if err != nil {
		return 0, err
	}
	rightParts, err := parsedBrowserVersion(right)
	if err != nil {
		return 0, err
	}
	for index := range 4 {
		switch {
		case leftParts[index] < rightParts[index]:
			return -1, nil
		case leftParts[index] > rightParts[index]:
			return 1, nil
		}
	}
	return 0, nil
}

func parsedBrowserVersion(value string) ([4]uint64, error) {
	var result [4]uint64
	parts := strings.Split(value, ".")
	if len(parts) < 2 || len(parts) > len(result) {
		return result, errors.New("Browser version is invalid")
	}
	for index, part := range parts {
		number, err := strconv.ParseUint(part, 10, 32)
		if err != nil {
			return result, errors.New("Browser version is invalid")
		}
		result[index] = number
	}
	return result, nil
}

func browserMajorVersion(value string) (int, error) {
	parts, err := parsedBrowserVersion(value)
	if err != nil || parts[0] == 0 || parts[0] > 1000 {
		return 0, errors.New("Browser major version is invalid")
	}
	return int(parts[0]), nil
}

func loadProfileEnvironment(root string) (ProfileEnvironment, error) {
	path := filepath.Join(root, profileEnvironmentRelative)
	info, err := os.Lstat(path)
	if err != nil {
		return ProfileEnvironment{}, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		info.Size() <= 0 || info.Size() > maxProfileEnvironmentBytes {
		return ProfileEnvironment{}, errors.New("Browser Profile environment file is invalid")
	}
	raw, err := os.ReadFile(path) // #nosec G304 -- fixed file inside validated Profile work root.
	if err != nil {
		return ProfileEnvironment{}, err
	}
	var record profileEnvironmentRecord
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return ProfileEnvironment{}, errors.New("Browser Profile environment payload is invalid")
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return ProfileEnvironment{}, errors.New("Browser Profile environment payload is invalid")
	}
	canonical, err := json.Marshal(record)
	if err != nil || !bytes.Equal(raw, canonical) {
		return ProfileEnvironment{}, errors.New("Browser Profile environment payload is not canonical")
	}
	return record.environment()
}

func writeProfileEnvironment(root string, environment ProfileEnvironment) error {
	if err := environment.Validate(); err != nil {
		return err
	}
	directory := filepath.Join(root, ".openlinker")
	info, err := os.Lstat(directory)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := os.Mkdir(directory, 0o700); err != nil {
			return fmt.Errorf("create Browser Profile metadata directory: %w", err)
		}
	case err != nil:
		return err
	case !info.IsDir() || info.Mode()&os.ModeSymlink != 0:
		return errors.New("Browser Profile metadata directory is invalid")
	}
	raw, err := json.Marshal(environment.record())
	if err != nil {
		return err
	}
	if len(raw) > maxProfileEnvironmentBytes {
		return errors.New("Browser Profile environment payload is too large")
	}
	temporary, err := os.CreateTemp(directory, ".environment-")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(raw); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	target := filepath.Join(directory, filepath.Base(profileEnvironmentRelative))
	if err := os.Rename(temporaryPath, target); err != nil {
		return err
	}
	handle, err := os.Open(directory) // #nosec G304 -- validated Profile metadata directory.
	if err != nil {
		return err
	}
	syncErr := handle.Sync()
	closeErr := handle.Close()
	return errors.Join(syncErr, closeErr)
}
