package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserprofile"
)

const profileMigrationTimeout = 10 * time.Minute

var errProfileMigrationReported = errors.New("profile migration outcome was reported")

type profileMigrationExecutor func(context.Context, string, browserprofile.MigrationMode) (browserprofile.MigrationReport, error)

func parseProfileMigrationArguments(args []string) (string, browserprofile.MigrationMode, error) {
	mode := browserprofile.MigrationExecute
	request := ""
	modeSet := false
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "--request-file":
			if request != "" || index+1 >= len(args) {
				return "", "", errProfileMigrationReported
			}
			index++
			request = args[index]
			if request == "" || !filepath.IsAbs(request) || filepath.Clean(request) != request || request == "/" || strings.ContainsRune(request, '\x00') {
				return "", "", errProfileMigrationReported
			}
		case "--check", "--finalize":
			if modeSet {
				return "", "", errProfileMigrationReported
			}
			modeSet = true
			if args[index] == "--check" {
				mode = browserprofile.MigrationCheck
			} else {
				mode = browserprofile.MigrationFinalize
			}
		default:
			return "", "", errProfileMigrationReported
		}
	}
	if request == "" {
		return "", "", errProfileMigrationReported
	}
	return request, mode, nil
}

func runProfileMigrationCommand(parent context.Context, args []string, output, errorOutput io.Writer, execute profileMigrationExecutor) error {
	request, mode, parseErr := parseProfileMigrationArguments(args)
	if parseErr != nil {
		report := browserprofile.MigrationReport{
			Schema: "openlinker.browser.profile-migration-report.v1",
			Mode:   browserprofile.MigrationExecute, Stage: "arguments", Status: "failed",
			FailureCode: "invalid_arguments", FailureType: "arguments",
		}
		return writeProfileMigrationReport(output, errorOutput, report, errProfileMigrationReported)
	}
	ctx, cancel := context.WithTimeout(parent, profileMigrationTimeout)
	defer cancel()
	report, err := execute(ctx, request, mode)
	// The domain report is the public safety boundary. Never print or wrap
	// err.Error(): filesystem and JSON errors may contain private data.
	return writeProfileMigrationReport(output, errorOutput, report, err)
}

func writeProfileMigrationReport(output, errorOutput io.Writer, report browserprofile.MigrationReport, migrationErr error) error {
	if err := writeProfileMigrationJSON(output, report); err != nil {
		completedStage := report.Stage
		report.Stage = "output"
		report.Status = "failed"
		report.FailureCode = "output_failed"
		report.FailureType = "output"
		// A failed stdout write must not turn a published/activated target back
		// into "not published". Preserve those flags and require reconciliation.
		_ = writeProfileMigrationJSON(errorOutput, struct {
			browserprofile.MigrationReport
			CompletedStage   string `json:"completed_stage"`
			OutcomeUncertain bool   `json:"outcome_uncertain"`
		}{report, completedStage, true})
		return errProfileMigrationReported
	}
	if migrationErr != nil {
		return errProfileMigrationReported
	}
	return nil
}

func writeProfileMigrationJSON(output io.Writer, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if output == nil {
		return io.ErrClosedPipe
	}
	count, err := output.Write(data)
	if err == nil && count != len(data) {
		return io.ErrShortWrite
	}
	return err
}
