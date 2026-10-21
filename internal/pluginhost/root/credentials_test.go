package root

import (
	"bytes"
	"encoding/json"
	openlinker "github.com/OpenLinker-ai/openlinker-go"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRuntimeCredentialPrepareEntryPreservesIdentityWithoutProviderStartup(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(503) }))
	defer server.Close()
	var configPath string
	execute := func(args []string) ([]byte, bool) {
		var out, stderr bytes.Buffer
		code := Run(args, strings.NewReader(""), &out, &stderr, func(key string) string {
			switch key {
			case "OPENLINKER_AGENT_CONFIG":
				return configPath
			case "OPENLINKER_URL":
				return server.URL
			case "OPENLINKER_AGENT_PROVIDER":
				return "invalid-must-not-initialize"
			}
			return ""
		})
		if code != 0 {
			return stderr.Bytes(), true
		}
		return out.Bytes(), false
	}
	stateDir := t.TempDir()
	dataDir := filepath.Join(stateDir, "runtime")
	if err := os.WriteFile(filepath.Join(stateDir, "node-id"), []byte("11111111-1111-4111-8111-111111111111\n"), 0600); err != nil {
		t.Fatal(err)
	}
	configPath = filepath.Join(stateDir, "agent.json")
	rawConfig, err := json.Marshal(map[string]any{"version": 1, "agent_id": "22222222-2222-4222-8222-222222222222", "state_dir": stateDir, "provider": "invalid-must-not-initialize"})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(configPath, rawConfig, 0600); err != nil {
		t.Fatal(err)
	}
	store, err := openlinker.OpenFileRuntimeStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	identity := store.Identity()
	args := []string{"runtime-credential", "prepare", "--data-dir", dataDir, "--node-id", "11111111-1111-4111-8111-111111111111", "--agent-id", "22222222-2222-4222-8222-222222222222", "--worker-id", identity.WorkerID}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	wrong := append([]string(nil), args...)
	wrong[5] = "33333333-3333-4333-8333-333333333333"
	if out, failed := execute(wrong); !failed {
		t.Fatalf("wrong persisted Node accepted: %s", out)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "runtime-credential.json")); !os.IsNotExist(err) {
		t.Fatal("wrong Node wrote credentials")
	}
	wrong = append([]string(nil), args...)
	wrong[7] = "33333333-3333-4333-8333-333333333333"
	if out, failed := execute(wrong); !failed {
		t.Fatalf("wrong configured Agent accepted: %s", out)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "runtime-credential.json")); !os.IsNotExist(err) {
		t.Fatal("wrong Agent wrote credentials")
	}
	var prepared openlinker.PreparedRuntimeCredential
	out, failed := execute(args)
	if failed || json.Unmarshal(out, &prepared) != nil {
		t.Fatalf("prepare failed: %s", out)
	}
	if prepared.WorkerID != identity.WorkerID || prepared.SessionEpoch != identity.SessionEpoch || prepared.NodeID != args[5] || prepared.AgentID != args[7] || len(prepared.PublicKeyThumbprint) != 64 || strings.Contains(string(out), "PRIVATE KEY") {
		t.Fatalf("unexpected public preparation receipt: %s", out)
	}
	out, failed = execute(args)
	var again openlinker.PreparedRuntimeCredential
	if failed || json.Unmarshal(out, &again) != nil || again.PublicKeyThumbprint != prepared.PublicKeyThumbprint || again.SessionEpoch != identity.SessionEpoch {
		t.Fatalf("preparation is not stable: %s", out)
	}
	store, err = openlinker.OpenFileRuntimeStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if store.Identity().WorkerID != identity.WorkerID || store.Identity().SessionEpoch != identity.SessionEpoch+1 {
		t.Fatal("preparation modified persisted Worker epoch")
	}
	if out, failed = execute(args); !failed {
		t.Fatalf("running Runtime store accepted: %s", out)
	}
	if out, failed = execute(append(args, "unexpected")); !failed {
		t.Fatalf("unexpected argument accepted: %s", out)
	}
	if requests.Load() != 0 {
		t.Fatal("offline preparation contacted a service")
	}
}
