package root

import (
	"encoding/json"
	openlinker "github.com/OpenLinker-ai/openlinker-go"
	agentapp "github.com/OpenLinker-ai/openlinker-plugin/packages/agent-adapters/agent"
	"github.com/spf13/cobra"
	"io"
)

func newRuntimeCredentialCommand(stdout io.Writer, getenv func(string) string) *cobra.Command {
	options := openlinker.RuntimeCredentialPreparation{}
	command := &cobra.Command{Use: "runtime-credential", Short: "Prepare the existing stopped Runtime identity for administrative mTLS migration"}
	prepare := &cobra.Command{Use: "prepare", Args: cobra.NoArgs, RunE: func(_ *cobra.Command, _ []string) error {
		result, err := agentapp.PrepareCredential(getenv, options)
		if err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(result)
	}}
	prepare.Flags().StringVar(&options.DataDir, "data-dir", "", "existing stopped Runtime data directory")
	prepare.Flags().StringVar(&options.NodeID, "node-id", "", "existing Core Node ID")
	prepare.Flags().StringVar(&options.AgentID, "agent-id", "", "existing Core Agent ID")
	prepare.Flags().StringVar(&options.WorkerID, "worker-id", "", "existing Worker ID in that directory")
	command.AddCommand(prepare)
	return command
}
