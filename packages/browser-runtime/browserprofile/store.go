package browserprofile

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	currentVersion        = 1
	currentFileName       = "current.json"
	metadataFileName      = "metadata.json"
	payloadFileName       = "payload.bin"
	quarantineReason      = "profile_authentication_failed"
	stateQuarantineReason = "profile_state_invalid"
	maxCurrentBytes       = 512
	checkpointIDBytes     = 16
)

type Store struct {
	root      string
	protector *protector
	random    io.Reader
	now       func() time.Time
	lockFile  *os.File
	mu        sync.Mutex
}

// QuarantineInvalidState removes an authenticated Profile whose decrypted
// application state failed strict semantic validation. Authentication alone
// cannot make a malformed Browser-owned state file safe to load.
func (store *Store) QuarantineInvalidState(identity Identity) error {
	if store == nil || identity.validate() != nil {
		return ErrInvalidConfiguration
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.lockFile == nil {
		return ErrProfileStoreClosed
	}
	if err := store.quarantine(identity, stateQuarantineReason); err != nil {
		return fmt.Errorf("quarantine invalid browser profile state: %w", err)
	}
	return errors.Join(ErrProfileQuarantined, ErrProfileCorrupt)
}

type currentRecord struct {
	Version    int    `json:"version"`
	Checkpoint string `json:"checkpoint"`
}

type quarantineReport struct {
	Version       int                  `json:"version"`
	ContractID    string               `json:"contract_id"`
	ProfileDigest string               `json:"profile_digest"`
	Reason        string               `json:"reason"`
	QuarantinedAt string               `json:"quarantined_at"`
	Files         []quarantineFileInfo `json:"files"`
}

type quarantineFileInfo struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

func NewStore(root string) (*Store, error) {
	return newStore(root, nil)
}

func newStore(root string, protector *protector) (*Store, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return nil, ErrInvalidConfiguration
	}
	if protector == nil {
		protector = newProtector(nil)
	}
	if protector.random == nil {
		return nil, ErrInvalidConfiguration
	}
	if err := ensurePrivateDirectory(root); err != nil {
		return nil, fmt.Errorf("prepare browser profile root: %w", err)
	}
	lockFile, err := acquireStoreLock(filepath.Join(root, ".manager.lock"))
	if err != nil {
		return nil, err
	}
	for _, name := range []string{"profiles", "quarantine"} {
		if err := ensurePrivateDirectory(filepath.Join(root, name)); err != nil {
			_ = releaseStoreLock(lockFile)
			return nil, fmt.Errorf("prepare browser profile %s directory: %w", name, err)
		}
	}
	return &Store{
		root:      root,
		protector: protector,
		random:    protector.random,
		now:       time.Now,
		lockFile:  lockFile,
	}, nil
}

func (store *Store) Close() error {
	if store == nil {
		return nil
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.lockFile == nil {
		return nil
	}
	err := releaseStoreLock(store.lockFile)
	store.lockFile = nil
	return err
}

func (store *Store) Create(
	identity Identity,
	root *RootKey,
	payload io.Reader,
) error {
	if store == nil || identity.validate() != nil || !validRoot(root) || payload == nil {
		return ErrInvalidConfiguration
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.lockFile == nil {
		return ErrProfileStoreClosed
	}

	profileDir := store.profileDir(identity)
	if _, err := os.Lstat(profileDir); err == nil {
		return ErrProfileExists
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("inspect browser profile: %w", err)
	}
	if err := ensurePrivateDirectory(profileDir); err != nil {
		return fmt.Errorf("create browser profile: %w", err)
	}
	metadata, payloadCipher, err := store.protector.create(identity, root)
	if err != nil {
		_ = os.Remove(profileDir)
		return err
	}
	defer payloadCipher.close()
	committed, err := store.commitSnapshot(profileDir, metadata, payloadCipher, payload)
	if err != nil {
		if !committed {
			_ = os.RemoveAll(profileDir)
		}
		return err
	}
	return nil
}

func (store *Store) Checkpoint(
	identity Identity,
	roots map[uint64]*RootKey,
	payload io.Reader,
) error {
	if store == nil || identity.validate() != nil || payload == nil {
		return ErrInvalidConfiguration
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.lockFile == nil {
		return ErrProfileStoreClosed
	}

	snapshot, err := store.openSnapshot(identity, roots)
	if err != nil {
		return store.handleOpenError(identity, err)
	}
	defer snapshot.close()
	if err := authenticatePayload(snapshot.payload, snapshot.payloadCipher); err != nil {
		snapshot.close()
		return store.handleOpenError(identity, err)
	}
	_, err = store.commitSnapshot(
		store.profileDir(identity),
		snapshot.metadata,
		snapshot.payloadCipher,
		payload,
	)
	if err != nil {
		return err
	}
	return nil
}

func (store *Store) Load(
	identity Identity,
	roots map[uint64]*RootKey,
	writer io.Writer,
) error {
	if store == nil || identity.validate() != nil || writer == nil {
		return ErrInvalidConfiguration
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.lockFile == nil {
		return ErrProfileStoreClosed
	}

	snapshot, err := store.openSnapshot(identity, roots)
	if err != nil {
		return store.handleOpenError(identity, err)
	}
	defer snapshot.close()
	if err := authenticatePayload(snapshot.payload, snapshot.payloadCipher); err != nil {
		snapshot.close()
		return store.handleOpenError(identity, err)
	}
	if _, err := snapshot.payload.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind browser profile payload: %w", err)
	}
	if err := snapshot.payloadCipher.decrypt(writer, snapshot.payload); err != nil {
		if errors.Is(err, ErrProfileCorrupt) {
			snapshot.close()
			return store.handleOpenError(identity, err)
		}
		return err
	}
	return nil
}

func (store *Store) rewrap(
	identity Identity,
	roots map[uint64]*RootKey,
	newRoot *RootKey,
) error {
	if store == nil || identity.validate() != nil || !validRoot(newRoot) {
		return ErrInvalidConfiguration
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.lockFile == nil {
		return ErrProfileStoreClosed
	}

	snapshot, err := store.openSnapshot(identity, roots)
	if err != nil {
		return store.handleOpenError(identity, err)
	}
	defer snapshot.close()
	if err := authenticatePayload(snapshot.payload, snapshot.payloadCipher); err != nil {
		snapshot.close()
		return store.handleOpenError(identity, err)
	}
	oldRoot := roots[snapshot.metadata.RootKeyGeneration]
	rewrapped, err := store.protector.rewrap(snapshot.metadata, identity, oldRoot, newRoot)
	if err != nil {
		return store.handleOpenError(identity, err)
	}
	raw, err := marshalMetadata(rewrapped)
	if err != nil {
		return err
	}
	if _, err := atomicWriteFile(filepath.Join(snapshot.dir, metadataFileName), raw, 0o600); err != nil {
		return fmt.Errorf("commit rewrapped browser profile metadata: %w", err)
	}
	return nil
}

// PruneInactive removes committed Profile snapshots whose current pointer
// has not been updated since before. Missing or malformed state is retained so
// that a later Load can quarantine it instead of silently discarding evidence.
func (store *Store) PruneInactive(before time.Time) (int, error) {
	if store == nil || before.IsZero() {
		return 0, ErrInvalidConfiguration
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.lockFile == nil {
		return 0, ErrProfileStoreClosed
	}
	profilesDir := filepath.Join(store.root, "profiles")
	entries, err := os.ReadDir(profilesDir)
	if err != nil {
		return 0, fmt.Errorf("list Browser Profiles for expiry: %w", err)
	}
	removed := 0
	for _, entry := range entries {
		if !entry.IsDir() || !validProfileDigest(entry.Name()) {
			continue
		}
		profileDir := filepath.Join(profilesDir, entry.Name())
		currentPath := filepath.Join(profileDir, currentFileName)
		info, err := os.Lstat(currentPath)
		if err != nil || !info.Mode().IsRegular() ||
			info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 ||
			!info.ModTime().Before(before) {
			continue
		}
		if err := os.RemoveAll(profileDir); err != nil {
			return removed, fmt.Errorf("expire inactive Browser Profile: %w", err)
		}
		removed++
	}
	if removed > 0 {
		if err := syncDirectory(profilesDir); err != nil {
			return removed, err
		}
	}
	return removed, nil
}

type openProfileSnapshot struct {
	dir           string
	metadata      Metadata
	payload       *os.File
	payloadCipher *payloadCipher
}

func (snapshot *openProfileSnapshot) close() {
	if snapshot == nil {
		return
	}
	if snapshot.payload != nil {
		_ = snapshot.payload.Close()
	}
	if snapshot.payloadCipher != nil {
		snapshot.payloadCipher.close()
	}
}

func (store *Store) openSnapshot(
	identity Identity,
	roots map[uint64]*RootKey,
) (*openProfileSnapshot, error) {
	profileDir := store.profileDir(identity)
	if err := validatePrivateDirectory(profileDir); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, ErrProfileNotFound
		}
		return nil, ErrProfileCorrupt
	}
	currentRaw, err := readBoundedRegularFile(
		filepath.Join(profileDir, currentFileName),
		maxCurrentBytes,
	)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrProfileCorrupt
	}
	if err != nil {
		return nil, ErrProfileCorrupt
	}
	current, err := parseCurrent(currentRaw)
	if err != nil {
		return nil, err
	}
	snapshotDir := filepath.Join(profileDir, "checkpoints", current.Checkpoint)
	if err := validatePrivateDirectory(snapshotDir); err != nil {
		return nil, ErrProfileCorrupt
	}
	metadataRaw, err := readBoundedRegularFile(
		filepath.Join(snapshotDir, metadataFileName),
		maxMetadataBytes,
	)
	if err != nil {
		return nil, ErrProfileCorrupt
	}
	metadata, err := parseMetadata(metadataRaw)
	if err != nil {
		return nil, err
	}
	root := roots[metadata.RootKeyGeneration]
	if !validRoot(root) {
		return nil, ErrRootKeyUnavailable
	}
	payloadCipher, err := store.protector.open(metadata, identity, root)
	if err != nil {
		return nil, err
	}
	payload, err := openRegularFile(filepath.Join(snapshotDir, payloadFileName))
	if err != nil {
		payloadCipher.close()
		return nil, ErrProfileCorrupt
	}
	return &openProfileSnapshot{
		dir:           snapshotDir,
		metadata:      metadata,
		payload:       payload,
		payloadCipher: payloadCipher,
	}, nil
}

func authenticatePayload(payload *os.File, payloadCipher *payloadCipher) error {
	if _, err := payload.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind browser profile payload: %w", err)
	}
	return payloadCipher.decrypt(io.Discard, payload)
}

func (store *Store) commitSnapshot(
	profileDir string,
	metadata Metadata,
	payloadCipher *payloadCipher,
	payload io.Reader,
) (bool, error) {
	checkpointsDir := filepath.Join(profileDir, "checkpoints")
	if err := ensurePrivateDirectory(checkpointsDir); err != nil {
		return false, fmt.Errorf("prepare browser profile checkpoints: %w", err)
	}
	pendingDir, err := os.MkdirTemp(checkpointsDir, ".pending-")
	if err != nil {
		return false, fmt.Errorf("create browser profile checkpoint: %w", err)
	}
	if err := os.Chmod(pendingDir, 0o700); err != nil {
		_ = os.RemoveAll(pendingDir)
		return false, fmt.Errorf("secure browser profile checkpoint: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(pendingDir)
		}
	}()

	rawMetadata, err := marshalMetadata(metadata)
	if err != nil {
		return false, err
	}
	if err := writeSyncedFile(filepath.Join(pendingDir, metadataFileName), rawMetadata, 0o600); err != nil {
		return false, err
	}
	payloadFile, err := openExclusiveFile(filepath.Join(pendingDir, payloadFileName), 0o600)
	if err != nil {
		return false, fmt.Errorf("create browser profile payload: %w", err)
	}
	encryptErr := payloadCipher.encrypt(payloadFile, payload)
	if encryptErr == nil {
		encryptErr = payloadFile.Sync()
	}
	closeErr := payloadFile.Close()
	if encryptErr != nil {
		return false, fmt.Errorf("encrypt browser profile payload: %w", encryptErr)
	}
	if closeErr != nil {
		return false, fmt.Errorf("close browser profile payload: %w", closeErr)
	}
	if err := syncDirectory(pendingDir); err != nil {
		return false, err
	}

	checkpointID, err := store.newCheckpointID(checkpointsDir)
	if err != nil {
		return false, err
	}
	finalDir := filepath.Join(checkpointsDir, checkpointID)
	if err := os.Rename(pendingDir, finalDir); err != nil {
		return false, fmt.Errorf("publish browser profile checkpoint: %w", err)
	}
	pendingDir = finalDir
	if err := syncDirectory(checkpointsDir); err != nil {
		return false, err
	}
	currentRaw, err := json.Marshal(currentRecord{
		Version:    currentVersion,
		Checkpoint: checkpointID,
	})
	if err != nil {
		return false, ErrInvalidConfiguration
	}
	pointerPublished, err := atomicWriteFile(
		filepath.Join(profileDir, currentFileName),
		currentRaw,
		0o600,
	)
	if pointerPublished {
		committed = true
	}
	if err != nil {
		return pointerPublished, fmt.Errorf("publish browser profile pointer: %w", err)
	}
	committed = true
	store.removeOldCheckpoints(checkpointsDir, checkpointID)
	return true, nil
}

func (store *Store) newCheckpointID(checkpointsDir string) (string, error) {
	for attempt := 0; attempt < 8; attempt++ {
		raw := make([]byte, checkpointIDBytes)
		if _, err := io.ReadFull(store.random, raw); err != nil {
			return "", fmt.Errorf("generate browser profile checkpoint ID: %w", err)
		}
		checkpointID := hex.EncodeToString(raw)
		if _, err := os.Lstat(filepath.Join(checkpointsDir, checkpointID)); errors.Is(err, fs.ErrNotExist) {
			return checkpointID, nil
		} else if err != nil {
			return "", fmt.Errorf("inspect browser profile checkpoint ID: %w", err)
		}
	}
	return "", errors.New("cannot allocate a unique browser profile checkpoint ID")
}

func (store *Store) removeOldCheckpoints(checkpointsDir string, current string) {
	entries, err := os.ReadDir(checkpointsDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.Name() == current {
			continue
		}
		if validCheckpointID(entry.Name()) || strings.HasPrefix(entry.Name(), ".pending-") {
			_ = os.RemoveAll(filepath.Join(checkpointsDir, entry.Name()))
		}
	}
	_ = syncDirectory(checkpointsDir)
}

func (store *Store) handleOpenError(identity Identity, cause error) error {
	if !isQuarantineCause(cause) {
		return cause
	}
	if err := store.quarantine(identity, quarantineReason); err != nil {
		return errors.Join(cause, fmt.Errorf("quarantine browser profile: %w", err))
	}
	return errors.Join(ErrProfileQuarantined, cause)
}

func isQuarantineCause(err error) bool {
	return errors.Is(err, ErrProfileCorrupt) ||
		errors.Is(err, ErrIdentityMismatch) ||
		errors.Is(err, ErrKeyGeneration)
}

func (store *Store) quarantine(identity Identity, reason string) error {
	profileDigest := profileDigest(identity)
	source := filepath.Join(store.root, "profiles", profileDigest)
	if _, err := os.Lstat(source); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return ErrProfileNotFound
		}
		return err
	}
	suffix, err := store.randomHex(8)
	if err != nil {
		return err
	}
	name := store.now().UTC().Format("20060102T150405.000000000Z") + "-" + profileDigest[:16] + "-" + suffix
	destination := filepath.Join(store.root, "quarantine", name)
	if err := os.Rename(source, destination); err != nil {
		return err
	}
	if err := syncDirectory(filepath.Join(store.root, "profiles")); err != nil {
		return err
	}
	if err := syncDirectory(filepath.Join(store.root, "quarantine")); err != nil {
		return err
	}
	files, err := sanitizeAndChecksumTree(destination)
	if err != nil {
		return err
	}
	report := quarantineReport{
		Version:       1,
		ContractID:    contractID(),
		ProfileDigest: profileDigest,
		Reason:        reason,
		QuarantinedAt: store.now().UTC().Format(time.RFC3339Nano),
		Files:         files,
	}
	raw, err := json.Marshal(report)
	if err != nil {
		return err
	}
	if err := writeSyncedFile(filepath.Join(destination, "quarantine.json"), raw, 0o400); err != nil {
		return err
	}
	return makeTreeReadOnly(destination)
}

func (store *Store) randomHex(size int) (string, error) {
	raw := make([]byte, size)
	if _, err := io.ReadFull(store.random, raw); err != nil {
		return "", fmt.Errorf("generate browser profile identifier: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

func (store *Store) profileDir(identity Identity) string {
	return filepath.Join(store.root, "profiles", profileDigest(identity))
}

func profileDigest(identity Identity) string {
	raw, _ := json.Marshal(struct {
		ContractID string   `json:"contract_id"`
		Identity   Identity `json:"identity"`
	}{
		ContractID: contractID(),
		Identity:   identity,
	})
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func parseCurrent(raw []byte) (currentRecord, error) {
	if len(raw) == 0 || len(raw) > maxCurrentBytes {
		return currentRecord{}, ErrProfileCorrupt
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var current currentRecord
	if err := decoder.Decode(&current); err != nil {
		return currentRecord{}, ErrProfileCorrupt
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return currentRecord{}, ErrProfileCorrupt
	}
	if current.Version != currentVersion || !validCheckpointID(current.Checkpoint) {
		return currentRecord{}, ErrProfileCorrupt
	}
	return current, nil
}

func validCheckpointID(value string) bool {
	if len(value) != checkpointIDBytes*2 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validProfileDigest(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func ensurePrivateDirectory(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		if err := os.Mkdir(path, 0o700); err != nil {
			return err
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrInvalidConfiguration
	}
	if info.Mode().Perm() != 0o700 {
		return os.Chmod(path, 0o700)
	}
	return nil
}

func validatePrivateDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return ErrProfileCorrupt
	}
	return nil
}

func openRegularFile(path string) (*os.File, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() || before.Mode().Perm()&0o077 != 0 {
		return nil, ErrProfileCorrupt
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	after, err := file.Stat()
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(before, after) {
		_ = file.Close()
		return nil, ErrProfileCorrupt
	}
	return file, nil
}

func readBoundedRegularFile(path string, limit int) ([]byte, error) {
	file, err := openRegularFile(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	value, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(value) > limit {
		return nil, ErrProfileCorrupt
	}
	return value, nil
}

func openExclusiveFile(path string, mode fs.FileMode) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
}

func writeSyncedFile(path string, value []byte, mode fs.FileMode) error {
	file, err := openExclusiveFile(path, mode)
	if err != nil {
		return err
	}
	writeErr := writeAll(file, value)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

func atomicWriteFile(path string, value []byte, mode fs.FileMode) (bool, error) {
	dir := filepath.Dir(path)
	temporary, err := os.CreateTemp(dir, ".atomic-")
	if err != nil {
		return false, err
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = os.Remove(temporaryPath)
	}()
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return false, err
	}
	writeErr := writeAll(temporary, value)
	if writeErr == nil {
		writeErr = temporary.Sync()
	}
	closeErr := temporary.Close()
	if writeErr != nil {
		return false, writeErr
	}
	if closeErr != nil {
		return false, closeErr
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return false, err
	}
	return true, syncDirectory(dir)
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("sync directory %s: %w", filepath.Base(path), err)
	}
	return nil
}

func sanitizeAndChecksumTree(root string) ([]quarantineFileInfo, error) {
	var files []quarantineFileInfo
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if err := os.Remove(path); err != nil {
				return err
			}
			files = append(files, quarantineFileInfo{
				Path: filepath.ToSlash(relative),
				Kind: "symlink_removed",
			})
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			if err := os.Remove(path); err != nil {
				return err
			}
			files = append(files, quarantineFileInfo{
				Path: filepath.ToSlash(relative),
				Kind: "non_regular_removed",
			})
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		digest := sha256.New()
		_, copyErr := io.Copy(digest, file)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		files = append(files, quarantineFileInfo{
			Path:   filepath.ToSlash(relative),
			Kind:   "regular",
			Bytes:  info.Size(),
			SHA256: hex.EncodeToString(digest.Sum(nil)),
		})
		return nil
	})
	sort.Slice(files, func(left int, right int) bool {
		return files[left].Path < files[right].Path
	})
	return files, err
}

func makeTreeReadOnly(root string) error {
	var directories []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return ErrProfileCorrupt
		}
		if entry.IsDir() {
			directories = append(directories, path)
			return nil
		}
		return os.Chmod(path, 0o400)
	})
	if err != nil {
		return err
	}
	sort.Slice(directories, func(left int, right int) bool {
		return len(directories[left]) > len(directories[right])
	})
	for _, directory := range directories {
		if err := os.Chmod(directory, 0o500); err != nil {
			return err
		}
	}
	return nil
}
