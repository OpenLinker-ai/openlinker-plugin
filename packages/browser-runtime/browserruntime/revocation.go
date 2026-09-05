//go:build !windows

package browserruntime

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserclient"
	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserprotocol"
)

const (
	revocationContractID = "openlinker.browser.revocations.v1"
	revocationFileName   = "revoked-leases.v1.json"
	maxRevocations       = 256
	maxRevocationBytes   = 64 << 10
	revocationRetention  = 30 * 24 * time.Hour
)

type revocationEntry struct {
	IdentityHash string `json:"identity_hash"`
	RevokedAt    string `json:"revoked_at"`
	ExpiresAt    string `json:"expires_at,omitempty"`
}

type revocationIndex struct {
	ContractID string            `json:"contract_id"`
	Entries    []revocationEntry `json:"entries"`
}

func (lease FileLease) Revoke(identity browserprotocol.Identity) *browserprotocol.Failure {
	revoked, failure := lease.IsRevoked(identity)
	if failure != nil || revoked {
		return failure
	}
	path, err := lease.revocationPath()
	if err != nil {
		return browserprotocol.NewFailure(
			browserprotocol.ErrorRuntimeUnavailable,
			"browser runtime revocation path is invalid",
			false,
		)
	}
	unlock, err := browserclient.LockAuthority(filepath.Dir(path))
	if err != nil {
		return browserprotocol.NewFailure(
			browserprotocol.ErrorRuntimeUnavailable,
			"browser lease authority lock is unavailable",
			true,
		)
	}
	defer unlock()
	revoked, failure = lease.IsRevoked(identity)
	if failure != nil || revoked {
		return failure
	}
	now := lease.Now
	if now == nil {
		now = time.Now
	}
	current := now().UTC()
	active, failure := lease.activeAt(identity, current)
	if failure != nil {
		return failure
	}
	index, err := lease.readRevocations(current)
	if err != nil {
		return browserprotocol.NewFailure(
			browserprotocol.ErrorRuntimeUnavailable,
			"browser runtime revocation state is invalid",
			false,
		)
	}
	if len(index.Entries) >= maxRevocations {
		oldest := 0
		for position := 1; position < len(index.Entries); position++ {
			if olderRevocation(index.Entries[position], index.Entries[oldest]) {
				oldest = position
			}
		}
		index.Entries = append(index.Entries[:oldest], index.Entries[oldest+1:]...)
	}
	hash := browserIdentityHash(identity)
	index.Entries = append(index.Entries, revocationEntry{
		IdentityHash: hash,
		RevokedAt:    current.Format(time.RFC3339Nano),
		ExpiresAt:    active.ExpiresAt.UTC().Format(time.RFC3339Nano),
	})
	sort.Slice(index.Entries, func(left, right int) bool {
		if index.Entries[left].RevokedAt == index.Entries[right].RevokedAt {
			return index.Entries[left].IdentityHash < index.Entries[right].IdentityHash
		}
		return index.Entries[left].RevokedAt > index.Entries[right].RevokedAt
	})
	if err := lease.writeRevocations(index); err != nil {
		return browserprotocol.NewFailure(
			browserprotocol.ErrorRuntimeUnavailable,
			"browser attachment closure could not be persisted",
			true,
		)
	}
	if err := os.Remove(filepath.Clean(lease.Path)); err != nil &&
		!errors.Is(err, os.ErrNotExist) {
		return browserprotocol.NewFailure(
			browserprotocol.ErrorRuntimeUnavailable,
			"browser active lease could not be revoked",
			true,
		)
	}
	return nil
}

func olderRevocation(left revocationEntry, right revocationEntry) bool {
	if left.RevokedAt == right.RevokedAt {
		return left.IdentityHash < right.IdentityHash
	}
	return left.RevokedAt < right.RevokedAt
}

func (lease FileLease) isRevoked(
	identity browserprotocol.Identity,
) (bool, error) {
	now := lease.Now
	if now == nil {
		now = time.Now
	}
	index, err := lease.readRevocations(now().UTC())
	if err != nil {
		return false, err
	}
	hash := browserIdentityHash(identity)
	for _, entry := range index.Entries {
		if entry.IdentityHash == hash {
			return true, nil
		}
	}
	return false, nil
}

func (lease FileLease) revocationPath() (string, error) {
	activePath := filepath.Clean(strings.TrimSpace(lease.Path))
	if !filepath.IsAbs(activePath) {
		return "", errors.New("active lease path is invalid")
	}
	return filepath.Join(filepath.Dir(activePath), revocationFileName), nil
}

func (lease FileLease) readRevocations(now time.Time) (revocationIndex, error) {
	index := revocationIndex{
		ContractID: revocationContractID,
		Entries:    []revocationEntry{},
	}
	path, err := lease.revocationPath()
	if err != nil {
		return revocationIndex{}, err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return index, nil
	}
	if err != nil {
		return revocationIndex{}, err
	}
	stat, owned := info.Sys().(*syscall.Stat_t)
	if info.Mode()&os.ModeSymlink != 0 ||
		!info.Mode().IsRegular() ||
		info.Mode().Perm()&0o077 != 0 ||
		!owned ||
		int(stat.Uid) != os.Geteuid() ||
		info.Size() <= 0 ||
		info.Size() > maxRevocationBytes {
		return revocationIndex{}, errors.New("revocation file is invalid")
	}
	raw, err := os.ReadFile(path) // #nosec G304 -- derived from the validated active lease path.
	if err != nil || len(raw) > maxRevocationBytes {
		return revocationIndex{}, errors.New("revocation file is unreadable")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&index); err != nil {
		return revocationIndex{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return revocationIndex{}, errors.New("revocation file has trailing data")
	}
	if index.ContractID != revocationContractID ||
		len(index.Entries) > maxRevocations {
		return revocationIndex{}, errors.New("revocation contract is invalid")
	}
	oldest := now.Add(-revocationRetention)
	seen := make(map[string]struct{}, len(index.Entries))
	retained := index.Entries[:0]
	for _, entry := range index.Entries {
		revokedAt, err := time.Parse(time.RFC3339Nano, entry.RevokedAt)
		if err != nil ||
			len(entry.IdentityHash) != sha256.Size*2 {
			return revocationIndex{}, errors.New("revocation entry is invalid")
		}
		if _, err := hex.DecodeString(entry.IdentityHash); err != nil {
			return revocationIndex{}, errors.New("revocation identity hash is invalid")
		}
		if _, duplicate := seen[entry.IdentityHash]; duplicate {
			return revocationIndex{}, errors.New("revocation identity is duplicated")
		}
		seen[entry.IdentityHash] = struct{}{}
		retain := !revokedAt.Before(oldest)
		if entry.ExpiresAt != "" {
			expiresAt, parseErr := time.Parse(time.RFC3339Nano, entry.ExpiresAt)
			if parseErr != nil || expiresAt.Before(revokedAt) {
				return revocationIndex{}, errors.New("revocation expiry is invalid")
			}
			retain = expiresAt.After(now)
		}
		if retain {
			retained = append(retained, entry)
		}
	}
	index.Entries = retained
	return index, nil
}

func (lease FileLease) writeRevocations(index revocationIndex) error {
	path, err := lease.revocationPath()
	if err != nil {
		return err
	}
	directory := filepath.Dir(path)
	info, err := os.Lstat(directory)
	if err != nil {
		return errors.New("revocation directory is invalid")
	}
	stat, owned := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 ||
		info.Mode().Perm()&0o077 != 0 || !owned || int(stat.Uid) != os.Geteuid() {
		return errors.New("revocation directory is invalid")
	}
	if existing, statErr := os.Lstat(path); statErr == nil {
		if existing.Mode()&os.ModeSymlink != 0 ||
			!existing.Mode().IsRegular() ||
			existing.Mode().Perm()&0o077 != 0 {
			return errors.New("revocation destination is invalid")
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	raw, err := json.Marshal(index)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if len(raw) > maxRevocationBytes {
		return errors.New("revocation state exceeds its limit")
	}
	temporary, err := os.CreateTemp(directory, ".revocations-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	keep := false
	defer func() {
		_ = temporary.Close()
		if !keep {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	if _, err := temporary.Write(raw); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	keep = true
	directoryHandle, err := os.Open(directory) // #nosec G304 -- directory is derived from the validated lease path.
	if err != nil {
		return err
	}
	defer directoryHandle.Close()
	return directoryHandle.Sync()
}

func browserIdentityHash(identity browserprotocol.Identity) string {
	digest := sha256.New()
	for _, value := range []string{
		identity.RunID,
		identity.AgentID,
		identity.PrincipalScopeID,
		identity.BrowserSessionID,
		identity.AttachmentID,
		identity.BrowserInteractionPolicy,
		identity.BrowserMutationOriginsSHA256,
	} {
		_, _ = digest.Write([]byte(value))
		_, _ = digest.Write([]byte{0})
	}
	_, _ = digest.Write([]byte(
		strconv.FormatUint(identity.SessionEpoch, 10) + "\x00" +
			strconv.FormatUint(identity.ControlEpoch, 10) + "\x00" +
			strconv.FormatInt(identity.BrowserInteractionPolicyGeneration, 10),
	))
	for _, origin := range identity.BrowserMutationOrigins {
		_, _ = digest.Write([]byte{0})
		_, _ = digest.Write([]byte(origin))
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func closedAttachmentObservation(
	identity browserprotocol.Identity,
) browserprotocol.Observation {
	digest := sha256.Sum256([]byte(identity.AttachmentID))
	return browserprotocol.Observation{
		PageStateID: "closed-" + hex.EncodeToString(digest[:16]),
	}
}
