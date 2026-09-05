//go:build windows

package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "openlinker Browser Runtime: Linux container runtime is required")
	os.Exit(1)
}
