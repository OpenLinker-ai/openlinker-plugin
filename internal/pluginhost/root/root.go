package root

import (
	"fmt"
	"io"
	"os"

	"github.com/OpenLinker-ai/openlinker-plugin/internal/pluginhost/agent"
	"github.com/OpenLinker-ai/openlinker-plugin/internal/pluginhost/buildinfo"
	"github.com/OpenLinker-ai/openlinker-plugin/internal/pluginhost/plugin"
	"github.com/OpenLinker-ai/openlinker-plugin/internal/pluginhost/shared"
	agentapp "github.com/OpenLinker-ai/openlinker-plugin/packages/agent-adapters/agent"
	"github.com/spf13/cobra"
)

// Run owns the Plugin command/MCP lifecycle. It never invokes the platform CLI.
func Run(args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) int {
	if getenv == nil {
		getenv = os.Getenv
	}
	streams := shared.IO{Stdin: stdin, Stdout: stdout, Stderr: stderr, Getenv: getenv}
	opts := shared.DefaultGlobalOptions(getenv)
	command := &cobra.Command{Use: "openlinker-plugin-host", Short: "Run OpenLinker native Plugin services", SilenceUsage: true, SilenceErrors: true}
	command.SetOut(stderr)
	command.SetErr(stderr)
	command.SetArgs(args)
	command.PersistentFlags().StringVar(&opts.APIBase, "api", opts.APIBase, "OpenLinker Core API base URL")
	command.PersistentFlags().StringVar(&opts.UserToken, "token", opts.UserToken, "OpenLinker User Token for caller tools only")
	command.PersistentFlags().DurationVar(&opts.Timeout, "timeout", opts.Timeout, "caller request timeout")
	service := agentapp.NewService(getenv, nil, buildinfo.Version)
	command.AddCommand(agent.New(streams, service), plugin.New(streams, &opts, service))
	command.AddCommand(&cobra.Command{Use: "context", Args: cobra.NoArgs, RunE: func(_ *cobra.Command, _ []string) error {
		return shared.WriteJSON(stdout, map[string]any{"host_version": buildinfo.Version, "plugin_commit": buildinfo.Revision, "surface_version": buildinfo.SurfaceVersion, "capabilities": buildinfo.Capabilities()})
	}})
	if err := command.Execute(); err != nil {
		fmt.Fprintf(stderr, "openlinker-plugin-host: %v\n", err)
		return 1
	}
	return 0
}
