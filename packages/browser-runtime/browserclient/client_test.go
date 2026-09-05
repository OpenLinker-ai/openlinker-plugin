package browserclient

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserprotocol"
)

func TestNewLoadsOnlyOwnerProtectedCredentialAndAuthoritativeLease(t *testing.T) {
	now := time.Date(2026, 7, 25, 1, 2, 3, 0, time.UTC)
	config, identity := writeClientConfig(t, now, time.Minute)
	client, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	if !browserprotocol.SameIdentity(client.Identity(), identity) {
		t.Fatalf("identity = %#v, want %#v", client.Identity(), identity)
	}
}

func TestNewRejectsInsecureCredentialAndLeaseFiles(t *testing.T) {
	now := time.Date(2026, 7, 25, 1, 2, 3, 0, time.UTC)
	tests := []struct {
		name   string
		mutate func(Config) error
	}{
		{
			name: "credential",
			mutate: func(config Config) error {
				return os.Chmod(config.CredentialFile, 0o644)
			},
		},
		{
			name: "lease",
			mutate: func(config Config) error {
				return os.Chmod(config.LeaseFile, 0o644)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config, _ := writeClientConfig(t, now, time.Minute)
			if err := test.mutate(config); err != nil {
				t.Fatal(err)
			}
			if _, err := New(config); err == nil || strings.Contains(err.Error(), strings.Repeat("c", 32)) {
				t.Fatalf("New error = %v", err)
			}
		})
	}
}

func TestNewRejectsExpiredOrUnknownLease(t *testing.T) {
	now := time.Date(2026, 7, 25, 1, 2, 3, 0, time.UTC)
	config, _ := writeClientConfig(t, now, -time.Second)
	if _, err := New(config); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired New error = %v", err)
	}
	config, _ = writeClientConfig(t, now, time.Minute)
	raw, err := os.ReadFile(config.LeaseFile)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	value["caller_selected_identity"] = map[string]any{"run_id": "spoofed"}
	raw, _ = json.Marshal(value)
	if err := os.WriteFile(config.LeaseFile, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(config); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("unknown-field New error = %v", err)
	}
}

func writeClientConfig(
	t *testing.T,
	now time.Time,
	leaseDuration time.Duration,
) (Config, browserprotocol.Identity) {
	t.Helper()
	dir, err := os.MkdirTemp("", "olbc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	credentialPath := filepath.Join(dir, "channel")
	if err := os.WriteFile(credentialPath, []byte(strings.Repeat("c", 32)), 0o600); err != nil {
		t.Fatal(err)
	}
	identity := browserprotocol.Identity{
		RunID:                              "11111111-1111-4111-8111-111111111111",
		AgentID:                            "22222222-2222-4222-8222-222222222222",
		PrincipalScopeID:                   "principal-owner",
		BrowserSessionID:                   "33333333-3333-4333-8333-333333333333",
		SessionEpoch:                       7,
		AttachmentID:                       "44444444-4444-4444-8444-444444444444",
		ControlEpoch:                       9,
		Controller:                         browserprotocol.ControllerAgent,
		BrowserInteractionPolicy:           "restricted",
		BrowserInteractionPolicyGeneration: 1,
		BrowserMutationOrigins:             []string{},
		BrowserMutationOriginsSHA256:       browserprotocol.RestrictedMutationOriginsSHA256,
	}
	lease := Lease{
		ContractID: LeaseContractID,
		ExpiresAt:  now.Add(leaseDuration),
		Identity:   identity,
	}
	raw, err := json.Marshal(lease)
	if err != nil {
		t.Fatal(err)
	}
	leasePath := filepath.Join(dir, "lease.json")
	if err := os.WriteFile(leasePath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return Config{
		SocketPath:     filepath.Join(dir, "browser.sock"),
		CredentialFile: credentialPath,
		LeaseFile:      leasePath,
		Timeout:        5 * time.Second,
		Now:            func() time.Time { return now },
	}, identity
}
