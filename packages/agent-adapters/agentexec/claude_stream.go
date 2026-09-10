package agentexec

import "github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/providerstream"

func newClaudeJSONLObserver(emit func(string, any) error, suppressBrowser bool) *jsonlObserver {
	// Browser event policy belongs to the deep Plugin, not the shared parser.
	var suppressTool func(string) bool
	if suppressBrowser {
		suppressTool = func(name string) bool {
			return name == "mcp__openlinker_browser__browser_session"
		}
	}
	return providerstream.NewClaudeObserver(emit, suppressTool)
}
