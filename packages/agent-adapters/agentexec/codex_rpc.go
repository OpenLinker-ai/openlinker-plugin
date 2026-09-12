package agentexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/codexrpc"
	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/codexturn"
)

var errCodexSessionMissing = codexturn.ErrSessionMissing

// Plugin retains Browser installation, launch flags and progress policy; the
// Node leaf owns the common app-server turn and cancellation lifecycle.
func runCodexRPC(ctx context.Context, bin, workspace, sandbox, sessionID, prompt string, persistent bool, config ProviderConfig, emit func(string, any) error) (string, string, error) {
	var beforeThread func(context.Context, *codexrpc.Client) error
	if nativeBrowserClientEnabled(config) {
		beforeThread = func(ctx context.Context, client *codexrpc.Client) error {
			marketplace := codexrpc.AbsolutePathBuf(filepath.Join(config.BrowserNativePlugin, ".agents", "plugins", "marketplace.json"))
			var installed codexrpc.PluginInstallResponse
			if err := client.Call(ctx, "plugin/install", codexrpc.PluginInstallParams{MarketplacePath: &marketplace, PluginName: "openlinker"}, &installed); err != nil {
				return fmt.Errorf("activate Runtime-owned Browser Plugin: %w", err)
			}
			if len(installed.AppsNeedingAuth) > 0 {
				return errors.New("Runtime Browser Plugin unexpectedly requires app authentication")
			}
			return nil
		}
	}
	return codexturn.Run(ctx, codexturn.Config{
		Prepare: func(processCtx context.Context) (codexturn.PreparedCommand, error) {
			return codexturn.PrepareNative(processCtx, codexturn.NativeCommand{
				Bin: bin, Workspace: workspace, Env: config.Env, EnvAllowlist: config.EnvAllowlist,
				Arguments: func(cwd string) []string { return codexAppServerArguments(config, cwd, sandbox) },
			})
		},
		Sandbox: sandbox, SessionID: sessionID, Prompt: prompt, Persistent: persistent,
		Model: config.Model, Approval: config.CodexApproval, BeforeThread: beforeThread,
		Observer: newCodexJSONLObserver(emit, browserProfileEnabled(config)), Emit: emit,
	})
}

func codexAppServerArguments(config ProviderConfig, workspace, sandbox string) []string {
	args := codexLaunchConfiguration(config, sandbox)
	// Do not hydrate account-installed plugins or start remote-control/hooks
	// from a native login. Only the declared local Browser bundle is installed.
	args = append(args, "--disable", "remote_plugin", "--disable", "remote_control", "--disable", "hooks", "--disable", "apps")
	if nativeBrowserClientEnabled(config) {
		args = append(args, "--enable", "plugins")
	} else {
		args = append(args, "--disable", "plugins")
	}
	args = append(args, "-c", `cli_auth_credentials_store="file"`, "-c", "projects={"+jsonString(workspace)+"={trust_level=\"untrusted\"}}")
	return append(args, "app-server", "--listen", "stdio://")
}

func jsonString(value string) string { raw, _ := json.Marshal(value); return string(raw) }
