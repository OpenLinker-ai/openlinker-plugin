package agentexec

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"
)

type jsonlObserver struct {
	mu       sync.Mutex
	pending  []byte
	emit     func(string, any) error
	progress func(map[string]any) []map[string]any
}

func newCodexJSONLObserver(
	emit func(string, any) error,
	suppressMCPProgress bool,
) *jsonlObserver {
	return &jsonlObserver{emit: emit, progress: func(event map[string]any) []map[string]any {
		if suppressMCPProgress && isOpenLinkerBrowserMCPEvent(event) {
			return nil
		}
		if payload, ok := codexProgressPayload(event); ok {
			return []map[string]any{payload}
		}
		return nil
	}}
}

func (observer *jsonlObserver) Write(value []byte) (int, error) {
	if observer == nil || len(value) == 0 {
		return len(value), nil
	}
	observer.mu.Lock()
	defer observer.mu.Unlock()
	observer.pending = append(observer.pending, value...)
	observer.drainLines(false)
	return len(value), nil
}

func (observer *jsonlObserver) Flush() {
	if observer == nil {
		return
	}
	observer.mu.Lock()
	defer observer.mu.Unlock()
	observer.drainLines(true)
}

func (observer *jsonlObserver) drainLines(flush bool) {
	for {
		index := bytes.IndexByte(observer.pending, '\n')
		if index < 0 {
			if flush && len(observer.pending) > 0 {
				observer.observeLine(observer.pending)
				observer.pending = nil
			}
			return
		}
		line := observer.pending[:index]
		observer.pending = observer.pending[index+1:]
		observer.observeLine(line)
	}
}

func (observer *jsonlObserver) observeLine(line []byte) {
	if observer.emit == nil {
		return
	}
	line = bytes.TrimSpace(line)
	if len(line) == 0 || line[0] != '{' {
		return
	}
	var event map[string]any
	if json.Unmarshal(line, &event) != nil {
		return
	}
	// Progress is best-effort and only contains normalized, non-sensitive fields.
	for _, payload := range observer.progress(event) {
		_ = observer.emit("run.status.changed", payload)
	}
}

func isOpenLinkerBrowserMCPEvent(event map[string]any) bool {
	item, ok := event["item"].(map[string]any)
	if !ok || normalizedCodexToolKind(item["type"]) != "mcp_tool" {
		return false
	}
	server := firstCodexString(item, "server", "server_name")
	tool := firstCodexString(item, "tool", "tool_name")
	return server == "openlinker_browser" && tool == "browser_session"
}

func firstCodexString(item map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(stringValue(item[key])); value != "" {
			return value
		}
	}
	return ""
}

func codexProgressPayload(event map[string]any) (map[string]any, bool) {
	eventType, _ := event["type"].(string)
	if eventType != "item.started" && eventType != "item.completed" {
		return nil, false
	}
	item, ok := event["item"].(map[string]any)
	if !ok {
		return nil, false
	}
	toolKind := normalizedCodexToolKind(item["type"])
	if toolKind == "" {
		return nil, false
	}
	phase := "started"
	if eventType == "item.completed" {
		phase = "completed"
		if status, _ := item["status"].(string); strings.EqualFold(strings.TrimSpace(status), "failed") {
			phase = "failed"
		}
	}
	return map[string]any{
		"status":    "provider_tool_" + phase,
		"provider":  "codex",
		"phase":     phase,
		"tool_kind": toolKind,
	}, true
}

func normalizedCodexToolKind(value any) string {
	switch strings.ToLower(strings.TrimSpace(stringValue(value))) {
	case "web_search":
		return "web_search"
	case "command_execution":
		return "command"
	case "mcp_tool_call":
		return "mcp_tool"
	default:
		return ""
	}
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}
