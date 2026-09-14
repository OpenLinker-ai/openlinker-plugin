//go:build linux

package browserprofile

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

type migrationMarker struct {
	Schema               string            `json:"schema"`
	OperationSHA256      string            `json:"operation_sha256"`
	RequestSHA256        string            `json:"request_sha256"`
	TargetBasenameSHA256 string            `json:"target_basename_sha256"`
	Source               MigrationSnapshot `json:"source"`
}

func executeProfileMigration(ctx context.Context, requestPath string, mode MigrationMode, report *MigrationReport) error {
	started := time.Now()
	if err := ctx.Err(); err != nil {
		return migrationFailure(report, "cancelled", err)
	}
	raw, err := migrationAbsoluteFile(requestPath, migrationMaxRequest)
	if err != nil {
		return migrationFailure(report, "invalid_request_file", err)
	}
	var request MigrationRequest
	if err := strictMigrationJSON(raw, &request); err != nil {
		return migrationFailure(report, "invalid_request", err)
	}
	anchor, err := validateMigrationRequest(request, time.Now())
	if err != nil {
		return migrationFailure(report, "invalid_request_or_retention", err)
	}
	report.RequestSHA256 = migrationSHA(raw)
	report.OperationSHA256 = migrationSHA([]byte(request.OperationID))
	identityRaw, _ := json.Marshal(request.ExpectedIdentity)
	report.IdentitySHA256 = migrationSHA(identityRaw)
	report.SourceContract, report.TargetContract = request.SourceContract, request.TargetContract
	report.ProfileGeneration, report.RootKeyGeneration = request.ExpectedIdentity.ProfileGeneration, request.RootKeyGeneration
	report.Source, report.Retention = request.Source, request.Retention
	report.Stage = "source_open"
	sourceDir, err := migrationDirectory(request.SourceStore, true)
	if err != nil {
		return migrationFailure(report, "source_path_invalid", err)
	}
	defer sourceDir.Close()
	sourceLock, err := migrationLock(sourceDir)
	if err != nil {
		return migrationFailure(report, "source_lock_unavailable", err)
	}
	defer releaseStoreLock(sourceLock)
	keyRaw, err := migrationAbsoluteFile(request.RootKeyFile, 32)
	if err != nil {
		return migrationFailure(report, "root_key_unavailable", err)
	}
	defer clear(keyRaw)
	key, err := NewRootKey(request.RootKeyGeneration, keyRaw)
	if err != nil {
		return migrationFailure(report, "root_key_invalid", err)
	}
	defer key.Close()
	source, err := openMigrationSnapshot(ctx, sourceDir, request.ExpectedIdentity, key, storageContractV1)
	if err != nil {
		return migrationFailure(report, "source_snapshot_invalid", err)
	}
	defer source.close()
	if source.fingerprint != request.Source {
		return migrationFailure(report, "source_precondition_mismatch", ErrProfileCorrupt)
	}
	report.Stage = "source_authenticate"
	if err := source.decrypt(ctx, io.Discard); err != nil {
		return migrationFailure(report, "source_authentication_failed", err)
	}
	report.SourceAuthenticated = true
	if err := source.verify(ctx, request.ExpectedIdentity, key, storageContractV1); err != nil {
		return migrationFailure(report, "source_changed", err)
	}
	parent, err := migrationDirectory(filepath.Dir(request.DestinationStore), true)
	if err != nil {
		return migrationFailure(report, "destination_parent_invalid", err)
	}
	defer parent.Close()
	if migrationSameFile(sourceDir, parent) {
		return migrationFailure(report, "source_destination_alias", ErrInvalidConfiguration)
	}
	if alias, err := migrationMountAlias(request.SourceStore, filepath.Dir(request.DestinationStore)); err != nil || alias {
		return migrationFailure(report, "source_destination_alias", ErrInvalidConfiguration)
	}
	marker := migrationMarker{Schema: MigrationRequestSchema, OperationSHA256: report.OperationSHA256, RequestSHA256: report.RequestSHA256, TargetBasenameSHA256: migrationSHA([]byte(filepath.Base(request.DestinationStore))), Source: request.Source}
	if mode != MigrationExecute {
		return verifyMigration(ctx, request, keyRaw, key, sourceLock, source, parent, marker, anchor, mode, report)
	}
	report.Stage = "destination_reserve"
	destination, lock, err := reserveMigration(parent, request.DestinationStore, marker, report)
	if err != nil {
		return migrationFailure(report, "destination_reservation_failed", err)
	}
	defer destination.Close()
	defer releaseStoreLock(lock)
	if err := migrationFaultPoint(ctx, "reserved"); err != nil {
		return migrationFailure(report, "interrupted", err)
	}
	// This private capability is the only marker bypass. It verifies the exact
	// inode of the already-held lock and the operation-bound marker.
	store, err := reservedMigrationStore(destination, lock, marker)
	if err != nil {
		return migrationFailure(report, "destination_capability_invalid", err)
	}
	report.Stage = "convert"
	sourceHash := sha256.New()
	sourceCount := &migrationLimitWriter{ctx: ctx, writer: sourceHash, remaining: migrationMaxPlaintext}
	reader, writer := io.Pipe()
	finished := make(chan error, 1)
	stopCancellation := context.AfterFunc(ctx, func() {
		_ = reader.CloseWithError(ctx.Err())
		_ = writer.CloseWithError(ctx.Err())
	})
	go func() {
		err := source.decrypt(ctx, io.MultiWriter(&migrationPipeWriter{ctx: ctx, writer: writer}, sourceCount))
		_ = writer.CloseWithError(err)
		finished <- err
	}()
	createErr := store.Create(request.ExpectedIdentity, key, reader)
	_ = reader.CloseWithError(createErr)
	decryptErr := <-finished
	stopCancellation()
	if err := errors.Join(createErr, decryptErr); err != nil {
		return migrationFailure(report, "conversion_failed", err)
	}
	if err := migrationFaultPoint(ctx, "converted"); err != nil {
		return migrationFailure(report, "interrupted", err)
	}
	report.Stage = "target_authenticate"
	targetHash := sha256.New()
	targetCount := &migrationLimitWriter{ctx: ctx, writer: targetHash, remaining: migrationMaxPlaintext}
	if err := store.loadContext(ctx, request.ExpectedIdentity, map[uint64]*RootKey{key.Generation(): key}, targetCount); err != nil {
		return migrationFailure(report, "target_authentication_failed", err)
	}
	report.TargetAuthenticated = true
	if sourceCount.count != targetCount.count || !bytes.Equal(sourceHash.Sum(nil), targetHash.Sum(nil)) {
		return migrationFailure(report, "plaintext_mismatch", ErrProfileCorrupt)
	}
	report.PlaintextEqual = true
	if err := verifyMigrationSource(ctx, request, keyRaw, key, sourceLock, source); err != nil {
		return migrationFailure(report, "source_changed", err)
	}
	report.SourceUnchanged = true
	report.Stage = "retention"
	if err := setMigrationRetention(destination, request.ExpectedIdentity, anchor); err != nil {
		return migrationFailure(report, "retention_write_failed", err)
	}
	target, err := openMigrationSnapshot(ctx, destination, request.ExpectedIdentity, key, storageContractV2)
	if err != nil {
		return migrationFailure(report, "target_snapshot_invalid", err)
	}
	defer target.close()
	report.Target = target.fingerprint
	if err := validateMigrationInventory(destination, request.ExpectedIdentity, target.checkpoint); err != nil {
		return migrationFailure(report, "target_inventory_invalid", err)
	}
	if report.Source.MetadataSHA256 == report.Target.MetadataSHA256 || report.Source.CheckpointIDSHA256 == report.Target.CheckpointIDSHA256 || report.Source.PayloadSHA256 == report.Target.PayloadSHA256 {
		return migrationFailure(report, "target_not_fresh", ErrProfileCorrupt)
	}
	report.Stage = "receipt"
	report.ElapsedMillis = time.Since(started).Milliseconds()
	receipt := *report
	receipt.Stage, receipt.Status = "verified", "complete"
	receiptRaw, err := json.Marshal(receipt)
	if err != nil {
		return migrationFailure(report, "receipt_invalid", err)
	}
	if err := migrationWriteAt(destination, migrationReceiptName, receiptRaw); err != nil {
		return migrationFailure(report, "receipt_write_failed", err)
	}
	if err := syncMigrationTree(destination, parent); err != nil {
		return migrationFailure(report, "durability_uncertain", err)
	}
	if err := migrationFaultPoint(ctx, "receipt_durable"); err != nil {
		return migrationFailure(report, "interrupted", err)
	}
	if err := verifyMigrationSource(ctx, request, keyRaw, key, sourceLock, source); err != nil {
		return migrationFailure(report, "source_changed", err)
	}
	return activateMigration(ctx, destination, parent, marker, report)
}

func migrationFailure(report *MigrationReport, code string, err error) error {
	report.FailureCode, report.FailureType = code, "validation_or_io"
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		report.FailureCode, report.FailureType = "cancelled", "cancellation"
	}
	return ErrProfileMigrationFailed
}

func reserveMigration(parent *os.File, targetPath string, marker migrationMarker, report *MigrationReport) (_ *os.File, _ *os.File, resultErr error) {
	var random [16]byte
	if _, err := io.ReadFull(rand.Reader, random[:]); err != nil {
		return nil, nil, err
	}
	name := ".migration-reservation-" + hex.EncodeToString(random[:])
	if err := unix.Mkdirat(int(parent.Fd()), name, 0o700); err != nil {
		return nil, nil, err
	}
	// No failure path removes a reservation, marker or encrypted output.
	dir, err := migrationChildDirectory(parent, name)
	if err != nil {
		return nil, nil, err
	}
	defer func() {
		if resultErr != nil {
			_ = dir.Close()
		}
	}()
	raw, err := json.Marshal(marker)
	if err != nil {
		return nil, nil, err
	}
	if err := migrationWriteAt(dir, migrationPendingName, raw); err != nil {
		return nil, nil, err
	}
	if err := migrationWriteAt(dir, ".manager.lock", nil); err != nil {
		return nil, nil, err
	}
	lock, err := migrationLock(dir)
	if err != nil {
		return nil, nil, err
	}
	defer func() {
		if resultErr != nil {
			_ = releaseStoreLock(lock)
		}
	}()
	if err := dir.Sync(); err != nil {
		return nil, nil, err
	}
	if err := unix.Renameat2(int(parent.Fd()), name, int(parent.Fd()), filepath.Base(targetPath), unix.RENAME_NOREPLACE); err != nil {
		// Do not infer unchanged state from an uncertain syscall error.
		if final, openErr := migrationChildDirectory(parent, filepath.Base(targetPath)); openErr == nil {
			report.Published = migrationSameFile(dir, final)
			_ = final.Close()
		}
		return nil, nil, err
	}
	report.Published = true
	if err := parent.Sync(); err != nil {
		return nil, nil, err
	}
	final, err := migrationChildDirectory(parent, filepath.Base(targetPath))
	if err != nil {
		return nil, nil, err
	}
	if !migrationSameFile(dir, final) {
		_ = final.Close()
		return nil, nil, ErrProfileCorrupt
	}
	_ = dir.Close()
	return final, lock, nil
}

func checkMigrationMarker(dir *os.File, expected migrationMarker) (bool, error) {
	raw, err := migrationReadFile(dir, migrationPendingName, migrationMaxRequest)
	if errors.Is(err, unix.ENOENT) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var marker migrationMarker
	if err := strictMigrationJSON(raw, &marker); err != nil || marker != expected {
		return false, ErrProfileCorrupt
	}
	return true, nil
}

func reservedMigrationStore(dir, lock *os.File, expected migrationMarker) (*Store, error) {
	actualLock, err := migrationFile(dir, ".manager.lock", 0)
	if err != nil {
		return nil, err
	}
	defer actualLock.Close()
	if !migrationSameFile(lock, actualLock) {
		return nil, ErrProfileCorrupt
	}
	pending, err := checkMigrationMarker(dir, expected)
	if err != nil || !pending {
		return nil, ErrProfileMigrationPending
	}
	for _, name := range []string{"profiles", "quarantine"} {
		if err := unix.Mkdirat(int(dir.Fd()), name, 0o700); err != nil {
			return nil, err
		}
	}
	protector := newProtector(nil)
	return &Store{root: dir.Name(), protector: protector, random: protector.random, now: time.Now, lockFile: lock, retainFailedMigration: true}, nil
}

func verifyMigrationSource(ctx context.Context, request MigrationRequest, originalKey []byte, key *RootKey, lock *os.File, source *migrationSnapshotReader) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	actualLock, err := migrationFile(source.root, ".manager.lock", 0)
	if err != nil {
		return err
	}
	defer actualLock.Close()
	if !migrationSameFile(lock, actualLock) {
		return ErrProfileCorrupt
	}
	raw, err := migrationAbsoluteFile(request.RootKeyFile, 32)
	if err != nil {
		return err
	}
	defer clear(raw)
	if !bytes.Equal(originalKey, raw) {
		return ErrProfileCorrupt
	}
	return source.verify(ctx, request.ExpectedIdentity, key, storageContractV1)
}

func setMigrationRetention(dir *os.File, identity Identity, anchor time.Time) error {
	profile, err := migrationChildDirectory(dir, "profiles")
	if err != nil {
		return err
	}
	defer profile.Close()
	leaf, err := migrationChildDirectory(profile, profileDigest(identity))
	if err != nil {
		return err
	}
	defer leaf.Close()
	times := []unix.Timespec{{Sec: 0, Nsec: unix.UTIME_OMIT}, unix.NsecToTimespec(anchor.UnixNano())}
	if err := unix.UtimesNanoAt(int(leaf.Fd()), currentFileName, times, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	file, err := migrationFile(leaf, currentFileName, maxCurrentBytes)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}

func validateMigrationInventory(dir *os.File, identity Identity, checkpoint string) error {
	fd, err := unix.Openat(int(dir.Fd()), ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	rootView := os.NewFile(uintptr(fd), dir.Name())
	rootEntries, rootErr := rootView.ReadDir(-1)
	_ = rootView.Close()
	if rootErr != nil {
		return rootErr
	}
	for _, entry := range rootEntries {
		switch entry.Name() {
		case "profiles", "quarantine", ".manager.lock", migrationPendingName, migrationReceiptName:
		default:
			return ErrProfileCorrupt
		}
	}
	profiles, err := migrationChildDirectory(dir, "profiles")
	if err != nil {
		return err
	}
	defer profiles.Close()
	entries, err := profiles.ReadDir(-1)
	if err != nil || len(entries) != 1 || entries[0].Name() != profileDigest(identity) || !entries[0].IsDir() {
		return ErrProfileCorrupt
	}
	profile, err := migrationChildDirectory(profiles, entries[0].Name())
	if err != nil {
		return err
	}
	defer profile.Close()
	entries, err = profile.ReadDir(-1)
	if err != nil || len(entries) != 2 {
		return ErrProfileCorrupt
	}
	for _, entry := range entries {
		if entry.Name() != "checkpoints" && entry.Name() != currentFileName {
			return ErrProfileCorrupt
		}
	}
	checkpoints, err := migrationChildDirectory(profile, "checkpoints")
	if err != nil {
		return err
	}
	defer checkpoints.Close()
	entries, err = checkpoints.ReadDir(-1)
	if err != nil || len(entries) != 1 || entries[0].Name() != checkpoint {
		return ErrProfileCorrupt
	}
	leaf, err := migrationChildDirectory(checkpoints, checkpoint)
	if err != nil {
		return err
	}
	defer leaf.Close()
	entries, err = leaf.ReadDir(-1)
	if err != nil || len(entries) != 2 {
		return ErrProfileCorrupt
	}
	for _, entry := range entries {
		if entry.Name() != metadataFileName && entry.Name() != payloadFileName {
			return ErrProfileCorrupt
		}
	}
	quarantine, err := migrationChildDirectory(dir, "quarantine")
	if err != nil {
		return err
	}
	defer quarantine.Close()
	entries, err = quarantine.ReadDir(-1)
	if err != nil || len(entries) != 0 {
		return ErrProfileCorrupt
	}
	return nil
}

func syncMigrationTree(dir, parent *os.File) error {
	// Walk only the new encrypted target; no plaintext is materialized here.
	var directories []string
	err := filepath.WalkDir(dir.Name(), func(path string, entry os.DirEntry, walkErr error) error {
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
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		return errors.Join(file.Sync(), file.Close())
	})
	if err != nil {
		return err
	}
	for index := len(directories) - 1; index >= 0; index-- {
		if err := syncDirectory(directories[index]); err != nil {
			return err
		}
	}
	return parent.Sync()
}

func activateMigration(ctx context.Context, dir, parent *os.File, marker migrationMarker, report *MigrationReport) error {
	report.Stage = "activation"
	if err := ctx.Err(); err != nil {
		return migrationFailure(report, "cancelled", err)
	}
	if err := migrationFaultPoint(ctx, "before_activation"); err != nil {
		return migrationFailure(report, "interrupted", err)
	}
	pending, err := checkMigrationMarker(dir, marker)
	if err != nil || !pending {
		return migrationFailure(report, "marker_mismatch", ErrProfileCorrupt)
	}
	if err := unix.Unlinkat(int(dir.Fd()), migrationPendingName, 0); err != nil {
		return migrationFailure(report, "activation_failed", err)
	}
	// Once unlink succeeds the runtime may observe an activatable target after
	// our lock closes, even if durability or output subsequently fails.
	report.ActivationReady = true
	if err := migrationFaultPoint(ctx, "marker_removed"); err != nil {
		return migrationFailure(report, "activation_durability_uncertain", err)
	}
	if err := errors.Join(dir.Sync(), parent.Sync()); err != nil {
		return migrationFailure(report, "activation_durability_uncertain", err)
	}
	report.Stage = "complete"
	return nil
}

func verifyMigration(ctx context.Context, request MigrationRequest, keyRaw []byte, key *RootKey, sourceLock *os.File, source *migrationSnapshotReader, parent *os.File, marker migrationMarker, anchor time.Time, mode MigrationMode, report *MigrationReport) error {
	report.Stage = "target_open"
	dir, err := migrationChildDirectory(parent, filepath.Base(request.DestinationStore))
	if err != nil {
		return migrationFailure(report, "target_unavailable", err)
	}
	defer dir.Close()
	report.Published = true
	if migrationSameFile(source.root, dir) {
		return migrationFailure(report, "source_destination_alias", ErrInvalidConfiguration)
	}
	lock, err := migrationLock(dir)
	if err != nil {
		return migrationFailure(report, "target_lock_unavailable", err)
	}
	defer releaseStoreLock(lock)
	pending, err := checkMigrationMarker(dir, marker)
	if err != nil {
		return migrationFailure(report, "marker_mismatch", err)
	}
	report.ActivationReady = !pending
	raw, err := migrationReadFile(dir, migrationReceiptName, migrationMaxRequest)
	if err != nil {
		return migrationFailure(report, "receipt_unavailable", err)
	}
	var receipt MigrationReport
	if err := strictMigrationJSON(raw, &receipt); err != nil {
		return migrationFailure(report, "receipt_invalid", err)
	}
	if receipt.Schema != migrationReportSchema || receipt.Status != "complete" || receipt.Stage != "verified" ||
		receipt.Mode != MigrationExecute || receipt.OperationSHA256 != report.OperationSHA256 || receipt.RequestSHA256 != report.RequestSHA256 ||
		receipt.IdentitySHA256 != report.IdentitySHA256 || receipt.Source != request.Source || receipt.Retention != request.Retention ||
		receipt.SourceContract != request.SourceContract || receipt.TargetContract != request.TargetContract ||
		receipt.ProfileGeneration != request.ExpectedIdentity.ProfileGeneration || receipt.RootKeyGeneration != request.RootKeyGeneration ||
		!receipt.SourceAuthenticated || !receipt.TargetAuthenticated || !receipt.PlaintextEqual || !receipt.SourceUnchanged || !receipt.Published || receipt.ActivationReady {
		return migrationFailure(report, "receipt_mismatch", ErrProfileCorrupt)
	}
	target, err := openMigrationSnapshot(ctx, dir, request.ExpectedIdentity, key, storageContractV2)
	if err != nil {
		return migrationFailure(report, "target_snapshot_invalid", err)
	}
	defer target.close()
	report.Target = target.fingerprint
	if target.fingerprint != receipt.Target || target.fingerprint.CurrentPointerMTime != anchor.UTC().Format(time.RFC3339Nano) {
		return migrationFailure(report, "target_precondition_mismatch", ErrProfileCorrupt)
	}
	if err := validateMigrationInventory(dir, request.ExpectedIdentity, target.checkpoint); err != nil {
		return migrationFailure(report, "target_inventory_invalid", err)
	}
	report.Stage = "target_authenticate"
	sourceHash, targetHash := sha256.New(), sha256.New()
	sourceCount := &migrationLimitWriter{ctx: ctx, writer: sourceHash, remaining: migrationMaxPlaintext}
	targetCount := &migrationLimitWriter{ctx: ctx, writer: targetHash, remaining: migrationMaxPlaintext}
	if err := source.decrypt(ctx, sourceCount); err != nil {
		return migrationFailure(report, "source_authentication_failed", err)
	}
	if err := target.decrypt(ctx, targetCount); err != nil {
		return migrationFailure(report, "target_authentication_failed", err)
	}
	report.TargetAuthenticated = true
	if sourceCount.count != targetCount.count || !bytes.Equal(sourceHash.Sum(nil), targetHash.Sum(nil)) {
		return migrationFailure(report, "plaintext_mismatch", ErrProfileCorrupt)
	}
	report.PlaintextEqual = true
	if err := verifyMigrationSource(ctx, request, keyRaw, key, sourceLock, source); err != nil {
		return migrationFailure(report, "source_changed", err)
	}
	if err := target.verify(ctx, request.ExpectedIdentity, key, storageContractV2); err != nil {
		return migrationFailure(report, "target_changed", err)
	}
	report.SourceUnchanged = true
	if mode == MigrationFinalize && pending {
		if err := syncMigrationTree(dir, parent); err != nil {
			return migrationFailure(report, "durability_uncertain", err)
		}
		return activateMigration(ctx, dir, parent, marker, report)
	}
	report.AlreadyFinalized = !pending
	report.Stage = "complete"
	return nil
}

// Unexported and context-scoped so fault-injection tests do not change global
// behavior or introduce a user-accessible bypass/recovery switch.
type migrationFaultKey struct{}

type migrationPipeWriter struct {
	ctx    context.Context
	writer io.Writer
}

func (writer *migrationPipeWriter) Write(raw []byte) (int, error) {
	n, err := writer.writer.Write(raw)
	if err == nil {
		err = migrationFaultPoint(writer.ctx, "conversion_chunk")
	}
	return n, err
}

func migrationFaultPoint(ctx context.Context, stage string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if hook, ok := ctx.Value(migrationFaultKey{}).(func(string) error); ok {
		return hook(stage)
	}
	return nil
}
