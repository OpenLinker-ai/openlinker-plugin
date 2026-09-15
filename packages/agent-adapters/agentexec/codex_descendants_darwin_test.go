package agentexec

import (
	"testing"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/providertest"
)

// Plugin's own Codex provider uses the shared native launcher, so it must also
// reap observed descendants that left the original process group.
func TestCodexRPCCancellationStopsDetachedDescendants(t *testing.T) {
	providertest.CodexRPCCancellationStopsDetachedDescendants(t, runCodexFixture)
}
