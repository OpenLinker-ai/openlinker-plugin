//go:build !windows

package browserruntime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestGoOriginBudgetMatchesSharedContractFixtures(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(
		"..",
		"browser-engine",
		"contract-fixtures.json",
	))
	if err != nil {
		t.Fatal(err)
	}
	var fixtures struct {
		OriginBudgets []struct {
			Value    string `json:"value"`
			Accepted bool   `json:"accepted"`
		} `json:"origin_budgets"`
	}
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range fixtures.OriginBudgets {
		root := t.TempDir()
		writeOriginBudgetFixture(t, root, fixture.Value, 0o600)
		validationErr := validateOriginBudgetState(root)
		if fixture.Accepted && validationErr != nil {
			t.Errorf("accepted origin-budget fixture failed: %v", validationErr)
		}
		if !fixture.Accepted && validationErr == nil {
			t.Errorf("rejected origin-budget fixture passed: %s", fixture.Value)
		}
	}
}
