//go:build !windows

package browserruntime

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/netpolicy"
)

const (
	pageContinuationContractID = "openlinker.browser.page-continuation.v1"
	pageContinuationDirectory  = ".openlinker"
	pageContinuationFile       = "page-continuations.v1.json"
	maxPageContinuationBytes   = int64(512 << 10)
	maxPageContinuationEntries = 32
	maxPageContinuationURL     = 8192
)

type pageContinuationIndex struct {
	ContractID string                  `json:"contract_id"`
	Entries    []pageContinuationEntry `json:"entries"`
}

type pageContinuationEntry struct {
	SessionKey string `json:"session_key"`
	URL        string `json:"url"`
	UpdatedAt  string `json:"updated_at"`
}

func validatePageContinuationState(root string) error {
	path := filepath.Join(root, pageContinuationDirectory, pageContinuationFile)
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil ||
		!info.Mode().IsRegular() ||
		info.Mode()&os.ModeSymlink != 0 ||
		info.Mode().Perm()&0o077 != 0 ||
		info.Size() <= 0 ||
		info.Size() > maxPageContinuationBytes {
		return errors.New("Browser page continuation file is invalid")
	}
	raw, err := os.ReadFile(path) // #nosec G304 -- fixed path below the private Profile work root.
	if err != nil || int64(len(raw)) > maxPageContinuationBytes {
		return errors.New("Browser page continuation file is unreadable")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var index pageContinuationIndex
	if err := decoder.Decode(&index); err != nil {
		return errors.New("Browser page continuation payload is invalid")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("Browser page continuation payload has trailing data")
	}
	if index.ContractID != pageContinuationContractID ||
		len(index.Entries) > maxPageContinuationEntries {
		return errors.New("Browser page continuation contract is invalid")
	}
	seen := make(map[string]struct{}, len(index.Entries))
	for _, entry := range index.Entries {
		key, err := hex.DecodeString(entry.SessionKey)
		if err != nil || len(key) != 32 {
			return errors.New("Browser page continuation Session key is invalid")
		}
		if _, exists := seen[entry.SessionKey]; exists {
			return errors.New("Browser page continuation Session key is duplicated")
		}
		seen[entry.SessionKey] = struct{}{}
		if err := validatePageContinuationURL(entry.URL); err != nil {
			return err
		}
		updatedAt, err := time.Parse(time.RFC3339Nano, entry.UpdatedAt)
		if err != nil || updatedAt.Location() != time.UTC {
			return errors.New("Browser page continuation timestamp is invalid")
		}
	}
	return nil
}

func validatePageContinuationURL(raw string) error {
	if raw == "" || len([]byte(raw)) > maxPageContinuationURL {
		return errors.New("Browser page continuation URL is invalid")
	}
	normalized, err := normalizePageContinuationURL(raw)
	if err != nil || normalized != raw {
		return errors.New("Browser page continuation URL is invalid")
	}
	return nil
}

func normalizePageContinuationURL(raw string) (string, error) {
	if raw == "" || len([]byte(raw)) > maxPageContinuationURL {
		return "", errors.New("Browser page continuation URL is invalid")
	}
	parsed, err := url.Parse(raw)
	host := strings.ToLower(parsedHostname(parsed))
	if err != nil ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.User != nil ||
		host == "" ||
		netpolicy.LooksLikeIPLiteral(host) ||
		!strings.Contains(host, ".") ||
		strings.HasSuffix(host, ".local") ||
		strings.HasSuffix(host, ".internal") {
		return "", errors.New("Browser page continuation URL is invalid")
	}
	parsed.Fragment = ""
	parsed.Host = strings.ToLower(parsed.Host)
	normalized := parsed.String()
	if len([]byte(normalized)) > maxPageContinuationURL {
		return "", errors.New("Browser page continuation URL is invalid")
	}
	return normalized, nil
}

func parsedHostname(parsed *url.URL) string {
	if parsed == nil {
		return ""
	}
	return parsed.Hostname()
}
