//go:build linux

package browserprofile

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func migrationTargetFile(t *testing.T, fixture migrationFixture, name string) string {
	t.Helper()
	profile := filepath.Join(fixture.request.DestinationStore, "profiles", profileDigest(fixture.request.ExpectedIdentity))
	raw, err := os.ReadFile(filepath.Join(profile, currentFileName))
	if err != nil {
		t.Fatal(err)
	}
	var pointer currentRecord
	if err := json.Unmarshal(raw, &pointer); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(profile, "checkpoints", pointer.Checkpoint, name)
}

func corruptMigrationTarget(t *testing.T, fixture migrationFixture, name string) {
	t.Helper()
	path := migrationTargetFile(t, fixture, name)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if name == metadataFileName {
		var metadata Metadata
		if err := json.Unmarshal(raw, &metadata); err != nil {
			t.Fatal(err)
		}
		metadata.WrappedDEK[0] ^= 1
		raw, err = json.Marshal(metadata)
		if err != nil {
			t.Fatal(err)
		}
	} else {
		raw[len(raw)-1] ^= 1
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestMigrationTargetAuthenticationFailurePreservesOriginalEvidence(t *testing.T) {
	for _, name := range []string{payloadFileName, metadataFileName} {
		t.Run(name, func(t *testing.T) {
			fixture := newMigrationFixture(t)
			sourceBefore := migrationTreeProof(t, fixture.request.SourceStore)
			var targetBefore string
			ctx := context.WithValue(context.Background(), migrationFaultKey{}, func(stage string) error {
				if stage == "converted" {
					corruptMigrationTarget(t, fixture, name)
					targetBefore = migrationTreeProof(t, fixture.request.DestinationStore)
				}
				return nil
			})
			report, err := ExecuteProfileMigration(ctx, fixture.path, MigrationExecute)
			if err == nil || report.Stage != "target_authenticate" || !report.Published || report.ActivationReady || report.TargetAuthenticated {
				t.Fatalf("corrupt target was accepted: %+v %v", report, err)
			}
			if targetBefore == "" || targetBefore != migrationTreeProof(t, fixture.request.DestinationStore) {
				t.Error("failed verification moved or changed target ciphertext, permissions or metadata")
			}
			if sourceBefore != migrationTreeProof(t, fixture.request.SourceStore) {
				t.Error("failed target verification changed source")
			}
			if _, err := os.Stat(filepath.Join(fixture.request.DestinationStore, "profiles", profileDigest(fixture.request.ExpectedIdentity))); err != nil {
				t.Error("failed target is no longer at its original Profile path")
			}
			entries, err := os.ReadDir(filepath.Join(fixture.request.DestinationStore, "quarantine"))
			if err != nil || len(entries) != 0 {
				t.Error("migration target was quarantined")
			}
			if _, err := os.Stat(filepath.Join(fixture.request.DestinationStore, migrationPendingName)); err != nil {
				t.Fatal("failed target lost pending marker")
			}
			beforeCheck := migrationTreeProof(t, fixture.request.DestinationStore)
			for _, mode := range []MigrationMode{MigrationCheck, MigrationFinalize} {
				checked, err := ExecuteProfileMigration(context.Background(), fixture.path, mode)
				if err == nil || !checked.Published || checked.ActivationReady {
					t.Fatalf("incomplete matching reservation accepted: %+v %v", checked, err)
				}
			}
			if beforeCheck != migrationTreeProof(t, fixture.request.DestinationStore) {
				t.Error("check/finalize changed failed target")
			}
			if store, err := NewStore(fixture.request.DestinationStore); !errors.Is(err, ErrProfileMigrationPending) {
				if store != nil {
					store.Close()
				}
				t.Fatalf("Runtime accepted failed target: %v", err)
			}
		})
	}
}

func TestMigrationVerificationFlagsRequireOperationEvidence(t *testing.T) {
	for _, condition := range []string{"unrelated-empty", "unrelated-store", "wrong-pending-operation", "wrong-finalized-operation", "missing-finalized-receipt", "invalid-finalized-receipt"} {
		t.Run(condition, func(t *testing.T) {
			fixture := newMigrationFixture(t)
			switch condition {
			case "unrelated-empty":
				if err := os.Mkdir(fixture.request.DestinationStore, 0o700); err != nil {
					t.Fatal(err)
				}
			case "unrelated-store":
				store, err := NewStore(fixture.request.DestinationStore)
				if err != nil {
					t.Fatal(err)
				}
				store.Close()
			default:
				ctx := context.Background()
				if condition == "wrong-pending-operation" {
					ctx = context.WithValue(ctx, migrationFaultKey{}, func(stage string) error {
						if stage == "receipt_durable" {
							return errors.New("synthetic interruption")
						}
						return nil
					})
				}
				report, err := ExecuteProfileMigration(ctx, fixture.path, MigrationExecute)
				if condition == "wrong-pending-operation" {
					if err == nil || !report.Published || report.ActivationReady {
						t.Fatalf("cannot prepare pending fixture: %+v %v", report, err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
				switch condition {
				case "wrong-pending-operation", "wrong-finalized-operation":
					fixture.request.OperationID = "88888888-8888-4888-8888-888888888888"
					fixture.write(t)
				case "missing-finalized-receipt":
					if err := os.Remove(filepath.Join(fixture.request.DestinationStore, migrationReceiptName)); err != nil {
						t.Fatal(err)
					}
				case "invalid-finalized-receipt":
					if err := os.WriteFile(filepath.Join(fixture.request.DestinationStore, migrationReceiptName), []byte("{}"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
			before := migrationTreeProof(t, fixture.request.DestinationStore)
			for _, mode := range []MigrationMode{MigrationCheck, MigrationFinalize} {
				report, err := ExecuteProfileMigration(context.Background(), fixture.path, mode)
				if err == nil || report.Published || report.ActivationReady {
					t.Errorf("%s attributed unrelated target to request: %+v %v", mode, report, err)
				}
			}
			if before != migrationTreeProof(t, fixture.request.DestinationStore) {
				t.Fatal("verification changed unrelated target")
			}
		})
	}
}

func TestMigrationFinalizedTargetRequiresAuthenticationForActivationReport(t *testing.T) {
	fixture := newMigrationFixture(t)
	if report, err := ExecuteProfileMigration(context.Background(), fixture.path, MigrationExecute); err != nil || !report.ActivationReady {
		t.Fatalf("prepare finalized target: %+v %v", report, err)
	}
	corruptMigrationTarget(t, fixture, payloadFileName)
	before := migrationTreeProof(t, fixture.request.DestinationStore)
	for _, mode := range []MigrationMode{MigrationCheck, MigrationFinalize} {
		report, err := ExecuteProfileMigration(context.Background(), fixture.path, mode)
		if err == nil || !report.Published || report.ActivationReady {
			t.Errorf("%s considered changed finalized target ready: %+v %v", mode, report, err)
		}
	}
	if before != migrationTreeProof(t, fixture.request.DestinationStore) {
		t.Fatal("verification changed finalized target")
	}
}

func TestMigrationVerificationFlagsForMatchingPendingAndFinalizedTargets(t *testing.T) {
	fixture := newMigrationFixture(t)
	ctx := context.WithValue(context.Background(), migrationFaultKey{}, func(stage string) error {
		if stage == "receipt_durable" {
			return errors.New("synthetic interruption")
		}
		return nil
	})
	if report, err := ExecuteProfileMigration(ctx, fixture.path, MigrationExecute); err == nil || !report.Published || report.ActivationReady {
		t.Fatalf("prepare complete pending target: %+v %v", report, err)
	}
	before := migrationTreeProof(t, fixture.request.DestinationStore)
	if report, err := ExecuteProfileMigration(context.Background(), fixture.path, MigrationCheck); err != nil || !report.Published || report.ActivationReady || report.AlreadyFinalized || !report.TargetAuthenticated {
		t.Fatalf("check matching pending target: %+v %v", report, err)
	}
	if before != migrationTreeProof(t, fixture.request.DestinationStore) {
		t.Fatal("pending check changed target")
	}
	if report, err := ExecuteProfileMigration(context.Background(), fixture.path, MigrationFinalize); err != nil || !report.Published || !report.ActivationReady {
		t.Fatalf("finalize matching target: %+v %v", report, err)
	}
	before = migrationTreeProof(t, fixture.request.DestinationStore)
	for _, mode := range []MigrationMode{MigrationCheck, MigrationFinalize} {
		if report, err := ExecuteProfileMigration(context.Background(), fixture.path, mode); err != nil || !report.Published || !report.ActivationReady || !report.AlreadyFinalized {
			t.Fatalf("%s matching finalized target: %+v %v", mode, report, err)
		}
	}
	if before != migrationTreeProof(t, fixture.request.DestinationStore) {
		t.Fatal("finalized verification changed target")
	}
}

func TestMigrationInitialVerificationRejectsNormalCheckpointOrMTimeAdvance(t *testing.T) {
	for _, advance := range []string{"checkpoint", "pointer-mtime"} {
		t.Run(advance, func(t *testing.T) {
			fixture := newMigrationFixture(t)
			if report, err := ExecuteProfileMigration(context.Background(), fixture.path, MigrationExecute); err != nil || !report.ActivationReady {
				t.Fatalf("prepare finalized target: %+v %v", report, err)
			}
			if advance == "checkpoint" {
				store, err := NewStore(fixture.request.DestinationStore)
				if err != nil {
					t.Fatal(err)
				}
				key := testRootKey(t, fixture.request.RootKeyGeneration, 0x11)
				err = store.Checkpoint(fixture.request.ExpectedIdentity, map[uint64]*RootKey{key.Generation(): key}, bytes.NewReader(fixture.plaintext))
				key.Close()
				closeErr := store.Close()
				if err != nil || closeErr != nil {
					t.Fatalf("normal checkpoint: %v, close: %v", err, closeErr)
				}
			} else {
				previous, err := time.Parse(time.RFC3339Nano, fixture.request.Source.CurrentPointerMTime)
				if err != nil {
					t.Fatal(err)
				}
				next := previous.Add(time.Minute) // Still recent and in the past; not an expiry/clock failure.
				pointer := filepath.Join(fixture.request.DestinationStore, "profiles", profileDigest(fixture.request.ExpectedIdentity), currentFileName)
				if err := os.Chtimes(pointer, next, next); err != nil {
					t.Fatal(err)
				}
			}
			sourceBefore := migrationTreeProof(t, fixture.request.SourceStore)
			targetBefore := migrationTreeProof(t, fixture.request.DestinationStore)
			for _, mode := range []MigrationMode{MigrationCheck, MigrationFinalize} {
				report, err := ExecuteProfileMigration(context.Background(), fixture.path, mode)
				if err == nil || !report.Published || report.ActivationReady || report.FailureCode != "target_precondition_mismatch" {
					t.Errorf("%s accepted %s beyond the initial snapshot: %+v %v", mode, advance, report, err)
				}
			}
			if targetBefore != migrationTreeProof(t, fixture.request.DestinationStore) || sourceBefore != migrationTreeProof(t, fixture.request.SourceStore) {
				t.Fatal("rejected initial verification changed source or normally advanced target")
			}
		})
	}
}
