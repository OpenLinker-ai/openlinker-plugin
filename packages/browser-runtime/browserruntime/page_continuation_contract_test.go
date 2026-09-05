//go:build !windows

package browserruntime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestGoPageContinuationMatchesSharedContractFixtures(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(
		"..",
		"browser-engine",
		"contract-fixtures.json",
	))
	if err != nil {
		t.Fatal(err)
	}
	var fixtures struct {
		Continuations []struct {
			Value      string  `json:"value"`
			Normalized *string `json:"normalized"`
		} `json:"continuations"`
	}
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range fixtures.Continuations {
		normalized, normalizeErr := normalizePageContinuationURL(fixture.Value)
		if fixture.Normalized == nil {
			if normalizeErr == nil {
				t.Errorf(
					"continuation %q normalized to %q, want rejection",
					fixture.Value,
					normalized,
				)
			}
			continue
		}
		if normalizeErr != nil || normalized != *fixture.Normalized {
			t.Errorf(
				"continuation %q normalized to %q, %v; want %q",
				fixture.Value,
				normalized,
				normalizeErr,
				*fixture.Normalized,
			)
		}
	}
}
