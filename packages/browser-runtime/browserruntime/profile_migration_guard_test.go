//go:build !windows

package browserruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserprofile"
	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserprotocol"
)

func TestProfileMigrationErrorsAreNonRetryableAndDoNotLeakSource(t *testing.T) {
	for _, cause := range []error{browserprofile.ErrProfileMigrationRequired, browserprofile.ErrProfileMigrationPending} {
		failure := profileFailure(errors.Join(cause, errors.New("private-profile-path-and-secret")))
		if failure.Code != browserprotocol.ErrorRuntimeUnavailable || failure.Recoverable ||
			failure.Message != "Browser Profile requires verified offline migration before use" {
			t.Fatalf("unexpected safe migration failure: %#v", failure)
		}
	}
}

// This exercises the real activate → prune → load path, not just the error
// mapper. An expired foreign-contract profile must neither be collected nor
// interpreted as permission to launch Chrome against an empty v2 profile.
func TestProfileEnginePreservesExpiredV1AndDoesNotLaunchEmptyV2(t *testing.T) {
	state := t.TempDir()
	engine := newFixtureProfileEngine(t, state, t.TempDir())
	defer engine.Close()
	identity := profileEngineIdentity("migration-principal")
	profile := browserprofile.Identity{AgentID: identity.AgentID, PrincipalScopeID: identity.PrincipalScopeID,
		ProfileSlot: "default", ProfileGeneration: engine.options.Environment.ProfileGeneration}
	scope, err := json.Marshal(struct {
		ContractID string                  `json:"contract_id"`
		Identity   browserprofile.Identity `json:"identity"`
	}{"openlinker.browser.v1", profile})
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(scope)
	profileDir := filepath.Join(state, "encrypted", "profiles", hex.EncodeToString(digest[:]))
	checkpoint := "0123456789abcdef0123456789abcdef"
	snapshot := filepath.Join(profileDir, "checkpoints", checkpoint)
	if err := os.MkdirAll(snapshot, 0o700); err != nil {
		t.Fatal(err)
	}
	metadata, err := json.Marshal(browserprofile.Metadata{Version: 1, ContractID: "openlinker.browser.v1",
		Algorithm: "AES-256-GCM", KDF: "HKDF-SHA-256", Identity: profile, RootKeyGeneration: 1,
		WrapNonce: make([]byte, 12), WrappedDEK: make([]byte, 48)})
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		filepath.Join(profileDir, "current.json"): []byte(`{"version":1,"checkpoint":"` + checkpoint + `"}`),
		filepath.Join(snapshot, "metadata.json"):  metadata,
		filepath.Join(snapshot, "payload.bin"):    []byte("opaque-legacy-ciphertext-never-decrypted-by-runtime"),
	}
	expired := time.Now().Add(-2 * browserprotocol.BrowserStateRetention).Truncate(time.Second)
	for name, raw := range files {
		if err := os.WriteFile(name, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(name, expired, expired); err != nil {
			t.Fatal(err)
		}
	}
	starts := 0
	engine.processFactory = func(options ProcessEngineOptions) (managedBrowserEngine, error) {
		starts++
		return fixtureProfileFactory(new(bool))(options)
	}
	_, failure := engine.Execute(context.Background(), identity, browserprotocol.Action{Kind: browserprotocol.ActionPreflight})
	if failure == nil || failure.Code != browserprotocol.ErrorRuntimeUnavailable || failure.Recoverable || starts != 0 {
		t.Fatalf("migration guard failed: failure=%#v process_starts=%d", failure, starts)
	}
	for name, raw := range files {
		actual, err := os.ReadFile(name)
		if err != nil || string(actual) != string(raw) {
			t.Fatalf("legacy file changed: %v", err)
		}
		info, err := os.Stat(name)
		if err != nil || !info.ModTime().Equal(expired) || info.Mode().Perm() != 0o600 {
			t.Fatalf("legacy metadata changed: %v", err)
		}
	}
	entries, err := os.ReadDir(filepath.Join(state, "encrypted", "profiles"))
	if err != nil || len(entries) != 1 || entries[0].Name() != hex.EncodeToString(digest[:]) {
		t.Fatalf("unexpected new profile: %v", err)
	}
	quarantine, err := os.ReadDir(filepath.Join(state, "encrypted", "quarantine"))
	if err != nil || len(quarantine) != 0 {
		t.Fatalf("legacy profile quarantined: %v", err)
	}
}
