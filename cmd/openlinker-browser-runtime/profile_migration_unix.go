//go:build !windows

package main

import (
	"context"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserprofile"
)

func profileMigrationSignalContext() (context.Context, context.CancelFunc) {
	// Go otherwise terminates the process on EPIPE from stdout/stderr before
	// Write can return the error. Retain control to emit the safe uncertainty
	// report on the other stream; do not globally ignore SIGPIPE for Runtime.
	pipeSignals := make(chan os.Signal, 1)
	signal.Notify(pipeSignals, syscall.SIGPIPE)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	return ctx, func() {
		stop()
		signal.Stop(pipeSignals)
	}
}

func runProfileMigration(args []string, output, errorOutput io.Writer) error {
	ctx, stop := profileMigrationSignalContext()
	defer stop()
	return runProfileMigrationCommand(ctx, args, output, errorOutput, browserprofile.ExecuteProfileMigration)
}
