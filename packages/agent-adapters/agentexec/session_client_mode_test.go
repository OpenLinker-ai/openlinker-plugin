package agentexec

import (
	"path/filepath"
	"testing"
)

func TestProviderSessionClientModeChangeAdvancesGeneration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	workspace := t.TempDir()
	if err := saveSessionForClientMode(
		path,
		"codex",
		workspace,
		"conversation",
		"session-native",
		"browser_native",
		4,
	); err != nil {
		t.Fatal(err)
	}
	if sessionID, generation, changed := loadSessionForClientMode(
		path,
		"codex",
		workspace,
		"conversation",
		"browser_native",
	); sessionID != "session-native" || generation != 4 || changed {
		t.Fatalf(
			"same-mode Session = %q generation=%d changed=%v",
			sessionID,
			generation,
			changed,
		)
	}
	if sessionID, generation, changed := loadSessionForClientMode(
		path,
		"codex",
		workspace,
		"conversation",
		"browser_mcp",
	); sessionID != "" || generation != 5 || !changed {
		t.Fatalf(
			"changed-mode Session = %q generation=%d changed=%v",
			sessionID,
			generation,
			changed,
		)
	}
}

func TestLegacyBrowserSessionIsDirectMCPGeneration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	workspace := t.TempDir()
	if err := saveSessionID(
		path,
		"codex",
		workspace,
		"conversation",
		"legacy-session",
	); err != nil {
		t.Fatal(err)
	}
	if sessionID, generation, changed := loadSessionForClientMode(
		path,
		"codex",
		workspace,
		"conversation",
		"browser_mcp",
	); sessionID != "legacy-session" || generation != 1 || changed {
		t.Fatalf(
			"legacy direct-MCP Session = %q generation=%d changed=%v",
			sessionID,
			generation,
			changed,
		)
	}
	if sessionID, generation, changed := loadSessionForClientMode(
		path,
		"codex",
		workspace,
		"conversation",
		"browser_native",
	); sessionID != "" || generation != 2 || !changed {
		t.Fatalf(
			"legacy native transition = %q generation=%d changed=%v",
			sessionID,
			generation,
			changed,
		)
	}
}

func TestOfficialChromeUsesAnIndependentProviderSessionGeneration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	workspace := t.TempDir()
	if mode := providerSessionClientMode(ProviderConfig{
		ExecutionProfile:       "browser",
		BrowserClientMode:      "native",
		BrowserBackendSelected: "official_chrome_extension",
	}); mode != "browser_native_official_chrome" {
		t.Fatalf("official Chrome Provider mode = %q", mode)
	}
	if err := saveSessionForClientMode(
		path,
		"codex",
		workspace,
		"conversation",
		"isolated-session",
		"browser_native",
		2,
	); err != nil {
		t.Fatal(err)
	}
	if sessionID, generation, changed := loadSessionForClientMode(
		path,
		"codex",
		workspace,
		"conversation",
		"browser_native_official_chrome",
	); sessionID != "" || generation != 3 || !changed {
		t.Fatalf(
			"official Chrome transition = %q generation=%d changed=%v",
			sessionID,
			generation,
			changed,
		)
	}
}
