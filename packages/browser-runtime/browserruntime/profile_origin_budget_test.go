//go:build !windows

package browserruntime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validEmptyOriginBudget = `{"contract_id":"openlinker.browser.origin-budgets.v1","entries":[]}`

func TestOriginBudgetStateAcceptsMissingAndCanonicalState(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := validateOriginBudgetState(root); err != nil {
		t.Fatalf("missing origin-budget state = %v", err)
	}
	writeOriginBudgetFixture(t, root, validEmptyOriginBudget, 0o600)
	if err := validateOriginBudgetState(root); err != nil {
		t.Fatalf("canonical origin-budget state = %v", err)
	}
}

func TestOriginBudgetStateRejectsAmbiguousOrUnsafeState(t *testing.T) {
	t.Parallel()
	keyA := strings.Repeat("a", 64)
	keyB := strings.Repeat("b", 64)
	tests := map[string]string{
		"wrong contract":  `{"contract_id":"wrong","entries":[]}`,
		"whitespace":      `{"contract_id": "openlinker.browser.origin-budgets.v1","entries":[]}`,
		"duplicate field": `{"contract_id":"openlinker.browser.origin-budgets.v1","contract_id":"openlinker.browser.origin-budgets.v1","entries":[]}`,
		"unknown field":   `{"contract_id":"openlinker.browser.origin-budgets.v1","entries":[],"extra":true}`,
		"duplicate key": `{"contract_id":"openlinker.browser.origin-budgets.v1","entries":[` +
			`{"key":"` + keyA + `","action_ms":[],"navigation_ms":[],"updated_at_ms":1},` +
			`{"key":"` + keyA + `","action_ms":[],"navigation_ms":[],"updated_at_ms":2}]}`,
		"unordered entries": `{"contract_id":"openlinker.browser.origin-budgets.v1","entries":[` +
			`{"key":"` + keyB + `","action_ms":[],"navigation_ms":[],"updated_at_ms":2},` +
			`{"key":"` + keyA + `","action_ms":[],"navigation_ms":[],"updated_at_ms":1}]}`,
		"unordered action times": `{"contract_id":"openlinker.browser.origin-budgets.v1","entries":[` +
			`{"key":"` + keyA + `","action_ms":[2,1],"navigation_ms":[],"updated_at_ms":2}]}`,
		"future action time": `{"contract_id":"openlinker.browser.origin-budgets.v1","entries":[` +
			`{"key":"` + keyA + `","action_ms":[2],"navigation_ms":[],"updated_at_ms":1}]}`,
		"noncanonical entry order": `{"contract_id":"openlinker.browser.origin-budgets.v1","entries":[` +
			`{"updated_at_ms":1,"key":"` + keyA + `","action_ms":[],"navigation_ms":[]}]}`,
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeOriginBudgetFixture(t, root, raw, 0o600)
			if err := validateOriginBudgetState(root); err == nil {
				t.Fatal("invalid origin-budget state was accepted")
			}
		})
	}
}

func TestOriginBudgetStateRejectsUnsafePermissionsAndSymlink(t *testing.T) {
	t.Parallel()
	unsafeRoot := t.TempDir()
	writeOriginBudgetFixture(t, unsafeRoot, validEmptyOriginBudget, 0o644)
	if err := validateOriginBudgetState(unsafeRoot); err == nil {
		t.Fatal("permission-unsafe origin-budget file was accepted")
	}

	symlinkRoot := t.TempDir()
	directory := filepath.Join(symlinkRoot, originBudgetDirectory)
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "budget.json")
	if err := os.WriteFile(target, []byte(validEmptyOriginBudget), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(directory, originBudgetFile)); err != nil {
		t.Fatal(err)
	}
	if err := validateOriginBudgetState(symlinkRoot); err == nil {
		t.Fatal("symlinked origin-budget file was accepted")
	}
}

func writeOriginBudgetFixture(
	t *testing.T,
	root string,
	raw string,
	mode os.FileMode,
) {
	t.Helper()
	directory := filepath.Join(root, originBudgetDirectory)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(directory, originBudgetFile),
		[]byte(raw),
		mode,
	); err != nil {
		t.Fatal(err)
	}
}
