package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/egressgateway/internal/egresscontract"
)

func main() {
	target := filepath.Join(
		"..",
		"browser-engine",
		"src",
		"egress-contract.generated.ts",
	)
	if err := os.WriteFile(
		target,
		[]byte(egresscontract.TypeScriptSource()),
		0o644,
	); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
