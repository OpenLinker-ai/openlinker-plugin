//go:build linux

package browserprofile

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type migrationFixture struct {
	request     MigrationRequest
	path        string
	payloadPath string
	plaintext   []byte
}

// Synthetic failure/race fixtures are deliberately distinct from the frozen
// historical golden producer used by golden_compatibility_test.go.
func newMigrationFixture(t *testing.T) migrationFixture {
	t.Helper()
	base := t.TempDir()
	if err := os.Chmod(base, 0o700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(base, "source")
	store, err := NewStore(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	identity := testIdentity()
	keyRaw := bytes.Repeat([]byte{0x11}, 32)
	key := testRootKey(t, 1, 0x11)
	defer key.Close()
	dek := bytes.Repeat([]byte{0x33}, 32)
	wrapping, err := deriveWrappingKeyForContract(identity, key, storageContractV1)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(wrapping)
	gcm, err := newGCM(wrapping)
	if err != nil {
		t.Fatal(err)
	}
	metadata := Metadata{Version: 1, ContractID: string(storageContractV1), Algorithm: algorithmName, KDF: kdfName, Identity: identity, RootKeyGeneration: 1, WrapNonce: bytes.Repeat([]byte{0x44}, 12)}
	metadata.WrappedDEK = gcm.Seal(nil, metadata.WrapNonce, dek, wrapAAD(metadata))
	metadataRaw, _ := json.Marshal(metadata)
	plaintext := bytes.Repeat([]byte("synthetic-profile-private-state\n"), 75000)
	gcm, err = newGCM(dek)
	if err != nil {
		t.Fatal(err)
	}
	prefix := bytes.Repeat([]byte{0x55}, noncePrefixSize)
	var payload bytes.Buffer
	payload.WriteString(payloadMagic)
	payload.Write(prefix)
	var index uint32
	for offset := 0; ; index++ {
		end := min(offset+payloadChunkSize, len(plaintext))
		chunk := plaintext[offset:end]
		var length [4]byte
		binary.BigEndian.PutUint32(length[:], uint32(len(chunk)))
		payload.Write(length[:])
		payload.Write(gcm.Seal(nil, payloadNonce(prefix, index), chunk, payloadAADForContract(identity, index, uint32(len(chunk)), len(chunk) == 0, storageContractV1)))
		if len(chunk) == 0 {
			break
		}
		offset = end
	}
	digest := profileDigestForContract(identity, storageContractV1)
	checkpoint := strings.Repeat("6", 32)
	profilePath := filepath.Join(source, "profiles", digest)
	snapshotPath := filepath.Join(profilePath, "checkpoints", checkpoint)
	if err := os.MkdirAll(snapshotPath, 0o700); err != nil {
		t.Fatal(err)
	}
	pointer, _ := json.Marshal(currentRecord{Version: 1, Checkpoint: checkpoint})
	for name, raw := range map[string][]byte{filepath.Join(snapshotPath, metadataFileName): metadataRaw, filepath.Join(snapshotPath, payloadFileName): payload.Bytes(), filepath.Join(profilePath, currentFileName): pointer, filepath.Join(base, "key"): keyRaw} {
		if err := os.WriteFile(name, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mtime := time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)
	if err := os.Chtimes(filepath.Join(profilePath, currentFileName), mtime, mtime); err != nil {
		t.Fatal(err)
	}
	request := MigrationRequest{Schema: MigrationRequestSchema, OperationID: "77777777-7777-4777-8777-777777777777", SourceContract: string(storageContractV1), TargetContract: string(storageContractV2), SourceStore: source, DestinationStore: filepath.Join(base, "target"), RootKeyFile: filepath.Join(base, "key"), ExpectedIdentity: identity, RootKeyGeneration: 1,
		Source: MigrationSnapshot{ProfileDirectorySHA256: digest, MetadataSHA256: migrationSHA(metadataRaw), CheckpointIDSHA256: migrationSHA([]byte(checkpoint)), PayloadSHA256: migrationSHA(payload.Bytes()), CurrentPointerMTime: mtime.Format(time.RFC3339Nano)}, Retention: MigrationRetention{Mode: "preserve"}}
	fixture := migrationFixture{request: request, path: filepath.Join(base, "request.json"), payloadPath: filepath.Join(snapshotPath, payloadFileName), plaintext: plaintext}
	fixture.write(t)
	return fixture
}

func (fixture *migrationFixture) write(t *testing.T) {
	t.Helper()
	raw, err := json.Marshal(fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture.path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func migrationTreeProof(t *testing.T, root string) string {
	t.Helper()
	var proof strings.Builder
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		proof.WriteString(rel + ":" + info.Mode().String() + ":" + info.ModTime().UTC().Format(time.RFC3339Nano) + ":")
		if info.Mode().IsRegular() {
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			proof.WriteString(migrationSHA(raw))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return migrationSHA([]byte(proof.String()))
}

func TestMigrationExecuteCheckAndNormalV2Load(t *testing.T) {
	fixture := newMigrationFixture(t)
	before := migrationTreeProof(t, fixture.request.SourceStore)
	report, err := ExecuteProfileMigration(context.Background(), fixture.path, MigrationExecute)
	if err != nil {
		t.Fatalf("execute: %+v, %v", report, err)
	}
	if !report.SourceAuthenticated || !report.TargetAuthenticated || !report.PlaintextEqual || !report.SourceUnchanged || !report.ActivationReady || !report.Published {
		t.Fatalf("incomplete evidence: %+v", report)
	}
	if report.Source.MetadataSHA256 == report.Target.MetadataSHA256 {
		t.Fatal("metadata was not freshly encrypted")
	}
	if before != migrationTreeProof(t, fixture.request.SourceStore) {
		t.Fatal("source changed")
	}
	targetBefore := migrationTreeProof(t, fixture.request.DestinationStore)
	for _, mode := range []MigrationMode{MigrationCheck, MigrationFinalize} {
		checked, err := ExecuteProfileMigration(context.Background(), fixture.path, mode)
		if err != nil || !checked.AlreadyFinalized || !checked.PlaintextEqual {
			t.Fatalf("%s: %+v %v", mode, checked, err)
		}
	}
	if targetBefore != migrationTreeProof(t, fixture.request.DestinationStore) {
		t.Fatal("read-only/already-finalized verification wrote target")
	}
	store, err := NewStore(fixture.request.DestinationStore)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	key := testRootKey(t, 1, 0x11)
	defer key.Close()
	var plaintext bytes.Buffer
	if err := store.Load(fixture.request.ExpectedIdentity, map[uint64]*RootKey{1: key}, &plaintext); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(plaintext.Bytes(), fixture.plaintext) {
		t.Fatal("normal v2 load changed archive bytes")
	}
	raw, _ := json.Marshal(report)
	for _, secret := range []string{fixture.request.SourceStore, fixture.request.ExpectedIdentity.AgentID, fixture.request.ExpectedIdentity.PrincipalScopeID, "wrapped_dek", "wrap_nonce", string(fixture.plaintext[:30])} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("report exposed private data")
		}
	}
}

func TestMigrationAuthenticationFailuresDoNotReserve(t *testing.T) {
	for _, mutation := range []string{"first", "middle", "last", "missing-final", "trailing", "wrong-key", "wrong-generation"} {
		t.Run(mutation, func(t *testing.T) {
			fixture := newMigrationFixture(t)
			raw, err := os.ReadFile(fixture.payloadPath)
			if err != nil {
				t.Fatal(err)
			}
			switch mutation {
			case "first":
				raw[len(payloadMagic)+noncePrefixSize+5] ^= 1
			case "middle":
				raw[len(raw)/2] ^= 1
			case "last":
				raw[len(raw)-1] ^= 1
			case "missing-final":
				raw = raw[:len(raw)-20]
			case "trailing":
				raw = append(raw, 1)
			case "wrong-key":
				if err := os.WriteFile(fixture.request.RootKeyFile, bytes.Repeat([]byte{0x12}, 32), 0o600); err != nil {
					t.Fatal(err)
				}
			case "wrong-generation":
				fixture.request.RootKeyGeneration = 2
			}
			if err := os.WriteFile(fixture.payloadPath, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			fixture.request.Source.PayloadSHA256 = migrationSHA(raw)
			fixture.write(t)
			before := migrationTreeProof(t, fixture.request.SourceStore)
			report, err := ExecuteProfileMigration(context.Background(), fixture.path, MigrationExecute)
			if err == nil || report.Published || report.ActivationReady {
				t.Fatalf("unsafe result: %+v", report)
			}
			if _, err := os.Stat(fixture.request.DestinationStore); !os.IsNotExist(err) {
				t.Fatal("failed source authentication created target")
			}
			if before != migrationTreeProof(t, fixture.request.SourceStore) {
				t.Fatal("bad source was mutated or quarantined")
			}
		})
	}
}

func TestMigrationCrashMarkersAndFinalize(t *testing.T) {
	for _, stage := range []string{"reserved", "converted", "receipt_durable", "before_activation", "marker_removed"} {
		t.Run(stage, func(t *testing.T) {
			fixture := newMigrationFixture(t)
			ctx := context.WithValue(context.Background(), migrationFaultKey{}, func(point string) error {
				if point == stage {
					return errors.New("synthetic interruption")
				}
				return nil
			})
			report, err := ExecuteProfileMigration(ctx, fixture.path, MigrationExecute)
			if err == nil || !report.Published {
				t.Fatalf("fault did not interrupt: %+v", report)
			}
			if stage != "marker_removed" {
				if store, err := NewStore(fixture.request.DestinationStore); !errors.Is(err, ErrProfileMigrationPending) {
					if store != nil {
						store.Close()
					}
					t.Fatalf("runtime accepted pending target: %v", err)
				}
			}
			before := migrationTreeProof(t, fixture.request.DestinationStore)
			checked, checkErr := ExecuteProfileMigration(context.Background(), fixture.path, MigrationCheck)
			if before != migrationTreeProof(t, fixture.request.DestinationStore) {
				t.Fatal("check wrote target")
			}
			complete := stage == "receipt_durable" || stage == "before_activation" || stage == "marker_removed"
			if complete != (checkErr == nil) {
				t.Fatalf("check completeness: %+v %v", checked, checkErr)
			}
			final, finalErr := ExecuteProfileMigration(context.Background(), fixture.path, MigrationFinalize)
			if complete != (finalErr == nil) || (complete && !final.ActivationReady) {
				t.Fatalf("finalize: %+v %v", final, finalErr)
			}
			if !complete {
				if _, err := os.Stat(filepath.Join(fixture.request.DestinationStore, migrationPendingName)); err != nil {
					t.Fatal("incomplete marker removed")
				}
			}
		})
	}
}

func TestMigrationExistingTargetsLocksAndLinks(t *testing.T) {
	for _, condition := range []string{"existing-empty", "existing-runtime", "source-lock-held", "source-lock-missing", "request-symlink", "request-hardlink", "payload-symlink", "nested-target"} {
		t.Run(condition, func(t *testing.T) {
			fixture := newMigrationFixture(t)
			switch condition {
			case "existing-empty":
				if err := os.Mkdir(fixture.request.DestinationStore, 0o700); err != nil {
					t.Fatal(err)
				}
			case "existing-runtime":
				store, err := NewStore(fixture.request.DestinationStore)
				if err != nil {
					t.Fatal(err)
				}
				defer store.Close()
			case "source-lock-held":
				store, err := NewStore(fixture.request.SourceStore)
				if err != nil {
					t.Fatal(err)
				}
				defer store.Close()
			case "source-lock-missing":
				if err := os.Remove(filepath.Join(fixture.request.SourceStore, ".manager.lock")); err != nil {
					t.Fatal(err)
				}
			case "request-symlink":
				path := fixture.path + ".link"
				if err := os.Symlink(fixture.path, path); err != nil {
					t.Fatal(err)
				}
				fixture.path = path
			case "request-hardlink":
				if err := os.Link(fixture.path, fixture.path+".hard"); err != nil {
					t.Fatal(err)
				}
			case "payload-symlink":
				path := fixture.payloadPath + ".real"
				if err := os.Rename(fixture.payloadPath, path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(path, fixture.payloadPath); err != nil {
					t.Fatal(err)
				}
			case "nested-target":
				fixture.request.DestinationStore = filepath.Join(fixture.request.SourceStore, "target")
				fixture.write(t)
			}
			report, err := ExecuteProfileMigration(context.Background(), fixture.path, MigrationExecute)
			if err == nil || report.ActivationReady || report.Published {
				t.Fatalf("accepted conflict: %+v", report)
			}
		})
	}
}

func TestMigrationNormalLegacyGuardAndPrune(t *testing.T) {
	fixture := newMigrationFixture(t)
	store, err := NewStore(fixture.request.SourceStore)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	key := testRootKey(t, 1, 0x11)
	defer key.Close()
	before := migrationTreeProof(t, fixture.request.SourceStore)
	if count, err := store.PruneInactive(time.Now()); err != nil || count != 0 {
		t.Fatalf("legacy prune = %d %v", count, err)
	}
	if err := store.Load(fixture.request.ExpectedIdentity, map[uint64]*RootKey{1: key}, io.Discard); !errors.Is(err, ErrProfileMigrationRequired) {
		t.Fatalf("load error: %v", err)
	}
	if err := store.Create(fixture.request.ExpectedIdentity, key, bytes.NewReader(nil)); !errors.Is(err, ErrProfileMigrationRequired) {
		t.Fatalf("create error: %v", err)
	}
	if before != migrationTreeProof(t, fixture.request.SourceStore) {
		t.Fatal("legacy guard modified source")
	}
}

func TestMigrationRequestStrictAndRetention(t *testing.T) {
	fixture := newMigrationFixture(t)
	raw, _ := os.ReadFile(fixture.path)
	for _, malformed := range [][]byte{append(append([]byte(nil), raw...), []byte("{}")...), []byte(strings.Replace(string(raw), `"schema":`, `"schema":"duplicate","schema":`, 1)), []byte(strings.Replace(string(raw), `"source_store":`, `"unknown":true,"source_store":`, 1))} {
		var request MigrationRequest
		if err := strictMigrationJSON(malformed, &request); err == nil {
			t.Fatal("accepted ambiguous JSON")
		}
	}
	fixture.request.Source.CurrentPointerMTime = time.Now().Add(-60 * 24 * time.Hour).UTC().Format(time.RFC3339Nano)
	if _, err := validateMigrationRequest(fixture.request, time.Now()); err == nil {
		t.Fatal("accepted expired preserve")
	}
	fixture.request.Retention = MigrationRetention{Mode: "restore-approved-anchor", Anchor: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano), AuthorizationSHA256: hex.EncodeToString(bytes.Repeat([]byte{1}, 32)), PriorRestorationReceiptSHA256: hex.EncodeToString(bytes.Repeat([]byte{2}, 32))}
	if _, err := validateMigrationRequest(fixture.request, time.Now()); err != nil {
		t.Fatal(err)
	}
	fixture.request.Retention.PriorRestorationReceiptSHA256 = ""
	if _, err := validateMigrationRequest(fixture.request, time.Now()); err == nil {
		t.Fatal("accepted unbound restoration")
	}
}
