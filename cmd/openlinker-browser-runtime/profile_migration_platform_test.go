package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserprofile"
)

func assertProfileMigrationPlatformReport(t *testing.T, raw []byte, mode browserprofile.MigrationMode, code string) {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("migration did not return one JSON object: %v", err)
	}
	for _, field := range []string{
		"schema", "mode", "source", "target", "retention", "source_authenticated", "target_authenticated",
		"plaintext_equal", "source_unchanged", "published", "activation_ready", "already_finalized",
		"stage", "status", "failure_code", "failure_type", "elapsed_ms",
	} {
		if _, ok := fields[field]; !ok {
			t.Fatalf("migration report omitted required field %s", field)
		}
	}
	for _, field := range []string{"source", "target", "retention"} {
		var object map[string]json.RawMessage
		if err := json.Unmarshal(fields[field], &object); err != nil || object == nil {
			t.Fatalf("report %s must be an object", field)
		}
		keys := []string{"mode"}
		if field != "retention" {
			keys = []string{"profile_directory_sha256", "metadata_sha256", "checkpoint_id_sha256", "payload_sha256", "current_pointer_mtime"}
		}
		for _, key := range keys {
			if _, ok := object[key]; !ok {
				t.Fatalf("report %s omitted %s", field, key)
			}
		}
	}
	var report browserprofile.MigrationReport
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatal(err)
	}
	if report.Schema != "openlinker.browser.profile-migration-report.v1" || report.Mode != mode || report.Status != "failed" || report.FailureCode != code {
		t.Fatalf("migration refusal contract changed: %+v", report)
	}
	if code == "unsupported_platform" && (report.Stage != "request" || report.FailureType != "platform") {
		t.Fatalf("non-Linux migration did not refuse before request processing: %+v", report)
	}
	if report.SourceAuthenticated || report.TargetAuthenticated || report.PlaintextEqual || report.SourceUnchanged || report.Published || report.ActivationReady || report.AlreadyFinalized {
		t.Fatal("a refused migration claimed completed work")
	}
	if report.Source != (browserprofile.MigrationSnapshot{}) || report.Target != (browserprofile.MigrationSnapshot{}) || report.Retention != (browserprofile.MigrationRetention{}) {
		t.Fatal("a refused migration exposed unprocessed request state")
	}
	if strings.Contains(string(raw), "synthetic-private") {
		t.Fatal("migration refusal leaked synthetic private input")
	}
}

func TestProfileMigrationPlatformArgumentsHaveCompleteReport(t *testing.T) {
	var output, errorOutput bytes.Buffer
	err := runProfileMigrationCommand(context.Background(), []string{"--synthetic-private-invalid"}, &output, &errorOutput,
		func(context.Context, string, browserprofile.MigrationMode) (browserprofile.MigrationReport, error) {
			t.Fatal("invalid arguments reached the domain executor")
			return browserprofile.MigrationReport{}, nil
		})
	if !errors.Is(err, errProfileMigrationReported) || errorOutput.Len() != 0 {
		t.Fatalf("argument refusal result changed: %v", err)
	}
	assertProfileMigrationPlatformReport(t, output.Bytes(), browserprofile.MigrationExecute, "invalid_arguments")
}

func TestProfileMigrationNonLinuxSharedCommandPreservesModeAndDoesNotReadRequest(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("non-Linux refusal contract")
	}
	root := t.TempDir()
	request := filepath.Join(root, "synthetic-private-request.json")
	private := []byte("synthetic-private-invalid-json-that-must-not-be-parsed")
	if err := os.WriteFile(request, private, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []browserprofile.MigrationMode{browserprofile.MigrationExecute, browserprofile.MigrationCheck, browserprofile.MigrationFinalize} {
		args := []string{"--request-file", request}
		if mode != browserprofile.MigrationExecute {
			args = append(args, "--"+string(mode))
		}
		var output, errorOutput bytes.Buffer
		err := runProfileMigrationCommand(context.Background(), args, &output, &errorOutput, browserprofile.ExecuteProfileMigration)
		if !errors.Is(err, errProfileMigrationReported) || errorOutput.Len() != 0 {
			t.Fatalf("platform refusal result changed: %v", err)
		}
		assertProfileMigrationPlatformReport(t, output.Bytes(), mode, "unsupported_platform")
	}
	actual, err := os.ReadFile(request)
	if err != nil || !bytes.Equal(actual, private) {
		t.Fatal("non-Linux command changed its request")
	}
}

func TestProfileMigrationNonLinuxProductionMainHasCompleteReport(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("non-Linux production entrypoint contract")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []browserprofile.MigrationMode{browserprofile.MigrationExecute, browserprofile.MigrationCheck, browserprofile.MigrationFinalize} {
		t.Run(string(mode), func(t *testing.T) {
			root := t.TempDir()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, executable, "-test.run=^TestProfileMigrationPlatformProcessHelper$")
			command.Env = append(os.Environ(), "OPENLINKER_TEST_MIGRATION_PLATFORM_HELPER="+string(mode), "OPENLINKER_TEST_MIGRATION_PLATFORM_ROOT="+root)
			var output, errorOutput bytes.Buffer
			command.Stdout, command.Stderr = &output, &errorOutput
			if err := command.Run(); err == nil || errorOutput.Len() != 0 {
				t.Fatalf("production main refusal changed: err=%v stderr=%s", err, &errorOutput)
			}
			assertProfileMigrationPlatformReport(t, output.Bytes(), mode, "unsupported_platform")
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 0 {
				t.Fatal("production main initialized Runtime or request artifacts")
			}
		})
	}
}

func TestProfileMigrationPlatformProcessHelper(t *testing.T) {
	mode := os.Getenv("OPENLINKER_TEST_MIGRATION_PLATFORM_HELPER")
	if mode == "" {
		return
	}
	root := os.Getenv("OPENLINKER_TEST_MIGRATION_PLATFORM_ROOT")
	for _, name := range []string{"OPENLINKER_BROWSER_CHANNEL_CREDENTIAL_FILE", "OPENLINKER_BROWSER_PROFILE_STORE", "OPENLINKER_BROWSER_PROFILE_WORK_ROOT", "OPENLINKER_BROWSER_PROFILE_ROOT_KEY_FILE", "OPENLINKER_BROWSER_SOCKET"} {
		if err := os.Setenv(name, filepath.Join(root, name)); err != nil {
			os.Exit(2)
		}
	}
	if err := os.Setenv("OPENLINKER_BROWSER_OPS_VIEWER_ENABLED", "synthetic-private-invalid-runtime-setting"); err != nil {
		os.Exit(2)
	}
	os.Args = []string{"openlinker-browser-runtime", "migrate-profile", "--request-file", filepath.Join(root, "synthetic-private-missing.json")}
	if mode != string(browserprofile.MigrationExecute) {
		os.Args = append(os.Args, "--"+mode)
	}
	main()
	os.Exit(0)
}
