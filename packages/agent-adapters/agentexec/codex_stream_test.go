package agentexec

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestCodexJSONLObserverProjectsOnlySafeToolProgress(t *testing.T) {
	var events []map[string]any
	observer := newCodexJSONLObserver(func(eventType string, payload any) error {
		if eventType != "run.status.changed" {
			t.Fatalf("event type = %q", eventType)
		}
		event, ok := payload.(map[string]any)
		if !ok {
			t.Fatalf("payload = %#v", payload)
		}
		events = append(events, event)
		return nil
	}, false)
	input := strings.Join([]string{
		`{"type":"thread.started","thread_id":"11111111-1111-4111-8111-111111111111"}`,
		`{"type":"item.started","item":{"id":"item-1","type":"command_execution","command":"curl https://secret.invalid/?token=do-not-emit","status":"in_progress"}}`,
		`{"type":"item.completed","item":{"id":"item-1","type":"command_execution","command":"curl https://secret.invalid/?token=do-not-emit","status":"completed"}}`,
		`{"type":"item.completed","item":{"id":"item-2","type":"mcp_tool_call","arguments":{"api_key":"do-not-emit"},"status":"failed"}}`,
		`{"type":"item.completed","item":{"id":"item-3","type":"agent_message","text":"do-not-stream-final"}}`,
	}, "\n") + "\n"
	split := len(input) / 2
	if _, err := observer.Write([]byte(input[:split])); err != nil {
		t.Fatal(err)
	}
	if _, err := observer.Write([]byte(input[split:])); err != nil {
		t.Fatal(err)
	}
	observer.Flush()

	if len(events) != 3 {
		t.Fatalf("events = %#v", events)
	}
	want := []map[string]any{
		{"status": "provider_tool_started", "provider": "codex", "phase": "started", "tool_kind": "command"},
		{"status": "provider_tool_completed", "provider": "codex", "phase": "completed", "tool_kind": "command"},
		{"status": "provider_tool_failed", "provider": "codex", "phase": "failed", "tool_kind": "mcp_tool"},
	}
	for index := range want {
		gotJSON, _ := json.Marshal(events[index])
		wantJSON, _ := json.Marshal(want[index])
		if string(gotJSON) != string(wantJSON) {
			t.Fatalf("event %d = %s, want %s", index, gotJSON, wantJSON)
		}
	}
	encoded, _ := json.Marshal(events)
	for _, forbidden := range []string{
		"11111111-1111-4111-8111-111111111111",
		"secret.invalid",
		"do-not-emit",
		"do-not-stream-final",
		"command_execution",
	} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("projected events leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestCodexJSONLObserverKeepsBrowserMCPProgressOutOfDurableEvents(t *testing.T) {
	project := func(actionCount int, browserProfile bool) []map[string]any {
		t.Helper()
		var events []map[string]any
		observer := newCodexJSONLObserver(func(eventType string, payload any) error {
			if eventType != "run.status.changed" {
				t.Fatalf("event type = %q", eventType)
			}
			events = append(events, payload.(map[string]any))
			return nil
		}, browserProfile)
		_, _ = observer.Write([]byte(
			`{"type":"item.started","item":{"type":"command_execution","status":"in_progress"}}` + "\n",
		))
		_, _ = observer.Write([]byte(
			`{"type":"item.completed","item":{"type":"command_execution","status":"completed"}}` + "\n",
		))
		for index := 0; index < actionCount; index++ {
			_, _ = observer.Write([]byte(
				`{"type":"item.started","item":{"type":"mcp_tool_call","server":"openlinker_browser","tool":"browser_session","status":"in_progress"}}` + "\n",
			))
			_, _ = observer.Write([]byte(
				`{"type":"item.completed","item":{"type":"mcp_tool_call","server_name":"openlinker_browser","tool_name":"browser_session","status":"completed"}}` + "\n",
			))
		}
		_, _ = observer.Write([]byte(
			`{"type":"item.completed","item":{"type":"mcp_tool_call","server":"other_server","tool":"other_tool","status":"completed"}}` + "\n",
		))
		observer.Flush()
		return events
	}

	one := project(1, true)
	fiveHundred := project(500, true)
	if len(one) != 3 || len(fiveHundred) != len(one) {
		t.Fatalf("Browser projected events: one=%d five-hundred=%d", len(one), len(fiveHundred))
	}
	for index := range one {
		got, _ := json.Marshal(fiveHundred[index])
		want, _ := json.Marshal(one[index])
		if string(got) != string(want) {
			t.Fatalf("Browser event %d = %s, want %s", index, got, want)
		}
	}
	standard := project(500, false)
	if len(standard) != 1003 {
		t.Fatalf("standard MCP progress changed: events=%d, want 1003", len(standard))
	}
}

func TestRunCodexCommandKeepsBrowserActionVolumeOutOfProjectedEvents(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	project := func(actionCount int, profile string) []map[string]any {
		t.Helper()
		var events []map[string]any
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		_, stderr, err := runCodexCommand(
			ctx,
			cancel,
			executable,
			[]string{"-test.run=TestCodexJSONLFixtureProcess"},
			t.TempDir(),
			"",
			ProviderConfig{
				ExecutionProfile: profile,
				Env: append(os.Environ(),
					"OPENLINKER_CODEX_JSONL_FIXTURE=1",
					"OPENLINKER_CODEX_JSONL_ACTIONS="+strconv.Itoa(actionCount),
				),
				EnvAllowlist: []string{
					"OPENLINKER_CODEX_JSONL_FIXTURE",
					"OPENLINKER_CODEX_JSONL_ACTIONS",
				},
			},
			func(eventType string, payload any) error {
				if eventType != "run.status.changed" {
					t.Fatalf("event type = %q", eventType)
				}
				events = append(events, payload.(map[string]any))
				return nil
			},
		)
		if err != nil {
			t.Fatalf("fixture Codex command failed: %v: %s", err, stderr)
		}
		return events
	}

	one := project(1, "browser")
	fiveHundred := project(500, "browser")
	if len(one) != 3 || len(fiveHundred) != len(one) {
		t.Fatalf("Browser command events: one=%d five-hundred=%d", len(one), len(fiveHundred))
	}
	standard := project(500, "standard")
	if len(standard) != 1003 {
		t.Fatalf("standard command MCP progress changed: events=%d, want 1003", len(standard))
	}
}

func TestCodexJSONLFixtureProcess(t *testing.T) {
	if os.Getenv("OPENLINKER_CODEX_JSONL_FIXTURE") != "1" {
		return
	}
	actionCount, err := strconv.Atoi(os.Getenv("OPENLINKER_CODEX_JSONL_ACTIONS"))
	if err != nil || actionCount < 0 {
		os.Exit(2)
	}
	fmt.Println(`{"type":"item.started","item":{"type":"command_execution","status":"in_progress"}}`)
	fmt.Println(`{"type":"item.completed","item":{"type":"command_execution","status":"completed"}}`)
	for index := 0; index < actionCount; index++ {
		fmt.Println(`{"type":"item.started","item":{"type":"mcp_tool_call","server":"openlinker_browser","tool":"browser_session","status":"in_progress"}}`)
		fmt.Println(`{"type":"item.completed","item":{"type":"mcp_tool_call","server":"openlinker_browser","tool":"browser_session","status":"completed"}}`)
	}
	fmt.Println(`{"type":"item.completed","item":{"type":"mcp_tool_call","server":"other_server","tool":"other_tool","status":"completed"}}`)
	os.Exit(0)
}

func TestBuildCodexPromptAdvertisesWebOnlyWhenEnabled(t *testing.T) {
	run := RunContext{
		RunID: "run-1",
		Input: map[string]any{"text": "latest news"},
	}
	enabled := buildCodexPrompt(run, true, true, false)
	for _, expected := range []string{
		"Live public-web access is enabled",
		"use web search or a permitted public HTTP tool before answering",
		"Do not claim that internet access is unavailable unless an actual web tool attempt fails",
		"Identify the public source hosts or URLs",
		"private, loopback, link-local, metadata, or credential-bearing destinations",
	} {
		if !strings.Contains(enabled, expected) {
			t.Fatalf("enabled prompt missing %q: %s", expected, enabled)
		}
	}
	disabled := buildCodexPrompt(run, true, false, false)
	if strings.Contains(disabled, "Live public-web access") ||
		strings.Contains(disabled, "use web search or a permitted public HTTP tool") {
		t.Fatalf("disabled prompt advertised web access: %s", disabled)
	}
}
