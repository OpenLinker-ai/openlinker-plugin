//go:build windows

package main

import (
	"context"
	"fmt"
	"os"

	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserprofile"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "migrate-profile" {
		// The non-Linux domain entrypoint refuses before opening the request.
		// Share argument validation and the complete report encoding; this does
		// not enable Profile migration or normal Runtime service on Windows.
		_ = runProfileMigrationCommand(context.Background(), os.Args[2:], os.Stdout, os.Stderr, browserprofile.ExecuteProfileMigration)
		os.Exit(1)
	}
	fmt.Fprintln(os.Stderr, "openlinker Browser Runtime: Linux container runtime is required")
	os.Exit(1)
}
