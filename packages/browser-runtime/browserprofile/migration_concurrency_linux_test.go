//go:build linux

package browserprofile

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type migrationProcessResult struct {
	Report        MigrationReport `json:"report"`
	RuntimeOpened bool            `json:"runtime_opened"`
	Succeeded     bool            `json:"succeeded"`
}

func TestMigrationConcurrentProcessHelper(t *testing.T) {
	role := os.Getenv("OPENLINKER_MIGRATION_TEST_HELPER")
	if role == "" {
		return
	}
	requestPath := os.Getenv("OPENLINKER_MIGRATION_TEST_REQUEST")
	var signal [1]byte
	if _, err := io.ReadFull(os.Stdin, signal[:]); err != nil {
		os.Exit(3)
	}
	if role == "runtime" {
		raw, err := os.ReadFile(requestPath)
		if err != nil {
			os.Exit(4)
		}
		var request MigrationRequest
		if json.Unmarshal(raw, &request) != nil {
			os.Exit(4)
		}
		store, err := NewStore(request.DestinationStore)
		opened := err == nil
		if store != nil {
			_ = store.Close()
		}
		_ = json.NewEncoder(os.Stdout).Encode(migrationProcessResult{RuntimeOpened: opened, Succeeded: opened})
		os.Exit(0)
	}
	ctx := context.Background()
	if role == "pause" {
		ctx = context.WithValue(ctx, migrationFaultKey{}, func(stage string) error {
			if stage != "reserved" {
				return nil
			}
			if _, err := io.WriteString(os.Stdout, "reserved\n"); err != nil {
				return err
			}
			_, err := io.ReadFull(os.Stdin, signal[:])
			return err
		})
	}
	report, err := ExecuteProfileMigration(ctx, requestPath, MigrationExecute)
	_ = json.NewEncoder(os.Stdout).Encode(migrationProcessResult{Report: report, Succeeded: err == nil})
	os.Exit(0)
}

type migrationProcess struct {
	command *exec.Cmd
	input   io.WriteCloser
	output  *bufio.Reader
}

func startMigrationProcess(t *testing.T, fixture migrationFixture, role string) migrationProcess {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(ctx, binary, "-test.run=^TestMigrationConcurrentProcessHelper$")
	command.Env = append(os.Environ(), "OPENLINKER_MIGRATION_TEST_HELPER="+role, "OPENLINKER_MIGRATION_TEST_REQUEST="+fixture.path)
	command.Stderr = os.Stderr
	input, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = input.Close()
		if command.ProcessState == nil {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	})
	return migrationProcess{command: command, input: input, output: bufio.NewReader(output)}
}

func (process migrationProcess) start(t *testing.T) {
	t.Helper()
	if _, err := process.input.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
}
func (process migrationProcess) result(t *testing.T) migrationProcessResult {
	t.Helper()
	var result migrationProcessResult
	if err := json.NewDecoder(process.output).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if err := process.command.Wait(); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestMigrationCrossProcessLocksExcludeSecondMigratorAndRuntime(t *testing.T) {
	fixture := newMigrationFixture(t)
	process := startMigrationProcess(t, fixture, "pause")
	process.start(t)
	line, err := process.output.ReadString('\n')
	if err != nil || line != "reserved\n" {
		t.Fatalf("reservation handshake: %q %v", line, err)
	}
	second, err := ExecuteProfileMigration(context.Background(), fixture.path, MigrationExecute)
	if err == nil || second.FailureCode != "source_lock_unavailable" || second.Published {
		t.Fatalf("second migrator crossed live source lock: %+v", second)
	}
	if store, err := NewStore(fixture.request.DestinationStore); !errors.Is(err, ErrProfileStoreLocked) {
		if store != nil {
			store.Close()
		}
		t.Fatalf("Runtime crossed reservation lock: %v", err)
	}
	if _, err := os.Stat(filepath.Join(fixture.request.DestinationStore, "profiles")); !os.IsNotExist(err) {
		t.Fatal("Runtime created profiles while pending")
	}
	process.start(t)
	result := process.result(t)
	if !result.Succeeded || !result.Report.ActivationReady {
		t.Fatalf("winning migration failed: %+v", result.Report)
	}
}

func TestMigrationAndRuntimeActualCreationRace(t *testing.T) {
	for attempt := 0; attempt < 8; attempt++ {
		fixture := newMigrationFixture(t)
		migration := startMigrationProcess(t, fixture, "migrate")
		runtime := startMigrationProcess(t, fixture, "runtime")
		// Both processes exist and wait on the same one-byte launch barrier.
		// Neither has inspected or created the target before release.
		if attempt%2 == 0 {
			migration.start(t)
			runtime.start(t)
		} else {
			runtime.start(t)
			migration.start(t)
		}
		migrated, opened := migration.result(t), runtime.result(t)
		if migrated.Succeeded == opened.RuntimeOpened {
			t.Fatalf("expected exactly one owner, migration=%+v runtime=%+v", migrated, opened)
		}
		if migrated.Succeeded {
			checked, err := ExecuteProfileMigration(context.Background(), fixture.path, MigrationCheck)
			if err != nil || !checked.ActivationReady || !checked.PlaintextEqual {
				t.Fatalf("winning migration invalid: %+v", checked)
			}
		} else {
			if migrated.Report.Published || migrated.Report.ActivationReady {
				t.Fatalf("losing migration claimed publication: %+v", migrated.Report)
			}
			entries, err := os.ReadDir(filepath.Join(fixture.request.DestinationStore, "profiles"))
			if err != nil || len(entries) != 0 {
				t.Fatal("migration wrote Runtime-owned target")
			}
		}
	}
}

func TestMigrationGCLeavesDuplicateMetadataAndPointer(t *testing.T) {
	for _, name := range []string{metadataFileName, currentFileName} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			store, err := NewStore(root)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			key := testRootKey(t, 1, 0x11)
			defer key.Close()
			identity := testIdentity()
			if err := store.Create(identity, key, bytes.NewReader([]byte("synthetic"))); err != nil {
				t.Fatal(err)
			}
			profile := store.profileDir(identity)
			raw, err := os.ReadFile(filepath.Join(profile, currentFileName))
			if err != nil {
				t.Fatal(err)
			}
			current, err := parseCurrent(raw)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(profile, currentFileName)
			if name == metadataFileName {
				path = filepath.Join(profile, "checkpoints", current.Checkpoint, metadataFileName)
				raw, err = os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
			}
			raw = []byte(strings.Replace(string(raw), `"version":1`, `"version":1,"version":1`, 1))
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			before := migrationTreeProof(t, profile)
			if count, err := store.PruneInactive(time.Now().Add(time.Hour)); err != nil || count != 0 {
				t.Fatalf("pruned malformed metadata: %d %v", count, err)
			}
			if before != migrationTreeProof(t, profile) {
				t.Fatal("GC changed malformed profile")
			}
		})
	}
}

func TestMigrationSecondPassFinalAuthenticationFailureCannotActivate(t *testing.T) {
	fixture := newMigrationFixture(t)
	ctx := context.WithValue(context.Background(), migrationFaultKey{}, func(stage string) error {
		if stage != "reserved" {
			return nil
		}
		// Simulate a non-cooperating writer after the initial full authentication.
		// This changes only the synthetic fixture, never real Profile state.
		file, err := os.OpenFile(fixture.payloadPath, os.O_RDWR, 0)
		if err != nil {
			return err
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil {
			return err
		}
		var last [1]byte
		if _, err := file.ReadAt(last[:], info.Size()-1); err != nil {
			return err
		}
		last[0] ^= 1
		_, err = file.WriteAt(last[:], info.Size()-1)
		return err
	})
	report, err := ExecuteProfileMigration(ctx, fixture.path, MigrationExecute)
	if err == nil || !report.SourceAuthenticated || !report.Published || report.ActivationReady || report.Stage != "convert" {
		t.Fatalf("late authentication failure escaped: %+v", report)
	}
	if _, err := os.Stat(filepath.Join(fixture.request.DestinationStore, migrationPendingName)); err != nil {
		t.Fatal("late failure removed pending marker")
	}
	retained := false
	if err := filepath.WalkDir(fixture.request.DestinationStore, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Name() == payloadFileName {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			retained = info.Size() > 0
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !retained {
		t.Fatal("failed conversion deleted encrypted evidence")
	}
	if store, err := NewStore(fixture.request.DestinationStore); !errors.Is(err, ErrProfileMigrationPending) {
		if store != nil {
			store.Close()
		}
		t.Fatal("Runtime accepted failed final authentication")
	}
}
