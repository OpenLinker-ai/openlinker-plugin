package browserclientmode

import (
	"os"
	"strings"
	"testing"
)

// The generated packages are exercised against validateAgentRuntimePlugin by
// the crossrepo integration gate. This contract pins their image provenance.
func TestProviderImageBuildsAgentRuntimePluginFromSameSource(t *testing.T) {
	dockerfile, err := os.ReadFile("../../../Dockerfile.providers")
	if err != nil {
		t.Fatal(err)
	}
	text := string(dockerfile)
	for _, required := range []string{
		"COPY scripts/build-agent-runtime-packages.mjs",
		"COPY shared/skills/use-isolated-browser",
		"node scripts/build-agent-runtime-packages.mjs --out /out",
		"COPY --from=agent-runtime-plugin /out/codex-marketplace /opt/openlinker/agent-runtime-plugin/codex",
		"COPY --from=agent-runtime-plugin /out/claude-plugin/openlinker /opt/openlinker/agent-runtime-plugin/claude",
		"COPY shared/cli-lock.json",
		"node scripts/download-pinned-cli.mjs",
		"COPY --from=cli-artifact /out/openlinker /usr/local/bin/openlinker",
		"stat -c '%U:%G %a'",
		"root:root 555",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("Provider Dockerfile omitted %q", required)
		}
	}
	for _, forbidden := range []string{
		"agent-runtime-plugin.lock.json",
		"github.com/OpenLinker-ai/openlinker-plugin/releases/download",
		"./cmd/openlinker ",
		"../openlinker-cli",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("Provider Dockerfile reintroduced circular source/release dependency %q", forbidden)
		}
	}
}
