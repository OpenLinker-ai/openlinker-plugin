package agentexec

import (
	"os/exec"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/providerprocess"
)

// Process operations are a shared mechanism. Plugin still owns its execution,
// tools, history and private-session policy and never invokes a Node Provider.
func configureProviderProcess(command *exec.Cmd) { providerprocess.Configure(command) }
func sanitizedEnvironment(environment, allowlist []string) []string {
	return providerprocess.Environment(environment, allowlist)
}
