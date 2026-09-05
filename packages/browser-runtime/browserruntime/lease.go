//go:build !windows

package browserruntime

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserclient"
	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserprotocol"
)

type LeaseValidator interface {
	Validate(browserprotocol.Identity) *browserprotocol.Failure
}

type LeaseRevoker interface {
	IsRevoked(browserprotocol.Identity) (bool, *browserprotocol.Failure)
	Revoke(browserprotocol.Identity) *browserprotocol.Failure
}

type StaticLease struct {
	Identity browserprotocol.Identity
}

func (lease StaticLease) Validate(identity browserprotocol.Identity) *browserprotocol.Failure {
	expected := lease.Identity
	if identity.RunID != expected.RunID ||
		identity.AgentID != expected.AgentID ||
		identity.PrincipalScopeID != expected.PrincipalScopeID ||
		identity.BrowserSessionID != expected.BrowserSessionID ||
		identity.SessionEpoch != expected.SessionEpoch ||
		identity.AttachmentID != expected.AttachmentID ||
		identity.BrowserInteractionPolicy != expected.BrowserInteractionPolicy ||
		identity.BrowserInteractionPolicyGeneration != expected.BrowserInteractionPolicyGeneration ||
		identity.BrowserMutationOriginsSHA256 != expected.BrowserMutationOriginsSHA256 ||
		!sameStrings(identity.BrowserMutationOrigins, expected.BrowserMutationOrigins) {
		return browserprotocol.NewFailure(
			browserprotocol.ErrorIdentityMismatch,
			"browser attachment identity does not match the active lease",
			false,
		)
	}
	if identity.ControlEpoch != expected.ControlEpoch {
		return browserprotocol.NewFailure(
			browserprotocol.ErrorStaleControlEpoch,
			"browser control epoch is stale",
			false,
		)
	}
	if identity.Controller != expected.Controller {
		return browserprotocol.NewFailure(
			browserprotocol.ErrorIdentityMismatch,
			"browser controller does not match the active lease",
			false,
		)
	}
	return nil
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

type FileLease struct {
	Path string
	Now  func() time.Time
}

func (lease FileLease) Validate(identity browserprotocol.Identity) *browserprotocol.Failure {
	revoked, err := lease.isRevoked(identity)
	if err != nil {
		return browserprotocol.NewFailure(
			browserprotocol.ErrorRuntimeUnavailable,
			"browser runtime revocation state is invalid",
			false,
		)
	}
	if revoked {
		return browserprotocol.NewFailure(
			browserprotocol.ErrorIdentityMismatch,
			"browser attachment has been closed",
			false,
		)
	}
	if failure := lease.validateActive(identity); failure != nil {
		return failure
	}
	return nil
}

func (lease FileLease) IsRevoked(
	identity browserprotocol.Identity,
) (bool, *browserprotocol.Failure) {
	revoked, err := lease.isRevoked(identity)
	if err != nil {
		return false, browserprotocol.NewFailure(
			browserprotocol.ErrorRuntimeUnavailable,
			"browser runtime revocation state is invalid",
			false,
		)
	}
	return revoked, nil
}

func (lease FileLease) validateActive(identity browserprotocol.Identity) *browserprotocol.Failure {
	_, failure := lease.active(identity)
	return failure
}

func (lease FileLease) active(
	identity browserprotocol.Identity,
) (browserclient.Lease, *browserprotocol.Failure) {
	now := lease.Now
	if now == nil {
		now = time.Now
	}
	return lease.activeAt(identity, now().UTC())
}

func (lease FileLease) activeAt(
	identity browserprotocol.Identity,
	now time.Time,
) (browserclient.Lease, *browserprotocol.Failure) {
	path := filepath.Clean(strings.TrimSpace(lease.Path))
	if !filepath.IsAbs(path) {
		return browserclient.Lease{}, browserprotocol.NewFailure(
			browserprotocol.ErrorRuntimeUnavailable,
			"browser runtime active lease path is invalid",
			false,
		)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return browserclient.Lease{}, browserprotocol.NewFailure(
			browserprotocol.ErrorRuntimeUnavailable,
			"browser runtime has no active attachment",
			true,
		)
	}
	stat, owned := info.Sys().(*syscall.Stat_t)
	if info.Mode()&os.ModeSymlink != 0 ||
		!info.Mode().IsRegular() ||
		info.Mode().Perm()&0o077 != 0 ||
		!owned ||
		int(stat.Uid) != os.Geteuid() ||
		info.Size() <= 0 ||
		info.Size() > 16<<10 {
		return browserclient.Lease{}, browserprotocol.NewFailure(
			browserprotocol.ErrorRuntimeUnavailable,
			"browser runtime active lease is invalid",
			false,
		)
	}
	file, err := os.Open(path) // #nosec G304 -- operator-selected active lease path is validated above.
	if err != nil {
		return browserclient.Lease{}, browserprotocol.NewFailure(
			browserprotocol.ErrorRuntimeUnavailable,
			"browser runtime active lease is unavailable",
			true,
		)
	}
	raw, err := io.ReadAll(io.LimitReader(file, (16<<10)+1))
	_ = file.Close()
	if err != nil || len(raw) > 16<<10 {
		return browserclient.Lease{}, browserprotocol.NewFailure(
			browserprotocol.ErrorRuntimeUnavailable,
			"browser runtime active lease is unreadable",
			true,
		)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var active browserclient.Lease
	if err := decoder.Decode(&active); err != nil {
		return browserclient.Lease{}, browserprotocol.NewFailure(
			browserprotocol.ErrorRuntimeUnavailable,
			"browser runtime active lease is invalid",
			false,
		)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return browserclient.Lease{}, browserprotocol.NewFailure(
			browserprotocol.ErrorRuntimeUnavailable,
			"browser runtime active lease is invalid",
			false,
		)
	}
	if failure := active.Validate(now.UTC()); failure != nil {
		return browserclient.Lease{}, failure
	}
	if failure := (StaticLease{Identity: active.Identity}).Validate(identity); failure != nil {
		return browserclient.Lease{}, failure
	}
	return active, nil
}
