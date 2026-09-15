//go:build linux

package browserprofile

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func supplementBoundedContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func supplementPending(t *testing.T, fixture migrationFixture) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(fixture.request.DestinationStore, migrationPendingName)); err != nil {
		t.Fatal("failed migration did not retain its activation blocker")
	}
	store, err := NewStore(fixture.request.DestinationStore)
	if store != nil {
		_ = store.Close()
	}
	if !errors.Is(err, ErrProfileMigrationPending) {
		t.Fatalf("normal Store accepted failed migration: %v", err)
	}
}

func supplementEncryptedBytes(t *testing.T, root string) int64 {
	t.Helper()
	var count int64
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.Contains(entry.Name(), "payload") {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		count += info.Size()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return count
}

func supplementNoConversionGoroutine(t *testing.T) {
	t.Helper()
	if !supplementWaitForConversionExit(2 * time.Second) {
		t.Fatal("conversion goroutine did not exit within 2 seconds after migration returned")
	}
}

func supplementWaitForConversionExit(timeout time.Duration) bool {
	// The result send is the conversion goroutine's final operation, but the
	// receiver may return before the sender finishes its runtime epilogue.
	// Poll for actual exit; a blocked converter must still fail at the deadline.
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	stacks := make([]byte, 1<<20)
	for {
		count := runtime.Stack(stacks, true)
		for count == len(stacks) {
			stacks = make([]byte, 2*len(stacks))
			count = runtime.Stack(stacks, true)
		}
		if !bytes.Contains(stacks[:count], []byte("browserprofile.executeProfileMigration.func")) {
			return true
		}
		select {
		case <-deadline.C:
			return false
		case <-tick.C:
		}
	}
}

func TestMigrationSupplementExitCheckDetectsBlockedConversion(t *testing.T) {
	fixture := newMigrationFixture(t)
	base, cancel := context.WithCancel(supplementBoundedContext(t))
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	stop := func() {
		cancel()
		releaseOnce.Do(func() { close(release) })
	}
	ctx := context.WithValue(base, migrationFaultKey{}, func(stage string) error {
		if stage == "conversion_chunk" {
			close(entered)
			<-release
			return base.Err()
		}
		return nil
	})
	var report MigrationReport
	var err error
	go func() {
		defer close(done)
		report, err = ExecuteProfileMigration(ctx, fixture.path, MigrationExecute)
	}()
	t.Cleanup(func() {
		stop()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("blocked migration did not stop during cleanup")
		}
	})
	select {
	case <-entered:
	case <-base.Done():
		t.Fatal("migration never reached a real conversion chunk")
	}
	if supplementWaitForConversionExit(25 * time.Millisecond) {
		t.Fatal("exit check accepted a conversion goroutine held inside a real chunk")
	}
	stop()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("migration did not return after releasing the conversion chunk")
	}
	if err == nil || report.Stage != "convert" || report.FailureType != "cancellation" {
		t.Fatalf("released converter did not report cancellation: %+v", report)
	}
	supplementNoConversionGoroutine(t)
}

func TestMigrationSupplementCancellationAfterActualConversionChunk(t *testing.T) {
	fixture := newMigrationFixture(t)
	before := migrationTreeProof(t, fixture.request.SourceStore)
	base, cancel := context.WithCancel(supplementBoundedContext(t))
	defer cancel()
	chunks := 0
	ctx := context.WithValue(base, migrationFaultKey{}, func(stage string) error {
		if stage == "conversion_chunk" {
			chunks++
			cancel()
			return base.Err()
		}
		return nil
	})
	report, err := ExecuteProfileMigration(ctx, fixture.path, MigrationExecute)
	if err == nil || chunks == 0 || report.Stage != "convert" || report.FailureType != "cancellation" || !report.SourceAuthenticated || !report.Published || report.ActivationReady {
		t.Fatalf("conversion cancellation lacked a positive chunk control: %+v chunks=%d", report, chunks)
	}
	supplementNoConversionGoroutine(t)
	supplementPending(t, fixture)
	if supplementEncryptedBytes(t, fixture.request.DestinationStore) == 0 {
		t.Fatal("conversion cancellation removed its partial encrypted output")
	}
	if before != migrationTreeProof(t, fixture.request.SourceStore) {
		t.Fatal("conversion cancellation changed source")
	}
}

func TestMigrationSupplementCiphertextGrowsAfterAuthentication(t *testing.T) {
	fixture := newMigrationFixture(t)
	var afterMutation string
	ctx := context.WithValue(supplementBoundedContext(t), migrationFaultKey{}, func(stage string) error {
		if stage != "reserved" {
			return nil
		}
		// This deliberate test-only mutation occurs after initial authentication.
		file, err := os.OpenFile(fixture.payloadPath, os.O_WRONLY|os.O_APPEND, 0)
		if err != nil {
			return err
		}
		_, writeErr := file.Write([]byte{1})
		err = errors.Join(writeErr, file.Close())
		afterMutation = migrationTreeProof(t, fixture.request.SourceStore)
		return err
	})
	report, err := ExecuteProfileMigration(ctx, fixture.path, MigrationExecute)
	if err == nil || afterMutation == "" || !report.SourceAuthenticated || !report.Published || report.ActivationReady || report.Stage != "convert" {
		t.Fatalf("growing ciphertext was accepted: %+v", report)
	}
	supplementNoConversionGoroutine(t)
	supplementPending(t, fixture)
	if supplementEncryptedBytes(t, fixture.request.DestinationStore) == 0 {
		t.Fatal("trailing ciphertext failure removed partial encrypted output")
	}
	if afterMutation != migrationTreeProof(t, fixture.request.SourceStore) {
		t.Fatal("migration altered source after the deliberate fixture mutation")
	}
}

func TestMigrationSupplementCiphertextSizeLimitRejectsBeforeReservation(t *testing.T) {
	fixture := newMigrationFixture(t)
	// Sparse extension tests the actual 512 MiB metadata boundary without
	// allocating or reading 512 MiB into memory or consuming that disk space.
	if err := os.Truncate(fixture.payloadPath, migrationMaxCiphertext+1); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(fixture.payloadPath)
	if err != nil {
		t.Fatal(err)
	}
	report, err := ExecuteProfileMigration(supplementBoundedContext(t), fixture.path, MigrationExecute)
	if err == nil || report.Published || report.ActivationReady || report.FailureCode != "source_snapshot_invalid" {
		t.Fatalf("oversize ciphertext was not refused: %+v", report)
	}
	after, err := os.Stat(fixture.payloadPath)
	if err != nil || before.Size() != after.Size() || before.Mode() != after.Mode() || !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("oversize ciphertext was changed or quarantined")
	}
	if _, err := os.Stat(fixture.request.DestinationStore); !os.IsNotExist(err) {
		t.Fatal("oversize ciphertext created a target")
	}
}

type supplementWriter func([]byte) (int, error)

func (writer supplementWriter) Write(raw []byte) (int, error) { return writer(raw) }

func TestMigrationSupplementBoundedPlaintextAndShortWrite(t *testing.T) {
	var output bytes.Buffer
	writer := &migrationLimitWriter{ctx: context.Background(), writer: &output, remaining: 3}
	if n, err := writer.Write([]byte("four")); n != 0 || err == nil || writer.count != 0 || output.Len() != 0 {
		t.Fatal("plaintext limit wrote excess bytes or reported success")
	}
	if n, err := writer.Write([]byte("one")); n != 3 || err != nil || writer.count != 3 || writer.remaining != 0 {
		t.Fatal("exact plaintext limit was rejected")
	}
	if _, err := writer.Write([]byte("x")); err == nil || output.String() != "one" {
		t.Fatal("plaintext limit was bypassed on a later chunk")
	}
	for _, result := range []int{0, -1, 5} {
		bounded := &migrationLimitWriter{ctx: context.Background(), writer: supplementWriter(func([]byte) (int, error) { return result, nil }), remaining: 4}
		if err := writeAll(bounded, []byte("four")); !errors.Is(err, io.ErrShortWrite) {
			t.Fatalf("invalid/zero-progress writer result %d was accepted: %v", result, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	writer = &migrationLimitWriter{ctx: ctx, writer: &output, remaining: 100}
	if n, err := writer.Write([]byte("private")); n != 0 || !errors.Is(err, context.Canceled) || output.String() != "one" {
		t.Fatal("canceled plaintext writer wrote more data")
	}
}

func TestMigrationSupplementIncompleteOrWrongOperationCannotFinalize(t *testing.T) {
	for _, condition := range []string{"missing-receipt", "wrong-operation"} {
		t.Run(condition, func(t *testing.T) {
			fixture := newMigrationFixture(t)
			sourceBefore := migrationTreeProof(t, fixture.request.SourceStore)
			ctx := context.WithValue(supplementBoundedContext(t), migrationFaultKey{}, func(stage string) error {
				if stage == "receipt_durable" {
					return errors.New("synthetic interruption before activation")
				}
				return nil
			})
			report, err := ExecuteProfileMigration(ctx, fixture.path, MigrationExecute)
			if err == nil || !report.Published || report.ActivationReady || !report.PlaintextEqual {
				t.Fatalf("did not establish complete pending control: %+v", report)
			}
			if condition == "missing-receipt" {
				if err := os.Remove(filepath.Join(fixture.request.DestinationStore, migrationReceiptName)); err != nil {
					t.Fatal(err)
				}
			} else {
				fixture.request.OperationID = "88888888-8888-4888-8888-888888888888"
				fixture.write(t)
			}
			before := migrationTreeProof(t, fixture.request.DestinationStore)
			for _, mode := range []MigrationMode{MigrationCheck, MigrationFinalize} {
				report, err := ExecuteProfileMigration(supplementBoundedContext(t), fixture.path, mode)
				wantPublished := condition == "missing-receipt" // The exact pending marker still binds this request.
				if err == nil || report.ActivationReady || report.Published != wantPublished {
					t.Fatalf("%s accepted %s: %+v", mode, condition, report)
				}
				if before != migrationTreeProof(t, fixture.request.DestinationStore) || sourceBefore != migrationTreeProof(t, fixture.request.SourceStore) {
					t.Fatal("rejected check/finalize changed encrypted input or target")
				}
			}
			supplementPending(t, fixture)
		})
	}
}

func TestMigrationSupplementActualTmpfsENOSPCRetainsEncryptedFailure(t *testing.T) {
	if os.Getenv("OPENLINKER_TEST_MIGRATION_ENOSPC") != "isolated-tmpfs" {
		t.Skip("requires an explicitly isolated synthetic tmpfs; never fills a host filesystem")
	}
	fixture := newMigrationFixture(t)
	before := migrationTreeProof(t, fixture.request.SourceStore)
	filler, err := os.CreateTemp(filepath.Dir(fixture.path), "synthetic-enospc-")
	if err != nil {
		t.Fatal(err)
	}
	defer filler.Close()
	var consumed bool
	ctx := context.WithValue(supplementBoundedContext(t), migrationFaultKey{}, func(stage string) error {
		if stage != "reserved" {
			return nil
		}
		var state unix.Statfs_t
		if err := unix.Fstatfs(int(filler.Fd()), &state); err != nil {
			return err
		}
		available := int64(state.Bavail) * int64(state.Bsize)
		if state.Type != unix.TMPFS_MAGIC || available < 4<<20 || available > 256<<20 {
			return errors.New("test requires a bounded dedicated tmpfs")
		}
		// Real fallocate consumes tmpfs blocks. The migration then encounters
		// actual filesystem exhaustion while encrypting its first 1 MiB chunk.
		if err := unix.Fallocate(int(filler.Fd()), 0, 0, available-(768<<10)); err != nil {
			return err
		}
		consumed = true
		return nil
	})
	report, err := ExecuteProfileMigration(ctx, fixture.path, MigrationExecute)
	if err == nil || !consumed || report.Stage != "convert" || report.FailureCode != "conversion_failed" || !report.Published || report.ActivationReady {
		t.Fatalf("actual tmpfs exhaustion did not fail conversion: %+v consumed=%v", report, consumed)
	}
	var state unix.Statfs_t
	if err := unix.Fstatfs(int(filler.Fd()), &state); err != nil || state.Bavail > 1 {
		t.Fatal("failed conversion did not exhaust the dedicated tmpfs")
	}
	supplementNoConversionGoroutine(t)
	supplementPending(t, fixture)
	if supplementEncryptedBytes(t, fixture.request.DestinationStore) == 0 {
		t.Fatal("ENOSPC removed the partial encrypted failure output")
	}
	if before != migrationTreeProof(t, fixture.request.SourceStore) {
		t.Fatal("ENOSPC changed the source")
	}
}
