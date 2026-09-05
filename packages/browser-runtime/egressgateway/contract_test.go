package egressgateway

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/egressgateway/internal/egresscontract"
)

func TestGeneratedBrowserEngineContractIsCurrent(t *testing.T) {
	path := filepath.Join(
		"..",
		"browser-engine",
		"src",
		"egress-contract.generated.ts",
	)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != egresscontract.TypeScriptSource() {
		t.Fatal("Browser engine egress contract is stale; run go generate ./packages/browser-runtime/egressgateway")
	}
}
