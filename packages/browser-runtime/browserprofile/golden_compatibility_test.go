//go:build unix

package browserprofile

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

type compatibilityGolden struct {
	Schema                   string   `json:"schema"`
	SyntheticOnly            bool     `json:"synthetic_only"`
	ContractID               string   `json:"contract_id"`
	Identity                 Identity `json:"identity"`
	RootKeyGeneration        uint64   `json:"root_key_generation"`
	RootKeyHex               string   `json:"root_key_hex"`
	RandomBytesHex           string   `json:"random_bytes_hex"`
	ProfileDirectoryDigest   string   `json:"profile_directory_digest"`
	CheckpointID             string   `json:"checkpoint_id"`
	CheckpointIDSHA256       string   `json:"checkpoint_id_sha256"`
	CurrentBase64            string   `json:"current_base64"`
	MetadataBase64           string   `json:"metadata_base64"`
	MetadataSHA256           string   `json:"metadata_sha256"`
	PayloadBase64            string   `json:"payload_base64"`
	PayloadSHA256            string   `json:"payload_sha256"`
	PlaintextBase64          string   `json:"plaintext_base64"`
	PlaintextSHA256          string   `json:"plaintext_sha256"`
	WrappingKeyHex           string   `json:"wrapping_key_hex"`
	WrapAADBase64            string   `json:"wrap_aad_base64"`
	PayloadDataAADBase64     string   `json:"payload_data_aad_base64"`
	PayloadTerminalAADBase64 string   `json:"payload_terminal_aad_base64"`
	Provenance               struct {
		ProducerCommit                string `json:"producer_commit"`
		SourceFilesUnmodified         bool   `json:"source_files_unmodified"`
		HarnessSHA256                 string `json:"harness_sha256"`
		ProductionAlgorithmsInHarness bool   `json:"production_algorithms_in_harness"`
		NetworkUsed                   bool   `json:"network_used"`
		SourceFiles                   []struct {
			Path    string `json:"path"`
			GitBlob string `json:"git_blob"`
			SHA256  string `json:"sha256"`
		} `json:"source_files"`
	} `json:"provenance"`
}

func readCompatibilityGolden(t *testing.T, version string) compatibilityGolden {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "golden-"+version+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var golden compatibilityGolden
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatal(err)
	}
	wantCommit := map[string]string{"v1": "bd07dc4a25ba331b9ab80e14ccb88399888ab26c", "v2": "07d03eb28a5c3e530acabdc167638a8c33cce20c"}[version]
	if golden.Schema != "openlinker.browser.profile.synthetic-golden.v1" || !golden.SyntheticOnly || golden.ContractID != "openlinker.browser."+version || golden.Provenance.ProducerCommit != wantCommit || !golden.Provenance.SourceFilesUnmodified || golden.Provenance.ProductionAlgorithmsInHarness || golden.Provenance.NetworkUsed || len(golden.Provenance.SourceFiles) < 10 {
		t.Fatal("golden provenance is not an independent historical producer")
	}
	harness, err := os.ReadFile(filepath.Join("testdata", "export-golden.go.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if compatibilitySHA(harness) != golden.Provenance.HarnessSHA256 {
		t.Fatal("exporter changed without regenerating historical evidence")
	}
	for _, source := range golden.Provenance.SourceFiles {
		if len(source.GitBlob) != 40 || len(source.SHA256) != 64 || source.Path == "" {
			t.Fatal("missing source provenance")
		}
	}
	for name, pair := range map[string][2]string{
		"metadata":  {golden.MetadataBase64, golden.MetadataSHA256},
		"payload":   {golden.PayloadBase64, golden.PayloadSHA256},
		"plaintext": {golden.PlaintextBase64, golden.PlaintextSHA256},
	} {
		if compatibilitySHA(compatibilityBase64(t, pair[0])) != pair[1] {
			t.Fatalf("invalid %s fixture checksum", name)
		}
	}
	if compatibilitySHA([]byte(golden.CheckpointID)) != golden.CheckpointIDSHA256 {
		t.Fatal("invalid checkpoint fixture checksum")
	}
	return golden
}

func compatibilityBase64(t *testing.T, value string) []byte {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func compatibilityHex(t *testing.T, value string) []byte {
	t.Helper()
	raw, err := hex.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func compatibilitySHA(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func compatibilityKey(t *testing.T, golden compatibilityGolden) *RootKey {
	t.Helper()
	root, err := NewRootKey(golden.RootKeyGeneration, compatibilityHex(t, golden.RootKeyHex))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(root.Close)
	return root
}

func TestHistoricalGoldenStorageDomainsAndAuthentication(t *testing.T) {
	for _, version := range []string{"v1", "v2"} {
		t.Run(version, func(t *testing.T) {
			golden := readCompatibilityGolden(t, version)
			contract := storageContract(golden.ContractID)
			root := compatibilityKey(t, golden)
			if profileDigestForContract(golden.Identity, contract) != golden.ProfileDirectoryDigest {
				t.Fatal("directory hash domain changed")
			}
			kek, err := deriveWrappingKeyForContract(golden.Identity, root, contract)
			if err != nil || !bytes.Equal(kek, compatibilityHex(t, golden.WrappingKeyHex)) {
				t.Fatal("historical KDF domain changed")
			}
			defer clear(kek)
			metadata, err := parseMetadataForContract(compatibilityBase64(t, golden.MetadataBase64), contract)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(wrapAAD(metadata), compatibilityBase64(t, golden.WrapAADBase64)) {
				t.Fatal("historical metadata AAD changed")
			}
			plaintext := compatibilityBase64(t, golden.PlaintextBase64)
			if !bytes.Equal(payloadAADForContract(golden.Identity, 0, uint32(len(plaintext)), false, contract), compatibilityBase64(t, golden.PayloadDataAADBase64)) || !bytes.Equal(payloadAADForContract(golden.Identity, 1, 0, true, contract), compatibilityBase64(t, golden.PayloadTerminalAADBase64)) {
				t.Fatal("historical payload AAD changed")
			}
			dek, err := unwrapForContract(metadata, golden.Identity, root, contract)
			if err != nil {
				t.Fatal(err)
			}
			defer clear(dek)
			cipher := &payloadCipher{identity: golden.Identity, contract: contract, random: bytes.NewReader(nil)}
			copy(cipher.key[:], dek)
			defer cipher.close()
			encrypted := compatibilityBase64(t, golden.PayloadBase64)
			var actual bytes.Buffer
			if err := cipher.decrypt(&actual, bytes.NewReader(encrypted)); err != nil || !bytes.Equal(actual.Bytes(), plaintext) {
				t.Fatalf("independent %s golden cannot be authenticated: %v", version, err)
			}
			for name, corrupt := range map[string][]byte{"truncated_final": encrypted[:len(encrypted)-1], "trailing": append(append([]byte(nil), encrypted...), 0)} {
				actual.Reset()
				if err := cipher.decrypt(&actual, bytes.NewReader(corrupt)); !errors.Is(err, ErrProfileCorrupt) {
					t.Fatalf("%s accepted: %v", name, err)
				}
			}
			opposite := storageContractV1
			if contract == storageContractV1 {
				opposite = storageContractV2
			}
			if _, err := unwrapForContract(metadata, golden.Identity, root, opposite); !errors.Is(err, ErrProfileCorrupt) {
				t.Fatal("cross-contract key unwrap was accepted")
			}
		})
	}
}

func TestCurrentV2WritesRemainByteIdenticalToFrozenProducer(t *testing.T) {
	golden := readCompatibilityGolden(t, "v2")
	root := compatibilityKey(t, golden)
	store, err := newStore(t.TempDir(), newProtector(bytes.NewReader(compatibilityHex(t, golden.RandomBytesHex))))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if contractID() != golden.ContractID || profileDigest(golden.Identity) != golden.ProfileDirectoryDigest {
		t.Fatal("normal Store storage contract changed")
	}
	if err := store.Create(golden.Identity, root, bytes.NewReader(compatibilityBase64(t, golden.PlaintextBase64))); err != nil {
		t.Fatal(err)
	}
	profileDir := store.profileDir(golden.Identity)
	for path, expected := range map[string]string{
		filepath.Join(profileDir, "current.json"):                                      golden.CurrentBase64,
		filepath.Join(profileDir, "checkpoints", golden.CheckpointID, "metadata.json"): golden.MetadataBase64,
		filepath.Join(profileDir, "checkpoints", golden.CheckpointID, "payload.bin"):   golden.PayloadBase64,
	} {
		raw, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(raw, compatibilityBase64(t, expected)) {
			t.Fatalf("frozen v2 bytes changed at %s: %v", filepath.Base(path), err)
		}
	}
}

func copyCompatibilitySource(t *testing.T, golden compatibilityGolden) (MigrationRequest, string) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(base, 0o700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(base, "source")
	profile := filepath.Join(source, "profiles", golden.ProfileDirectoryDigest)
	checkpoint := filepath.Join(profile, "checkpoints", golden.CheckpointID)
	if err := os.MkdirAll(checkpoint, 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		filepath.Join(source, ".manager.lock"):     nil,
		filepath.Join(profile, "current.json"):     compatibilityBase64(t, golden.CurrentBase64),
		filepath.Join(checkpoint, "metadata.json"): compatibilityBase64(t, golden.MetadataBase64),
		filepath.Join(checkpoint, "payload.bin"):   compatibilityBase64(t, golden.PayloadBase64),
		filepath.Join(base, "synthetic-root-key"):  compatibilityHex(t, golden.RootKeyHex),
	}
	for path, content := range files {
		if err := os.WriteFile(path, content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	anchor := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(filepath.Join(profile, "current.json"), anchor, anchor); err != nil {
		t.Fatal(err)
	}
	request := MigrationRequest{
		Schema: MigrationRequestSchema, OperationID: "22222222-2222-4222-8222-222222222222", SourceContract: golden.ContractID, TargetContract: "openlinker.browser.v2",
		SourceStore: source, DestinationStore: filepath.Join(base, "destination"), RootKeyFile: filepath.Join(base, "synthetic-root-key"),
		ExpectedIdentity: golden.Identity, RootKeyGeneration: golden.RootKeyGeneration,
		Source:    MigrationSnapshot{ProfileDirectorySHA256: golden.ProfileDirectoryDigest, MetadataSHA256: golden.MetadataSHA256, CheckpointIDSHA256: golden.CheckpointIDSHA256, PayloadSHA256: golden.PayloadSHA256, CurrentPointerMTime: anchor.Format(time.RFC3339Nano)},
		Retention: MigrationRetention{Mode: "preserve"},
	}
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	requestPath := filepath.Join(base, "request.json")
	if err := os.WriteFile(requestPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return request, requestPath
}

func compatibilityTree(t *testing.T, root string) map[string]string {
	t.Helper()
	result := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		value := fmt.Sprintf("%s/%d/%d", info.Mode(), info.Size(), info.ModTime().UnixNano())
		if info.Mode().IsRegular() {
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			value += "/" + compatibilitySHA(raw)
		}
		result[rel] = value
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestNormalV2StoreRefusesIndependentLegacyGoldenWithoutQuarantine(t *testing.T) {
	golden := readCompatibilityGolden(t, "v1")
	request, _ := copyCompatibilitySource(t, golden)
	root := compatibilityKey(t, golden)
	store, err := NewStore(request.SourceStore)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	before := compatibilityTree(t, filepath.Join(request.SourceStore, "profiles"))
	var output bytes.Buffer
	if err := store.Load(golden.Identity, map[uint64]*RootKey{1: root}, &output); !errors.Is(err, ErrProfileMigrationRequired) {
		t.Fatalf("legacy Load must require explicit migration: %v", err)
	}
	if err := store.Create(golden.Identity, root, bytes.NewReader([]byte("must never create empty v2"))); !errors.Is(err, ErrProfileMigrationRequired) {
		t.Fatalf("legacy Create must require explicit migration: %v", err)
	}
	if output.Len() != 0 || !reflect.DeepEqual(before, compatibilityTree(t, filepath.Join(request.SourceStore, "profiles"))) {
		t.Fatal("normal Store consumed or mutated legacy Profile")
	}
	quarantined, err := os.ReadDir(filepath.Join(request.SourceStore, "quarantine"))
	if err != nil || len(quarantined) != 0 {
		t.Fatal("legacy golden was quarantined")
	}
}

func TestHistoricalV1GoldenFullMigrationAndNormalV2Consumption(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("full offline migration is Linux-only; codec goldens run natively in separate tests")
	}
	golden := readCompatibilityGolden(t, "v1")
	request, path := copyCompatibilitySource(t, golden)
	before := compatibilityTree(t, request.SourceStore)
	report, err := ExecuteProfileMigration(context.Background(), path, MigrationExecute)
	if err != nil {
		t.Fatalf("independent historical migration failed: stage=%s code=%s", report.Stage, report.FailureCode)
	}
	if !report.SourceAuthenticated || !report.TargetAuthenticated || !report.PlaintextEqual || !report.SourceUnchanged || !report.ActivationReady || !report.Published {
		t.Fatalf("incomplete migration report: %+v", report)
	}
	if !reflect.DeepEqual(before, compatibilityTree(t, request.SourceStore)) {
		t.Fatal("migration changed historical source bytes, paths, modes or mtime")
	}
	checked, err := ExecuteProfileMigration(context.Background(), path, MigrationCheck)
	if err != nil || !checked.ActivationReady || !checked.PlaintextEqual {
		t.Fatalf("check failed: %s/%s", checked.Stage, checked.FailureCode)
	}
	root := compatibilityKey(t, golden)
	store, err := NewStore(request.DestinationStore)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var output bytes.Buffer
	if err := store.Load(golden.Identity, map[uint64]*RootKey{1: root}, &output); err != nil || !bytes.Equal(output.Bytes(), compatibilityBase64(t, golden.PlaintextBase64)) {
		t.Fatalf("normal v2 Store did not consume migrated historical archive: %v", err)
	}
	profile := store.profileDir(golden.Identity)
	currentRaw, err := os.ReadFile(filepath.Join(profile, "current.json"))
	if err != nil {
		t.Fatal(err)
	}
	current, err := parseCurrent(currentRaw)
	if err != nil {
		t.Fatal(err)
	}
	metadataRaw, err := os.ReadFile(filepath.Join(profile, "checkpoints", current.Checkpoint, "metadata.json"))
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := parseMetadata(metadataRaw)
	if err != nil {
		t.Fatal(err)
	}
	oldMetadata, err := parseMetadataForContract(compatibilityBase64(t, golden.MetadataBase64), storageContractV1)
	if err != nil {
		t.Fatal(err)
	}
	newDEK, err := unwrap(metadata, golden.Identity, root)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(newDEK)
	oldDEK, err := unwrapForContract(oldMetadata, golden.Identity, root, storageContractV1)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(oldDEK)
	if bytes.Equal(newDEK, oldDEK) || bytes.Equal(metadata.WrapNonce, oldMetadata.WrapNonce) || current.Checkpoint == golden.CheckpointID || !metadata.Identity.equal(golden.Identity) || metadata.RootKeyGeneration != golden.RootKeyGeneration {
		t.Fatal("migration did not create fresh cryptographic material while preserving identity")
	}
	info, err := os.Stat(filepath.Join(profile, "current.json"))
	if err != nil || info.ModTime().UTC().Format(time.RFC3339Nano) != request.Source.CurrentPointerMTime {
		t.Fatal("retention anchor drifted")
	}
	encoded, _ := json.Marshal(report)
	for _, forbidden := range []string{golden.Identity.AgentID, golden.Identity.PrincipalScopeID, golden.RootKeyHex, golden.PlaintextSHA256, request.SourceStore, request.RootKeyFile} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatal("report leaks private execution detail")
		}
	}
	if err := store.Checkpoint(golden.Identity, map[uint64]*RootKey{1: root}, bytes.NewReader(output.Bytes())); err != nil {
		t.Fatal(err)
	}
	advancedRaw, err := os.ReadFile(filepath.Join(profile, "current.json"))
	if err != nil {
		t.Fatal(err)
	}
	advanced, err := parseCurrent(advancedRaw)
	if err != nil || advanced.Checkpoint == current.Checkpoint {
		t.Fatal("normal v2 Store could not advance migrated checkpoint")
	}
	if !reflect.DeepEqual(before, compatibilityTree(t, request.SourceStore)) {
		t.Fatal("normal target consumption changed source")
	}
}
