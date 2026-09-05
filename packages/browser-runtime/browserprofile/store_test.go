//go:build unix

package browserprofile

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestStoreCreateLoadCheckpointAndAtomicRecovery(t *testing.T) {
	t.Parallel()
	store := testStore(t)
	identity := testIdentity()
	root := testRootKey(t, 1, 0x11)
	defer root.Close()
	roots := map[uint64]*RootKey{root.Generation(): root}

	if err := store.Create(identity, root, strings.NewReader("checkpoint-one")); err != nil {
		t.Fatal(err)
	}
	assertStoredPayload(t, store, identity, roots, "checkpoint-one")
	if err := store.Create(identity, root, strings.NewReader("replacement")); !errors.Is(err, ErrProfileExists) {
		t.Fatalf("second Create() error = %v, want %v", err, ErrProfileExists)
	}

	if err := store.Checkpoint(identity, roots, strings.NewReader("checkpoint-two")); err != nil {
		t.Fatal(err)
	}
	assertStoredPayload(t, store, identity, roots, "checkpoint-two")
	checkpoints, err := os.ReadDir(filepath.Join(store.profileDir(identity), "checkpoints"))
	if err != nil {
		t.Fatal(err)
	}
	if len(checkpoints) != 1 || !validCheckpointID(checkpoints[0].Name()) {
		t.Fatalf("checkpoint entries = %#v, want one committed checkpoint", checkpoints)
	}

	failing := &errorReader{remaining: []byte("partial"), err: errors.New("fixture read failed")}
	if err := store.Checkpoint(identity, roots, failing); err == nil {
		t.Fatal("Checkpoint() with failing reader succeeded")
	}
	assertStoredPayload(t, store, identity, roots, "checkpoint-two")
}

func TestStoreRewrapDoesNotRewritePayload(t *testing.T) {
	t.Parallel()
	store := testStore(t)
	identity := testIdentity()
	oldRoot := testRootKey(t, 1, 0x11)
	defer oldRoot.Close()
	newRoot := testRootKey(t, 2, 0x22)
	defer newRoot.Close()
	if err := store.Create(identity, oldRoot, strings.NewReader("persistent-login-state")); err != nil {
		t.Fatal(err)
	}
	snapshotDir := currentSnapshotDir(t, store, identity)
	payloadBefore, err := os.ReadFile(filepath.Join(snapshotDir, payloadFileName))
	if err != nil {
		t.Fatal(err)
	}

	if err := store.rewrap(
		identity,
		map[uint64]*RootKey{oldRoot.Generation(): oldRoot},
		newRoot,
	); err != nil {
		t.Fatal(err)
	}
	payloadAfter, err := os.ReadFile(filepath.Join(snapshotDir, payloadFileName))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(payloadBefore, payloadAfter) {
		t.Fatal("root key rewrap changed encrypted Profile payload")
	}
	if err := store.Load(
		identity,
		map[uint64]*RootKey{oldRoot.Generation(): oldRoot},
		&bytes.Buffer{},
	); !errors.Is(err, ErrRootKeyUnavailable) {
		t.Fatalf("Load() with retired root error = %v, want %v", err, ErrRootKeyUnavailable)
	}
	assertStoredPayload(
		t,
		store,
		identity,
		map[uint64]*RootKey{newRoot.Generation(): newRoot},
		"persistent-login-state",
	)
}

func TestStoreMissingRootFailsClosedWithoutQuarantine(t *testing.T) {
	t.Parallel()
	store := testStore(t)
	identity := testIdentity()
	root := testRootKey(t, 1, 0x11)
	defer root.Close()
	if err := store.Create(identity, root, strings.NewReader("state")); err != nil {
		t.Fatal(err)
	}
	if err := store.Load(identity, nil, &bytes.Buffer{}); !errors.Is(err, ErrRootKeyUnavailable) {
		t.Fatalf("Load() error = %v, want %v", err, ErrRootKeyUnavailable)
	}
	if _, err := os.Stat(store.profileDir(identity)); err != nil {
		t.Fatalf("missing root key moved Profile unexpectedly: %v", err)
	}
	quarantineEntries, err := os.ReadDir(filepath.Join(store.root, "quarantine"))
	if err != nil {
		t.Fatal(err)
	}
	if len(quarantineEntries) != 0 {
		t.Fatalf("quarantine entries = %d, want 0", len(quarantineEntries))
	}
}

func TestStorePrunesOnlyInactiveValidProfiles(t *testing.T) {
	t.Parallel()
	store := testStore(t)
	oldIdentity := testIdentity()
	currentIdentity := oldIdentity
	currentIdentity.ProfileSlot = "current"
	root := testRootKey(t, 1, 0x11)
	defer root.Close()
	if err := store.Create(oldIdentity, root, strings.NewReader("old")); err != nil {
		t.Fatal(err)
	}
	if err := store.Create(currentIdentity, root, strings.NewReader("current")); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	oldCurrent := filepath.Join(store.profileDir(oldIdentity), currentFileName)
	if err := os.Chtimes(oldCurrent, now.Add(-31*24*time.Hour), now.Add(-31*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	currentPointer := filepath.Join(store.profileDir(currentIdentity), currentFileName)
	if err := os.Chtimes(currentPointer, now, now); err != nil {
		t.Fatal(err)
	}
	removed, err := store.PruneInactive(now.Add(-30 * 24 * time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("removed Profiles = %d, want 1", removed)
	}
	if _, err := os.Stat(store.profileDir(oldIdentity)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("inactive Browser Profile remains: %v", err)
	}
	assertStoredPayload(
		t,
		store,
		currentIdentity,
		map[uint64]*RootKey{root.Generation(): root},
		"current",
	)
}

func TestStoreCorruptionMovesProfileToReadOnlyQuarantine(t *testing.T) {
	t.Parallel()
	store := testStore(t)
	identity := testIdentity()
	root := testRootKey(t, 1, 0x11)
	defer root.Close()
	roots := map[uint64]*RootKey{root.Generation(): root}
	if err := store.Create(identity, root, strings.NewReader("sensitive-cookie-state")); err != nil {
		t.Fatal(err)
	}
	payloadPath := filepath.Join(currentSnapshotDir(t, store, identity), payloadFileName)
	payload, err := os.ReadFile(payloadPath)
	if err != nil {
		t.Fatal(err)
	}
	payload[len(payload)-1] ^= 0xff
	if err := os.WriteFile(payloadPath, payload, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := store.Load(identity, roots, &bytes.Buffer{}); !errors.Is(err, ErrProfileQuarantined) {
		t.Fatalf("Load() error = %v, want %v", err, ErrProfileQuarantined)
	}
	if _, err := os.Stat(store.profileDir(identity)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("active Profile still exists after quarantine: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(store.root, "quarantine"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("quarantine entries = %d, want 1", len(entries))
	}
	quarantineDir := filepath.Join(store.root, "quarantine", entries[0].Name())
	report, err := os.ReadFile(filepath.Join(quarantineDir, "quarantine.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(report, []byte(quarantineReason)) ||
		bytes.Contains(report, []byte("sensitive-cookie-state")) {
		t.Fatalf("unexpected quarantine report: %s", report)
	}
	if runtime.GOOS != "windows" {
		assertMode(t, quarantineDir, 0o500)
		assertMode(t, filepath.Join(quarantineDir, "quarantine.json"), 0o400)
	}
}

func TestStoreRejectsSymlinkedStateFiles(t *testing.T) {
	t.Parallel()
	store := testStore(t)
	identity := testIdentity()
	root := testRootKey(t, 1, 0x11)
	defer root.Close()
	if err := store.Create(identity, root, strings.NewReader("state")); err != nil {
		t.Fatal(err)
	}
	currentPath := filepath.Join(store.profileDir(identity), currentFileName)
	currentRaw, err := os.ReadFile(currentPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(currentPath); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "current.json")
	if err := os.WriteFile(target, currentRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, currentPath); err != nil {
		t.Fatal(err)
	}
	if err := store.Load(
		identity,
		map[uint64]*RootKey{root.Generation(): root},
		&bytes.Buffer{},
	); !errors.Is(err, ErrProfileQuarantined) {
		t.Fatalf("Load() error = %v, want %v", err, ErrProfileQuarantined)
	}
}

func TestStoreRejectsSymlinkRootAndInvalidPointerTraversal(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	target := filepath.Join(parent, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(parent, "link")
	if err := os.Symlink(target, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink unavailable: %v", err)
		}
		t.Fatal(err)
	}
	if _, err := NewStore(link); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("NewStore() error = %v, want %v", err, ErrInvalidConfiguration)
	}

	store := testStore(t)
	identity := testIdentity()
	root := testRootKey(t, 1, 0x11)
	defer root.Close()
	if err := store.Create(identity, root, strings.NewReader("state")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(store.profileDir(identity), currentFileName),
		[]byte(`{"version":1,"checkpoint":"../../outside"}`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := store.Load(
		identity,
		map[uint64]*RootKey{root.Generation(): root},
		&bytes.Buffer{},
	); !errors.Is(err, ErrProfileQuarantined) {
		t.Fatalf("Load() error = %v, want %v", err, ErrProfileQuarantined)
	}
}

func TestStoreHoldsExclusiveManagerLock(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "browser-profiles")
	first, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(root); !errors.Is(err, ErrProfileStoreLocked) {
		t.Fatalf("second NewStore() error = %v, want %v", err, ErrProfileStoreLocked)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
}

type errorReader struct {
	remaining []byte
	err       error
}

func (reader *errorReader) Read(value []byte) (int, error) {
	if len(reader.remaining) == 0 {
		return 0, reader.err
	}
	read := copy(value, reader.remaining)
	reader.remaining = reader.remaining[read:]
	return read, nil
}

func testStore(t *testing.T) *Store {
	t.Helper()
	root := filepath.Join(t.TempDir(), "browser-profiles")
	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = store.Close()
		_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			if entry.IsDir() {
				_ = os.Chmod(path, 0o700)
			} else {
				_ = os.Chmod(path, 0o600)
			}
			return nil
		})
	})
	return store
}

func assertStoredPayload(
	t *testing.T,
	store *Store,
	identity Identity,
	roots map[uint64]*RootKey,
	expected string,
) {
	t.Helper()
	var loaded bytes.Buffer
	if err := store.Load(identity, roots, &loaded); err != nil {
		t.Fatal(err)
	}
	if loaded.String() != expected {
		t.Fatalf("loaded Profile = %q, want %q", loaded.String(), expected)
	}
}

func currentSnapshotDir(t *testing.T, store *Store, identity Identity) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(store.profileDir(identity), currentFileName))
	if err != nil {
		t.Fatal(err)
	}
	current, err := parseCurrent(raw)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(store.profileDir(identity), "checkpoints", current.Checkpoint)
}

func assertMode(t *testing.T, path string, expected fs.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != expected {
		t.Fatalf("%s mode = %#o, want %#o", filepath.Base(path), got, expected)
	}
}

var _ io.Reader = (*errorReader)(nil)
