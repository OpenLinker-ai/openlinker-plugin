//go:build !linux

package browserprofile

import "context"

func executeProfileMigration(_ context.Context, _ string, _ MigrationMode, report *MigrationReport) error {
	report.FailureCode = "unsupported_platform"
	report.FailureType = "platform"
	return ErrProfileMigrationFailed
}
