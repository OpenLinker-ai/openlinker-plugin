//go:build !windows

package browserruntime

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

const (
	originBudgetContractID         = "openlinker.browser.origin-budgets.v1"
	originBudgetDirectory          = ".openlinker"
	originBudgetFile               = "origin-budgets.v1.json"
	maxOriginBudgetBytes     int64 = 256 << 10
	maxOriginBudgetEntries         = 256
	maxOriginActionTimes           = 600
	maxOriginNavigationTimes       = 60
	maxJavaScriptSafeInteger       = int64(1<<53 - 1)
)

type originBudgetDocument struct {
	ContractID string              `json:"contract_id"`
	Entries    []originBudgetEntry `json:"entries"`
}

type originBudgetEntry struct {
	Key            string  `json:"key"`
	ActionMS       []int64 `json:"action_ms"`
	NavigationMS   []int64 `json:"navigation_ms"`
	BackoffUntilMS *int64  `json:"retry_after_ms,omitempty"`
	UpdatedAtMS    int64   `json:"updated_at_ms"`
}

func validateBrowserProfileState(root string) error {
	if err := validatePageContinuationState(root); err != nil {
		return err
	}
	return validateOriginBudgetState(root)
}

func validateOriginBudgetState(root string) error {
	directory := filepath.Join(root, originBudgetDirectory)
	path := filepath.Join(directory, originBudgetFile)
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return errors.New("Browser origin-budget file is unreadable")
	}
	directoryInfo, directoryErr := os.Lstat(directory)
	if directoryErr != nil ||
		!directoryInfo.IsDir() ||
		directoryInfo.Mode()&os.ModeSymlink != 0 ||
		directoryInfo.Mode().Perm()&0o077 != 0 {
		return errors.New("Browser origin-budget directory is invalid")
	}
	if !info.Mode().IsRegular() ||
		info.Mode()&os.ModeSymlink != 0 ||
		info.Mode().Perm()&0o077 != 0 ||
		info.Size() <= 0 ||
		info.Size() > maxOriginBudgetBytes {
		return errors.New("Browser origin-budget file is invalid")
	}
	raw, err := os.ReadFile(path) // #nosec G304 -- fixed path below the validated private Profile root.
	if err != nil || int64(len(raw)) > maxOriginBudgetBytes {
		return errors.New("Browser origin-budget file is unreadable")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var document originBudgetDocument
	if err := decoder.Decode(&document); err != nil {
		return errors.New("Browser origin-budget payload is invalid")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("Browser origin-budget payload has trailing data")
	}
	canonical, err := json.Marshal(document)
	if err != nil || !bytes.Equal(canonical, raw) {
		return errors.New("Browser origin-budget payload is not canonical")
	}
	if document.ContractID != originBudgetContractID ||
		len(document.Entries) > maxOriginBudgetEntries {
		return errors.New("Browser origin-budget contract is invalid")
	}
	seen := make(map[string]struct{}, len(document.Entries))
	for position, entry := range document.Entries {
		key, err := hex.DecodeString(entry.Key)
		if err != nil ||
			len(key) != 32 ||
			entry.Key != string(bytes.ToLower([]byte(entry.Key))) {
			return errors.New("Browser origin-budget key is invalid")
		}
		if _, exists := seen[entry.Key]; exists {
			return errors.New("Browser origin-budget key is duplicated")
		}
		seen[entry.Key] = struct{}{}
		if position > 0 &&
			compareOriginBudgetEntries(document.Entries[position-1], entry) >= 0 {
			return errors.New("Browser origin-budget entries are not ordered")
		}
		if !validOriginBudgetTimes(entry.ActionMS, maxOriginActionTimes) ||
			!validOriginBudgetTimes(entry.NavigationMS, maxOriginNavigationTimes) ||
			!validJavaScriptTime(entry.UpdatedAtMS) ||
			(entry.BackoffUntilMS != nil && !validJavaScriptTime(*entry.BackoffUntilMS)) {
			return errors.New("Browser origin-budget entry is invalid")
		}
		if lastOriginBudgetTime(entry.ActionMS) > entry.UpdatedAtMS ||
			lastOriginBudgetTime(entry.NavigationMS) > entry.UpdatedAtMS {
			return errors.New("Browser origin-budget update time is invalid")
		}
	}
	return nil
}

func compareOriginBudgetEntries(left, right originBudgetEntry) int {
	switch {
	case left.UpdatedAtMS < right.UpdatedAtMS:
		return -1
	case left.UpdatedAtMS > right.UpdatedAtMS:
		return 1
	case left.Key < right.Key:
		return -1
	case left.Key > right.Key:
		return 1
	default:
		return 0
	}
}

func validOriginBudgetTimes(values []int64, maximum int) bool {
	if values == nil || len(values) > maximum {
		return false
	}
	for index, value := range values {
		if !validJavaScriptTime(value) ||
			(index > 0 && value < values[index-1]) {
			return false
		}
	}
	return true
}

func validJavaScriptTime(value int64) bool {
	return value >= 0 && value <= maxJavaScriptSafeInteger
}

func lastOriginBudgetTime(values []int64) int64 {
	if len(values) == 0 {
		return 0
	}
	return values[len(values)-1]
}
