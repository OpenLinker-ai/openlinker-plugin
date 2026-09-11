package plugin

import (
	"errors"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/agentdelegation"
	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/agenthost"
	"github.com/OpenLinker-ai/openlinker-plugin/internal/pluginhost/buildinfo"
	"github.com/OpenLinker-ai/openlinker-plugin/internal/pluginhost/pluginbridge"
	"github.com/OpenLinker-ai/openlinker-plugin/internal/pluginhost/shared"
	"github.com/OpenLinker-ai/openlinker-plugin/packages/agent-adapters/agent"
	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserclient"
	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserplugin"
	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserprotocol"
	"github.com/spf13/cobra"
)

func New(ioStreams shared.IO, options *shared.GlobalOptions, agentService *agent.Service) *cobra.Command {
	command := &cobra.Command{Use: "plugin", Short: "Run OpenLinker native plugin services"}
	command.AddCommand(newServeCommand(ioStreams, options, agentService))
	command.AddCommand(newBrowserServeCommand(ioStreams))
	command.AddCommand(newBrowserProxyCommand(ioStreams))
	command.AddCommand(newDelegationProxyCommand(ioStreams))
	command.AddCommand(&cobra.Command{Use: "capabilities", Hidden: true, Args: cobra.NoArgs, RunE: func(command *cobra.Command, args []string) error {
		return shared.WriteJSON(ioStreams.Stdout, agenthost.SupportedCapabilities())
	}})
	return command
}

func newBrowserProxyCommand(ioStreams shared.IO) *cobra.Command {
	var host string
	command := &cobra.Command{
		Use:    "browser-proxy",
		Short:  "Proxy stdio to the trusted Browser tool broker",
		Hidden: true,
		RunE: func(command *cobra.Command, args []string) error {
			host = strings.ToLower(strings.TrimSpace(host))
			if host != "codex" && host != "claude" {
				return errors.New("plugin browser-proxy requires --host codex or --host claude")
			}
			getenv := browserToolGetenv(ioStreams.Getenv)
			for _, name := range []string{
				"CODEX_API_KEY",
				"ANTHROPIC_API_KEY",
				"OPENLINKER_AGENT_TOKEN",
				"OPENLINKER_USER_TOKEN",
			} {
				_ = os.Unsetenv(name)
			}
			ctx, stop := signal.NotifyContext(
				command.Context(),
				os.Interrupt,
				syscall.SIGTERM,
			)
			defer stop()
			return runBrowserProxy(ctx, ioStreams.Stdin, ioStreams.Stdout, getenv)
		},
	}
	command.Flags().StringVar(&host, "host", "", "native host: codex or claude")
	return command
}

func newServeCommand(ioStreams shared.IO, options *shared.GlobalOptions, agentService *agent.Service) *cobra.Command {
	var host string
	command := &cobra.Command{
		Use:   "serve",
		Short: "Serve the local OpenLinker MCP bridge over stdio",
		RunE: func(command *cobra.Command, args []string) error {
			host = strings.ToLower(strings.TrimSpace(host))
			if host != "codex" && host != "claude" {
				return errors.New("plugin serve requires --host codex or --host claude")
			}
			ctx, stop := signal.NotifyContext(command.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			server := &pluginbridge.Server{Host: host, IO: ioStreams, Options: options}
			if agentService != nil {
				server.Agent = agentService
			}
			return server.Serve(ctx, ioStreams.Stdin, ioStreams.Stdout)
		},
	}
	command.Flags().StringVar(&host, "host", "", "native host: codex or claude")
	return command
}

func newBrowserServeCommand(ioStreams shared.IO) *cobra.Command {
	var host string
	command := &cobra.Command{
		Use:   "browser-serve",
		Short: "Serve the client-owned Browser tool over stdio",
		RunE: func(command *cobra.Command, args []string) error {
			host = strings.ToLower(strings.TrimSpace(host))
			if host != "codex" && host != "claude" {
				return errors.New("plugin browser-serve requires --host codex or --host claude")
			}
			ioStreams.Getenv = browserToolGetenv(ioStreams.Getenv)
			for _, name := range []string{
				"CODEX_API_KEY",
				"ANTHROPIC_API_KEY",
				"OPENLINKER_AGENT_TOKEN",
				"OPENLINKER_USER_TOKEN",
			} {
				_ = os.Unsetenv(name)
			}
			ctx, stop := signal.NotifyContext(
				command.Context(),
				os.Interrupt,
				syscall.SIGTERM,
			)
			defer stop()
			server := &browserplugin.Server{
				Host:    host,
				IO:      browserplugin.IO{Getenv: ioStreams.Getenv},
				Version: buildinfo.Version,
				IdentitySupplier: func() (browserprotocol.Identity, error) {
					return browserclient.LoadLeaseIdentityFromEnv(ioStreams.Getenv)
				},
			}
			return server.Serve(ctx, ioStreams.Stdin, ioStreams.Stdout)
		},
	}
	command.Flags().StringVar(&host, "host", "", "native host: codex or claude")
	return command
}

func browserToolGetenv(getenv func(string) string) func(string) string {
	if getenv == nil {
		getenv = os.Getenv
	}
	return func(name string) string {
		switch name {
		case "CODEX_API_KEY",
			"ANTHROPIC_API_KEY",
			"OPENLINKER_AGENT_TOKEN",
			"OPENLINKER_USER_TOKEN":
			return ""
		default:
			return getenv(name)
		}
	}
}

func newDelegationProxyCommand(ioStreams shared.IO) *cobra.Command {
	var host string
	command := &cobra.Command{Use: "delegation-proxy", Hidden: true, Args: cobra.NoArgs,
		Short: "Proxy stdio to the active Attempt delegation broker",
		RunE: func(command *cobra.Command, args []string) error {
			if host != "codex" && host != "claude" {
				return errors.New("delegation-proxy requires --host codex or --host claude")
			}
			getenv := ioStreams.Getenv
			if getenv == nil {
				getenv = os.Getenv
			}
			socket := getenv(agentdelegation.SocketEnvironment)
			for _, name := range []string{"CODEX_API_KEY", "ANTHROPIC_API_KEY", "OPENLINKER_AGENT_TOKEN", "OPENLINKER_USER_TOKEN"} {
				_ = os.Unsetenv(name)
			}
			ctx, stop := signal.NotifyContext(command.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return agentdelegation.Proxy(ctx, ioStreams.Stdin, ioStreams.Stdout, socket)
		},
	}
	command.Flags().StringVar(&host, "host", "", "native host: codex or claude")
	return command
}
