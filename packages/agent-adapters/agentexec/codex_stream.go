package agentexec

import (
	"strings"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/providerstream"
)

type jsonlObserver = providerstream.Observer

func newCodexJSONLObserver(emit func(string, any) error, suppressMCPProgress bool) *jsonlObserver {
	// The shared JSONL mechanism projects safe fields; only Plugin decides which
	// Browser progress is already represented by its authoritative lifecycle.
	var suppressItem func(map[string]any) bool
	if suppressMCPProgress {
		suppressItem = func(item map[string]any) bool {
			kind, _ := item["type"].(string)
			return strings.EqualFold(strings.TrimSpace(kind), "mcp_tool_call") &&
				firstCodexString(item, "server", "server_name") == "openlinker_browser" &&
				firstCodexString(item, "tool", "tool_name") == "browser_session"
		}
	}
	return providerstream.NewCodexObserver(emit, suppressItem)
}

func firstCodexString(item map[string]any, keys ...string) string {
	for _, key := range keys {
		value, _ := item[key].(string)
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
