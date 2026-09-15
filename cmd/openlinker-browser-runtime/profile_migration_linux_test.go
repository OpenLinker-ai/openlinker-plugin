//go:build linux

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserprofile"
)

type migrationGolden struct {
	SyntheticOnly     bool                    `json:"synthetic_only"`
	ContractID        string                  `json:"contract_id"`
	Identity          browserprofile.Identity `json:"identity"`
	RootKeyHex        string                  `json:"root_key_hex"`
	RootKeyGeneration uint64                  `json:"root_key_generation"`
	Directory         string                  `json:"profile_directory_digest"`
	Checkpoint        string                  `json:"checkpoint_id"`
	Metadata          []byte                  `json:"metadata_base64"`
	Payload           []byte                  `json:"payload_base64"`
	Current           []byte                  `json:"current_base64"`
	Provenance        struct {
		ProducerCommit string `json:"producer_commit"`
	} `json:"provenance"`
}

func migrationTestSHA(raw []byte) string {
	value := sha256.Sum256(raw)
	return hex.EncodeToString(value[:])
}

func syntheticMigrationRequest(t *testing.T) (string, browserprofile.MigrationRequest) {
	t.Helper()
	goldenDir := os.Getenv("OPENLINKER_TEST_PROFILE_GOLDEN_DIR")
	if goldenDir == "" {
		goldenDir = filepath.Join("..", "..", "packages", "browser-runtime", "browserprofile", "testdata")
	}
	raw, err := os.ReadFile(filepath.Join(goldenDir, "golden-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var golden migrationGolden
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatal(err)
	}
	if !golden.SyntheticOnly || golden.ContractID != "openlinker.browser.v1" || golden.Provenance.ProducerCommit != "bd07dc4a25ba331b9ab80e14ccb88399888ab26c" {
		t.Fatal("command compatibility test requires the independently produced historical v1 golden")
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "source")
	parent := filepath.Join(root, "destination-parent")
	profile := filepath.Join(source, "profiles", golden.Directory)
	checkpoint := filepath.Join(profile, "checkpoints", golden.Checkpoint)
	for _, directory := range []string{checkpoint, parent} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for filename, contents := range map[string][]byte{
		filepath.Join(source, ".manager.lock"):     nil,
		filepath.Join(profile, "current.json"):     golden.Current,
		filepath.Join(checkpoint, "metadata.json"): golden.Metadata,
		filepath.Join(checkpoint, "payload.bin"):   golden.Payload,
	} {
		if err := os.WriteFile(filename, contents, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	key, err := hex.DecodeString(golden.RootKeyHex)
	if err != nil || len(key) != 32 {
		t.Fatal("invalid synthetic golden key")
	}
	keyFile := filepath.Join(root, "synthetic-profile-root-key")
	if err := os.WriteFile(keyFile, key, 0o600); err != nil {
		t.Fatal(err)
	}
	clear(key)
	pointer := filepath.Join(profile, "current.json")
	anchor := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(pointer, anchor, anchor); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(pointer)
	if err != nil {
		t.Fatal(err)
	}
	request := browserprofile.MigrationRequest{
		Schema:         browserprofile.MigrationRequestSchema,
		OperationID:    "99999999-9999-4999-8999-999999999999",
		SourceContract: "openlinker.browser.v1", TargetContract: "openlinker.browser.v2",
		SourceStore: source, DestinationStore: filepath.Join(parent, "migrated"), RootKeyFile: keyFile,
		ExpectedIdentity: golden.Identity, RootKeyGeneration: golden.RootKeyGeneration,
		Source:    browserprofile.MigrationSnapshot{ProfileDirectorySHA256: golden.Directory, MetadataSHA256: migrationTestSHA(golden.Metadata), CheckpointIDSHA256: migrationTestSHA([]byte(golden.Checkpoint)), PayloadSHA256: migrationTestSHA(golden.Payload), CurrentPointerMTime: info.ModTime().UTC().Format(time.RFC3339Nano)},
		Retention: browserprofile.MigrationRetention{Mode: "preserve"},
	}
	requestPath := filepath.Join(root, "request.json")
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(requestPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	return requestPath, request
}

type migrationTreeEntry struct {
	Mode  fs.FileMode
	MTime string
	Hash  string
}

func migrationTree(t *testing.T, root string) map[string]migrationTreeEntry {
	t.Helper()
	result := map[string]migrationTreeEntry{}
	err := filepath.WalkDir(root, func(filename string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		value := migrationTreeEntry{Mode: info.Mode(), MTime: info.ModTime().UTC().Format(time.RFC3339Nano)}
		if info.Mode().IsRegular() {
			raw, err := os.ReadFile(filename)
			if err != nil {
				return err
			}
			value.Hash = migrationTestSHA(raw)
		}
		relative, err := filepath.Rel(root, filename)
		if err != nil {
			return err
		}
		result[relative] = value
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func executeSyntheticMigration(t *testing.T, mode, request string, wantSuccess bool) browserprofile.MigrationReport {
	t.Helper()
	command := migrationHelperCommand(t, mode)
	command.Env = append(command.Env, "OPENLINKER_TEST_MIGRATION_REQUEST="+request)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	if (err == nil) != wantSuccess || stderr.Len() != 0 {
		t.Fatalf("command success=%v want=%v stderr=%s stdout=%s", err == nil, wantSuccess, &stderr, &stdout)
	}
	var report browserprofile.MigrationReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout.String(), request) || strings.Contains(stdout.String(), "synthetic-golden-principal-scope") {
		t.Fatal("command report leaked private request fields")
	}
	return report
}

func TestProfileMigrationLinuxCommandGoldenExecuteCheckFinalize(t *testing.T) {
	requestPath, request := syntheticMigrationRequest(t)
	before := migrationTree(t, request.SourceStore)
	first := executeSyntheticMigration(t, "fixture-execute", requestPath, true)
	if first.Status != "success" || !first.SourceAuthenticated || !first.TargetAuthenticated || !first.PlaintextEqual || !first.SourceUnchanged || !first.Published || !first.ActivationReady || first.SourceContract != "openlinker.browser.v1" || first.TargetContract != "openlinker.browser.v2" {
		t.Fatalf("migration did not complete cryptographic gates: %#v", first)
	}
	if !reflect.DeepEqual(before, migrationTree(t, request.SourceStore)) {
		t.Fatal("migration changed source bytes, permissions or mtime")
	}
	targetBefore := migrationTree(t, request.DestinationStore)
	checked := executeSyntheticMigration(t, "fixture-check", requestPath, true)
	if checked.Mode != browserprofile.MigrationCheck || !checked.PlaintextEqual || !checked.ActivationReady {
		t.Fatalf("check report = %#v", checked)
	}
	if !reflect.DeepEqual(targetBefore, migrationTree(t, request.DestinationStore)) {
		t.Fatal("read-only check changed target")
	}
	finalized := executeSyntheticMigration(t, "fixture-finalize", requestPath, true)
	if !finalized.AlreadyFinalized || !finalized.ActivationReady {
		t.Fatalf("already-finalized report = %#v", finalized)
	}
	if !reflect.DeepEqual(targetBefore, migrationTree(t, request.DestinationStore)) {
		t.Fatal("already-finalized check changed target")
	}
	failed := executeSyntheticMigration(t, "fixture-execute", requestPath, false)
	if failed.Status != "failed" || failed.ActivationReady {
		t.Fatalf("existing target migration was accepted: %#v", failed)
	}
	if !reflect.DeepEqual(before, migrationTree(t, request.SourceStore)) || !reflect.DeepEqual(targetBefore, migrationTree(t, request.DestinationStore)) {
		t.Fatal("repeated execute changed source or target")
	}
}

func TestProfileMigrationLinuxCommandRejectsPrivateInvalidRequestWithoutInitialization(t *testing.T) {
	requestPath, request := syntheticMigrationRequest(t)
	before := migrationTree(t, request.SourceStore)
	if err := os.WriteFile(requestPath, []byte(`{"schema":"synthetic-private-invalid","schema":"duplicate-private-value"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	report := executeSyntheticMigration(t, "fixture-execute", requestPath, false)
	if report.Published || report.ActivationReady || report.FailureCode != "invalid_request" {
		t.Fatalf("invalid request report = %#v", report)
	}
	if _, err := os.Stat(request.DestinationStore); !os.IsNotExist(err) {
		t.Fatal("invalid request created destination")
	}
	if !reflect.DeepEqual(before, migrationTree(t, request.SourceStore)) {
		t.Fatal("invalid request changed source")
	}
}
