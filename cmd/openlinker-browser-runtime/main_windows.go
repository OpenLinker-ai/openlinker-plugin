//go:build windows

package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "migrate-profile" {
		fmt.Fprintln(os.Stdout, `{"schema":"openlinker.browser.profile-migration-report.v1","stage":"platform","status":"failed","failure_code":"unsupported_platform","failure_type":"platform","published":false,"activation_ready":false}`)
		os.Exit(1)
	}
	fmt.Fprintln(os.Stderr, "openlinker Browser Runtime: Linux container runtime is required")
	os.Exit(1)
}
