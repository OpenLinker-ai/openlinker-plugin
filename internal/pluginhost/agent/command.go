package agent

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/OpenLinker-ai/openlinker-plugin/internal/pluginhost/buildinfo"
	"github.com/OpenLinker-ai/openlinker-plugin/internal/pluginhost/shared"
	agentapp "github.com/OpenLinker-ai/openlinker-plugin/packages/agent-adapters/agent"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

func New(ioStreams shared.IO, service *agentapp.Service) *cobra.Command {
	if service == nil {
		service = agentapp.NewService(ioStreams.Getenv, nil, buildinfo.Version)
	}
	command := &cobra.Command{Use: "agent", Short: "Configure and serve this Plugin as an OpenLinker Agent"}
	command.AddCommand(newConfigureCommand(ioStreams))
	command.AddCommand(newServeCommand(service))
	command.AddCommand(newStatusCommand(ioStreams, service))
	command.AddCommand(newDoctorCommand(ioStreams))
	return command
}

func newConfigureCommand(ioStreams shared.IO) *cobra.Command {
	var provider, agentID, workspace, platformURL, state, bin, model, transport, codexBaseURL, sandbox, approval, permission string
	var executionProfile, browserInteractionPolicy, browserClientMode, browserPluginBin, browserNativePlugin, browserSocket, browserCredentialFile, browserLeaseRoot, browserBrokerRoot string
	var capacity int64
	var timeout int
	var webSearch, sessionReuse, enabled bool
	var allowedTools, delegationTargets shared.StringList
	var delegationProxyBin, delegationBrokerRoot string
	command := &cobra.Command{
		Use:   "configure",
		Short: "Write non-secret Agent mode configuration",
		RunE: func(command *cobra.Command, args []string) error {
			changed := make([]string, 0)
			command.Flags().Visit(func(flag *pflag.Flag) { changed = append(changed, flag.Name) })
			config, path, err := agentapp.ConfigureNonSecret(ioStreams.Getenv, agentapp.ConfigureOptions{
				ChangedFields: changed,
				Provider:      provider, AgentID: agentID, Workspace: workspace,
				OpenLinkerURL: platformURL, StateDir: state, ProviderBin: bin,
				Model: model, Transport: transport, Capacity: capacity, TimeoutSeconds: timeout,
				SessionReuse: &sessionReuse, WebSearch: &webSearch, Enabled: &enabled,
				CodexBaseURL: codexBaseURL, CodexSandbox: sandbox, CodexApproval: approval,
				ClaudePermission: permission, AllowedTools: allowedTools,
				DelegationTargets: delegationTargets, DelegationProxyBin: delegationProxyBin, DelegationBrokerRoot: delegationBrokerRoot,
				ExecutionProfile: executionProfile, BrowserInteractionPolicy: browserInteractionPolicy,
				BrowserClientMode: browserClientMode, BrowserPluginBin: browserPluginBin,
				BrowserNativePlugin: browserNativePlugin, BrowserSocket: browserSocket,
				BrowserCredentialFile: browserCredentialFile, BrowserLeaseRoot: browserLeaseRoot,
				BrowserBrokerRoot: browserBrokerRoot,
			})
			if err != nil {
				return err
			}
			return shared.WriteJSON(ioStreams.Stdout, map[string]any{
				"configured": true, "config_path": path, "provider": config.Provider,
				"agent_id": config.AgentID, "workspace": config.Workspace, "enabled": config.Enabled,
				"secrets_written": false,
			})
		},
	}
	command.Flags().StringVar(&provider, "provider", "", "codex or claude")
	command.Flags().StringVar(&agentID, "agent-id", "", "existing OpenLinker Agent UUID")
	command.Flags().StringVar(&workspace, "workspace", "", "provider workspace")
	command.Flags().StringVar(&platformURL, "url", "", "public OpenLinker platform URL")
	command.Flags().StringVar(&state, "state-dir", "", "private persistent Agent state directory")
	command.Flags().StringVar(&bin, "provider-bin", "", "provider CLI binary")
	command.Flags().StringVar(&model, "model", "", "provider model")
	command.Flags().StringVar(&transport, "transport", "", "auto, websocket, or pull")
	command.Flags().Int64Var(&capacity, "capacity", 1, "maximum concurrent Runs")
	command.Flags().IntVar(&timeout, "timeout", 1800, "provider execution timeout in seconds")
	command.Flags().BoolVar(&sessionReuse, "session-reuse", true, "reuse provider sessions by Core conversation")
	command.Flags().BoolVar(&webSearch, "web-search", false, "allow provider web search")
	command.Flags().StringVar(&codexBaseURL, "codex-base-url", "", "Codex OpenAI-compatible API Base URL")
	command.Flags().StringVar(&sandbox, "codex-sandbox", "read-only", "Codex sandbox mode: read-only, workspace-write, or danger-full-access for externally isolated runtimes")
	command.Flags().StringVar(&approval, "codex-approval", "never", "Codex approval mode")
	command.Flags().StringVar(&permission, "claude-permission", "dontAsk", "Claude permission mode")
	command.Flags().Var(&delegationTargets, "delegation-target", "allowed target Agent UUID; repeatable, empty clears delegation")
	command.Flags().StringVar(&delegationProxyBin, "delegation-proxy-bin", "", "OpenLinker Plugin host binary used for delegated Agent tools")
	command.Flags().StringVar(&delegationBrokerRoot, "delegation-broker-root", "", "private local directory for Attempt delegation sockets")
	command.Flags().Var(&allowedTools, "allowed-tool", "Claude allowed tool; repeatable")
	command.Flags().StringVar(&executionProfile, "execution-profile", "standard", "Agent execution profile: standard or browser")
	command.Flags().StringVar(&browserInteractionPolicy, "browser-interaction-policy", "restricted", "Browser interaction policy: restricted or full")
	command.Flags().StringVar(&browserClientMode, "browser-client-mode", "mcp", "Browser client mode: auto, openlinker-native-chrome, isolated-native, isolated-mcp, native, or mcp")
	command.Flags().StringVar(&browserPluginBin, "browser-plugin-bin", "", "OpenLinker Plugin host binary used for the Browser-only tool server")
	command.Flags().StringVar(&browserNativePlugin, "browser-native-plugin", "", "absolute Runtime-owned native Browser Plugin path")
	command.Flags().StringVar(&browserSocket, "browser-socket", "", "private Browser Runtime Unix socket")
	command.Flags().StringVar(&browserCredentialFile, "browser-credential-file", "", "owner-only Browser channel credential file")
	command.Flags().StringVar(&browserLeaseRoot, "browser-lease-root", "", "private shared Browser lease directory")
	command.Flags().StringVar(&browserBrokerRoot, "browser-broker-root", "", "private local Browser tool broker directory")
	command.Flags().BoolVar(&enabled, "enabled", false, "persist Agent mode enable state")
	return command
}

func newServeCommand(service *agentapp.Service) *cobra.Command {
	var provider string
	command := &cobra.Command{
		Use:   "serve",
		Short: "Run the reliable OpenLinker Runtime Worker in the foreground",
		RunE: func(command *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(command.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return service.Run(ctx, provider)
		},
	}
	command.Flags().StringVar(&provider, "provider", "", "override provider: codex or claude")
	return command
}

func newStatusCommand(ioStreams shared.IO, service *agentapp.Service) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show redacted Agent mode status",
		RunE: func(command *cobra.Command, args []string) error {
			status, err := agentapp.ReadStatus(ioStreams.Getenv, service)
			if err != nil {
				return err
			}
			return shared.WriteJSON(ioStreams.Stdout, status)
		},
	}
}

func newDoctorCommand(ioStreams shared.IO) *cobra.Command {
	var provider string
	command := &cobra.Command{
		Use:   "doctor",
		Short: "Check Agent mode without exposing credentials",
		RunE: func(command *cobra.Command, args []string) error {
			diagnostic := agentapp.Diagnose(ioStreams.Getenv, provider)
			return shared.WriteJSON(ioStreams.Stdout, diagnostic)
		},
	}
	command.Flags().StringVar(&provider, "provider", "", "override provider: codex or claude")
	return command
}
