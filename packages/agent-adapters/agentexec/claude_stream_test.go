package agentexec

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestPluginClaudeBrowserFilterKeepsPairedProgressRedacted(t *testing.T) {
	project := func(browserActions int, browserProfile bool) []map[string]any {
		t.Helper()
		var events []map[string]any
		observer := newClaudeJSONLObserver(func(eventType string, payload any) error {
			if eventType != "run.status.changed" {
				t.Fatalf("unexpected event type %q", eventType)
			}
			events = append(events, payload.(map[string]any))
			return nil
		}, browserProfile)
		feed := func(raw string) {
			t.Helper()
			// Use fragmented writes and an unterminated final line: both the
			// shared parser and Plugin's filter must execute on this real path.
			split := len(raw) / 2
			_, _ = observer.Write([]byte(raw[:split]))
			_, _ = observer.Write([]byte(raw[split:] + "\n"))
		}
		feed(`{"type":"stream_event","event":{"type":"message_start"}}`)
		feed(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"cmd","name":"Bash","input":{"command":"secret-command"}}]}}`)
		for index := 0; index < browserActions; index++ {
			feed(fmt.Sprintf(`{"type":"stream_event","event":{"type":"content_block_start","content_block":{"type":"tool_use","id":"browser-%d","name":"mcp__openlinker_browser__browser_session","input":{"url":"https://secret.invalid/?token=must-not-emit"}}}}`, index))
			// Claude repeats tool_use in its assembled assistant message.
			feed(fmt.Sprintf(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"browser-%d","name":"mcp__openlinker_browser__browser_session"}]}}`, index))
			feed(fmt.Sprintf(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"browser-%d","content":"private-browser-output","is_error":true}]}}`, index))
		}
		feed(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"cmd","content":"private-command-output"}]}}`)
		feed(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"other","name":"mcp__other__browser_session","input":{"password":"must-not-emit"}}]}}`)
		feed(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"other","is_error":true}]}}`)
		_, _ = observer.Write([]byte(`{"type":"system","subtype":"api_retry","error":"must-not-emit"}`))
		observer.Flush()
		return events
	}
	want := []map[string]any{
		{"provider": "claude", "status": "provider_processing", "phase": "started"},
		{"provider": "claude", "status": "provider_tool_started", "phase": "started", "tool_kind": "command"},
		{"provider": "claude", "status": "provider_tool_completed", "phase": "completed", "tool_kind": "command"},
		{"provider": "claude", "status": "provider_tool_started", "phase": "started", "tool_kind": "mcp_tool"},
		{"provider": "claude", "status": "provider_tool_failed", "phase": "failed", "tool_kind": "mcp_tool"},
		{"provider": "claude", "status": "provider_retrying", "phase": "retrying"},
	}
	for _, count := range []int{1, 500} {
		got := project(count, true)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("Browser durable progress changed for %d actions: %#v", count, got)
		}
	}
	standard := project(500, false)
	if len(standard) != len(want)+1000 {
		t.Fatalf("positive control: standard parser omitted tool progress (%d events)", len(standard))
	}
	raw, err := json.Marshal(standard)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"secret-command", "private-command-output", "private-browser-output", "secret.invalid", "must-not-emit", "browser-", "mcp__"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("shared progress projection leaked %q", secret)
		}
	}
}
