//go:build !windows

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserprofile"
)

func TestProfileMigrationArgumentsAreClosedAndMutuallyExclusive(t *testing.T) {
	t.Parallel()
	request := filepath.Join(t.TempDir(), "request.json")
	for _, test := range []struct {
		args []string
		mode browserprofile.MigrationMode
	}{
		{[]string{"--request-file", request}, browserprofile.MigrationExecute},
		{[]string{"--request-file", request, "--check"}, browserprofile.MigrationCheck},
		{[]string{"--check", "--request-file", request}, browserprofile.MigrationCheck},
		{[]string{"--request-file", request, "--finalize"}, browserprofile.MigrationFinalize},
		{[]string{"--finalize", "--request-file", request}, browserprofile.MigrationFinalize},
	} {
		path, mode, err := parseProfileMigrationArguments(test.args)
		if err != nil || path != request || mode != test.mode {
			t.Fatalf("parse valid migration arguments: path=%q mode=%q err=%v", path, mode, err)
		}
	}
	for _, args := range [][]string{
		nil, {"--request-file"}, {"--request-file", ""}, {"--request-file", "relative.json"},
		{"--request-file", "/"}, {"--request-file", "/tmp/../private.json"},
		{"--request-file", "/tmp/a\x00b"}, {"--request-file=" + request},
		{"--request-file", request, "--request-file", request},
		{"--check", "--finalize", "--request-file", request},
		{"--check", "--check", "--request-file", request},
		{"--finalize", "--finalize", "--request-file", request},
		{"--request-file", request, "--force"}, {"--request-file", request, "unexpected"},
		{"--key", "synthetic-secret", "--request-file", request},
	} {
		if _, _, err := parseProfileMigrationArguments(args); err == nil {
			t.Fatalf("invalid migration arguments accepted: %#v", args)
		}
	}
}

func TestProfileMigrationArgumentErrorsNeverExecuteOrEchoInputs(t *testing.T) {
	t.Parallel()
	var output, errorOutput bytes.Buffer
	err := runProfileMigrationCommand(context.Background(), []string{"--secret=synthetic-private-value"}, &output, &errorOutput,
		func(context.Context, string, browserprofile.MigrationMode) (browserprofile.MigrationReport, error) {
			t.Fatal("invalid arguments reached migration execution")
			return browserprofile.MigrationReport{}, nil
		})
	if !errors.Is(err, errProfileMigrationReported) || errorOutput.Len() != 0 || strings.Contains(output.String(), "synthetic-private-value") {
		t.Fatalf("unsafe argument failure: err=%v stdout=%s stderr=%s", err, &output, &errorOutput)
	}
	var report browserprofile.MigrationReport
	if err := json.Unmarshal(output.Bytes(), &report); err != nil || report.FailureCode != "invalid_arguments" || report.Published || report.ActivationReady {
		t.Fatalf("argument report = %#v, %v", report, err)
	}
}

func TestProfileMigrationCommandPreservesModesAndHasTenMinuteBudget(t *testing.T) {
	t.Parallel()
	request := filepath.Join(t.TempDir(), "request.json")
	for _, flag := range []string{"", "--check", "--finalize"} {
		args := []string{"--request-file", request}
		if flag != "" {
			args = append(args, flag)
		}
		var output, errorOutput bytes.Buffer
		calls := 0
		err := runProfileMigrationCommand(context.Background(), args, &output, &errorOutput,
			func(ctx context.Context, path string, mode browserprofile.MigrationMode) (browserprofile.MigrationReport, error) {
				calls++
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > 10*time.Minute || time.Until(deadline) < 9*time.Minute || path != request {
					t.Fatal("migration invocation lost its path or bounded deadline")
				}
				expected := browserprofile.MigrationExecute
				if flag == "--check" {
					expected = browserprofile.MigrationCheck
				}
				if flag == "--finalize" {
					expected = browserprofile.MigrationFinalize
				}
				if mode != expected {
					t.Fatalf("mode=%q, want %q", mode, expected)
				}
				return browserprofile.MigrationReport{Schema: "openlinker.browser.profile-migration-report.v1", Mode: mode, Stage: "complete", Status: "success", Published: true, ActivationReady: true}, nil
			})
		if err != nil || calls != 1 || errorOutput.Len() != 0 {
			t.Fatalf("command result: calls=%d err=%v stderr=%s", calls, err, &errorOutput)
		}
		var report browserprofile.MigrationReport
		if err := json.Unmarshal(output.Bytes(), &report); err != nil || report.Status != "success" {
			t.Fatalf("success report: %#v, %v", report, err)
		}
	}
}

func TestProfileMigrationCommandHonorsParentCancellationAndRedactsUpstreamError(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	var output, errorOutput bytes.Buffer
	err := runProfileMigrationCommand(ctx, []string{"--request-file", "/synthetic/request.json"}, &output, &errorOutput,
		func(ctx context.Context, _ string, mode browserprofile.MigrationMode) (browserprofile.MigrationReport, error) {
			<-ctx.Done()
			return browserprofile.MigrationReport{Mode: mode, Status: "failed", Stage: "source", FailureCode: "canceled", FailureType: "context"}, errors.New("upstream /private/key=synthetic-secret")
		})
	if !errors.Is(err, errProfileMigrationReported) || strings.Contains(output.String()+errorOutput.String(), "synthetic-secret") || strings.Contains(err.Error(), "upstream") {
		t.Fatalf("unsafe upstream error handling: %v stdout=%s stderr=%s", err, &output, &errorOutput)
	}
}

type migrationBrokenWriter struct{ short bool }

func (writer migrationBrokenWriter) Write(value []byte) (int, error) {
	if writer.short {
		return len(value) / 2, nil
	}
	return 0, errors.New("private output sink secret")
}

func TestProfileMigrationOutputFailurePreservesPublishedState(t *testing.T) {
	t.Parallel()
	for _, short := range []bool{false, true} {
		var errorOutput bytes.Buffer
		report := browserprofile.MigrationReport{Schema: "openlinker.browser.profile-migration-report.v1", Status: "success", Stage: "complete", Published: true, ActivationReady: true}
		err := writeProfileMigrationReport(migrationBrokenWriter{short}, &errorOutput, report, nil)
		if !errors.Is(err, errProfileMigrationReported) || strings.Contains(errorOutput.String(), "secret") {
			t.Fatalf("output failure leaked: %v %s", err, &errorOutput)
		}
		var value struct {
			browserprofile.MigrationReport
			OutcomeUncertain bool   `json:"outcome_uncertain"`
			CompletedStage   string `json:"completed_stage"`
		}
		if err := json.Unmarshal(errorOutput.Bytes(), &value); err != nil || !value.Published || !value.ActivationReady || !value.OutcomeUncertain || value.CompletedStage != "complete" || value.FailureCode != "output_failed" || value.Stage != "output" {
			t.Fatalf("uncertain output report = %#v, %v", value, err)
		}
	}
}

func migrationHelperCommand(t *testing.T, mode string) *exec.Cmd {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	command := exec.CommandContext(ctx, executable, "-test.run=^TestProfileMigrationProcessHelper$")
	command.Env = append(os.Environ(), "OPENLINKER_TEST_MIGRATION_HELPER="+mode, "OPENLINKER_TEST_MIGRATION_ROOT="+t.TempDir())
	return command
}

func TestProfileMigrationProductionDispatchPrecedesServiceInitialization(t *testing.T) {
	command := migrationHelperCommand(t, "main")
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err == nil {
		t.Fatal("missing request unexpectedly succeeded")
	}
	var report browserprofile.MigrationReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil || report.Status != "failed" || report.Schema != "openlinker.browser.profile-migration-report.v1" || stderr.Len() != 0 {
		t.Fatalf("production command did not emit exactly safe JSON: %v stdout=%s stderr=%s", err, &stdout, &stderr)
	}
	if runtime.GOOS != "linux" && report.FailureCode != "unsupported_platform" {
		t.Fatalf("non-Linux report = %#v", report)
	}
	if strings.Contains(stdout.String(), "synthetic-private") {
		t.Fatal("request path leaked")
	}
	for _, entry := range command.Env {
		if strings.HasPrefix(entry, "OPENLINKER_TEST_MIGRATION_ROOT=") {
			entries, err := os.ReadDir(strings.TrimPrefix(entry, "OPENLINKER_TEST_MIGRATION_ROOT="))
			if err != nil || len(entries) != 0 {
				t.Fatalf("migration dispatch initialized normal Runtime artifacts: %v %v", entries, err)
			}
		}
	}
}

func TestProfileMigrationSIGTERMCancelsTheCommandContext(t *testing.T) {
	command := migrationHelperCommand(t, "signal")
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(stdout)
	line, err := reader.ReadString('\n')
	if err != nil || line != "READY\n" {
		t.Fatalf("helper readiness=%q err=%v", line, err)
	}
	if err := command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err == nil {
		t.Fatal("canceled command returned success")
	}
	var report browserprofile.MigrationReport
	if err := json.Unmarshal(raw, &report); err != nil || report.FailureCode != "canceled" || stderr.Len() != 0 {
		t.Fatalf("SIGTERM was not handled: report=%#v err=%v stderr=%s", report, err, &stderr)
	}
}

func TestProfileMigrationProcessHelper(t *testing.T) {
	mode := os.Getenv("OPENLINKER_TEST_MIGRATION_HELPER")
	if mode == "" {
		return
	}
	if mode == "signal" {
		ctx, stop := profileMigrationSignalContext()
		defer stop()
		err := runProfileMigrationCommand(ctx, []string{"--request-file", "/synthetic/request.json"}, os.Stdout, os.Stderr,
			func(ctx context.Context, _ string, mode browserprofile.MigrationMode) (browserprofile.MigrationReport, error) {
				fmt.Fprintln(os.Stdout, "READY")
				<-ctx.Done()
				return browserprofile.MigrationReport{Mode: mode, Stage: "source", Status: "failed", FailureCode: "canceled", FailureType: "context"}, ctx.Err()
			})
		if err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	root := os.Getenv("OPENLINKER_TEST_MIGRATION_ROOT")
	for _, key := range []string{"OPENLINKER_BROWSER_CHANNEL_CREDENTIAL_FILE", "OPENLINKER_BROWSER_PROFILE_STORE", "OPENLINKER_BROWSER_PROFILE_WORK_ROOT", "OPENLINKER_BROWSER_PROFILE_ROOT_KEY_FILE", "OPENLINKER_BROWSER_SOCKET", "OPENLINKER_BROWSER_OPS_CREDENTIAL_FILE", "OPENLINKER_BROWSER_OBSERVER_CREDENTIAL_FILE"} {
		if err := os.Setenv(key, filepath.Join(root, key)); err != nil {
			panic("test environment setup failed")
		}
	}
	if err := os.Setenv("OPENLINKER_BROWSER_OPS_VIEWER_ENABLED", "invalid-normal-runtime-setting"); err != nil {
		panic("test environment setup failed")
	}
	request := os.Getenv("OPENLINKER_TEST_MIGRATION_REQUEST")
	if request == "" {
		request = filepath.Join(root, "synthetic-private-missing.json")
	}
	os.Args = []string{"openlinker-browser-runtime", "migrate-profile", "--request-file", request}
	if mode == "fixture-check" {
		os.Args = append(os.Args, "--check")
	}
	if mode == "fixture-finalize" {
		os.Args = append(os.Args, "--finalize")
	}
	main()
	os.Exit(0)
}
