package agentexec

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/providertest"
)

const fixtureThread = providertest.FixtureThread
const fixtureTurn = providertest.FixtureTurn

func writeCodexRPCFixture(t *testing.T, path, scenario string) {
	t.Helper()
	providertest.WriteCodexRPCFixture(t, path, scenario)
}

// Keep the fake-process seam available to this package's delegation regressions.
type rpcFixture = providertest.RPCFixture

func startRPCFixture(scenario string) *rpcFixture {
	return providertest.StartRPCFixture(scenario)
}

func TestCodexRPCFixtureProcess(t *testing.T) {
	providertest.CodexRPCFixtureProcess()
}

// Only test setup is shared. This callback always runs this package's provider.
func runCodexFixture(ctx context.Context, c providertest.CodexConfig, input any, sessionKey string) error {
	config := ProviderConfig{
		Bin: c.Bin, Workspace: c.Workspace, SessionStore: c.SessionStore,
		SessionReuse: c.SessionReuse, Timeout: c.Timeout,
		Env: c.Env, EnvAllowlist: c.EnvAllowlist,
	}
	run := RunContext{Input: input}
	if sessionKey != "" {
		run.Conversation = &ConversationContext{SessionKey: sessionKey}
	}
	_, err := (CodexProvider{Config: config}).Run(ctx, run)
	return err
}

func TestCodexRPCDrainsShutdownBeforeWaiting(t *testing.T) {
	providertest.CodexRPCDrainsShutdownBeforeWaiting(t, func(ctx context.Context, bin, dir string) (string, error) {
		_, answer, err := runCodexRPC(ctx, bin, dir, "read-only", "", "test shutdown", false, ProviderConfig{}, nil)
		return answer, err
	})
}

func TestCodexRPCAlreadyCanceledDoesNotLaunch(t *testing.T) {
	providertest.CodexRPCAlreadyCanceledDoesNotLaunch(t, func(ctx context.Context, bin, dir string) (string, error) {
		_, answer, err := runCodexRPC(ctx, bin, dir, "read-only", "", "canceled", false, ProviderConfig{}, nil)
		return answer, err
	})
}

func TestCodexRPCBrowserInstallPrecedesThreadAndFailsClosed(t *testing.T) {
	for _, scenario := range []string{"plugin-install", "plugin-install-failed", "plugin-install-auth"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			bin, log := filepath.Join(dir, "codex"), filepath.Join(dir, "protocol")
			writeCodexRPCFixture(t, bin, scenario)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			config := ProviderConfig{ExecutionProfile: "browser", BrowserClientMode: "native", BrowserNativePlugin: dir,
				Env: append(os.Environ(), "TEST_LOG="+log), EnvAllowlist: []string{"TEST_LOG"}}
			_, answer, err := runCodexRPC(ctx, bin, dir, "read-only", "", "browser task", false, config, nil)
			raw, readErr := os.ReadFile(log + ".requests")
			if readErr != nil {
				t.Fatal(readErr)
			}
			requests := string(raw)
			installed, thread, turn := strings.Index(requests, " plugin/install\n"), strings.Index(requests, " thread/start\n"), strings.Index(requests, " turn/start\n")
			if installed < 0 {
				t.Fatalf("Browser hook was not called: %s", requests)
			}
			if scenario == "plugin-install" {
				if err != nil || answer != "provider answer" || thread <= installed || turn <= thread {
					t.Fatalf("Browser installation order changed: %v %s", err, requests)
				}
			} else if err == nil || answer != "" || thread >= 0 || turn >= 0 {
				t.Fatalf("Browser installation failure allowed execution: %v %s", err, requests)
			}
			args, err := os.ReadFile(log + ".args")
			if err != nil || !strings.Contains(string(args), "--enable plugins") || strings.Contains(string(args), "--disable plugins") {
				t.Fatalf("Browser launch policy changed: %v %s", err, args)
			}
		})
	}
}

func TestCodexRPCCancellationInterruptsScopedTurn(t *testing.T) {
	providertest.CodexRPCCancellationInterruptsScopedTurn(t, runCodexFixture)
}

func TestCodexRPCFailureDoesNotPersistSession(t *testing.T) {
	providertest.CodexRPCFailureDoesNotPersistSession(t, runCodexFixture)
}

func TestCodexRPCDoesNotRecoverUnrelatedResumeErrors(t *testing.T) {
	providertest.CodexRPCDoesNotRecoverUnrelatedResumeErrors(t, runCodexFixture, func(store, workspace string) error {
		return saveSessionForClientMode(store, "codex", workspace, "conversation", fixtureThread, "codex_rpc_v1:standard", 1)
	})
}

func TestCodexRPCResolvesRelativeWorkspaceAndDottedTrustKey(t *testing.T) {
	providertest.CodexRPCResolvesRelativeWorkspaceAndDottedTrustKey(t, runCodexFixture)
}
