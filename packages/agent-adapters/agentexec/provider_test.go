package agentexec

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	openlinker "github.com/OpenLinker-ai/openlinker-go"
)

func TestCodexProviderStreamsSafeProgressBeforeExit(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "codex-fake")
	logPath := filepath.Join(dir, "provider")
	scriptBody := `#!/bin/sh
set -eu
printf '%s\n' "$*" > "$TEST_LOG.args"
cat > "$TEST_LOG.prompt"
printf '%s\n' '{"type":"thread.started","thread_id":"11111111-1111-4111-8111-111111111111"}'
printf '%s\n' '{"type":"item.started","item":{"id":"item-1","type":"web_search","query":"private query must not be emitted","status":"in_progress"}}'
while [ ! -f "$TEST_LOG.release" ]; do
  sleep 0.05
done
printf '%s\n' '{"type":"item.completed","item":{"id":"item-1","type":"web_search","query":"private query must not be emitted","status":"completed"}}'
printf '%s\n' '{"type":"item.completed","item":{"id":"item-2","type":"agent_message","text":"provider answer"}}'
`
	if err := os.WriteFile(script, []byte(scriptBody), 0o700); err != nil {
		t.Fatal(err)
	}
	provider := CodexProvider{Config: ProviderConfig{
		Provider: "codex", Bin: script, Workspace: dir, Sandbox: "read-only",
		WebSearch: true, SessionReuse: true, SessionStore: filepath.Join(dir, "sessions.json"),
		Timeout: 15 * time.Second, Env: append(os.Environ(), "TEST_LOG="+logPath), EnvAllowlist: []string{"TEST_LOG"},
	}}
	progress := make(chan map[string]any, 8)
	run := RunContext{
		RunID: "run-1", Input: map[string]any{"text": "latest news"},
		Conversation: &ConversationContext{
			ID: "conversation-1", SessionKey: "conversation-1", CurrentRunID: "run-1", Source: "core",
		},
		Emit: func(eventType string, payload any) error {
			if eventType == "run.status.changed" {
				progress <- payload.(map[string]any)
			}
			return nil
		},
	}
	type providerResult struct {
		result openlinker.RuntimeResult
		err    error
	}
	completed := make(chan providerResult, 1)
	go func() {
		result, err := provider.Run(context.Background(), run)
		completed <- providerResult{result: result, err: err}
	}()

	select {
	case event := <-progress:
		if event["phase"] != "started" || event["tool_kind"] != "web_search" {
			t.Fatalf("first progress = %#v", event)
		}
	case result := <-completed:
		t.Fatalf("provider completed before streaming progress: result=%#v err=%v", result.result, result.err)
	case <-time.After(10 * time.Second):
		t.Fatal("provider did not stream progress before exit")
	}
	if err := os.WriteFile(logPath+".release", []byte("continue"), 0o600); err != nil {
		t.Fatal(err)
	}

	result := <-completed
	if result.err != nil {
		t.Fatal(result.err)
	}
	if result.result.Output.(map[string]any)["summary"] != "provider answer" {
		t.Fatalf("result = %#v", result.result)
	}
	finished := <-progress
	if finished["phase"] != "completed" || finished["tool_kind"] != "web_search" {
		t.Fatalf("completed progress = %#v", finished)
	}
	args, _ := os.ReadFile(logPath + ".args")
	if !strings.Contains(string(args), "--search") {
		t.Fatalf("Codex args did not enable web search: %s", args)
	}
	prompt, _ := os.ReadFile(logPath + ".prompt")
	if !strings.Contains(string(prompt), "Live public-web access is enabled") {
		t.Fatalf("Codex prompt did not describe web capability: %s", prompt)
	}
	encoded, _ := json.Marshal([]map[string]any{
		{"started": "captured above"},
		finished,
	})
	for _, forbidden := range []string{"private query", "11111111-1111-4111-8111-111111111111"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("progress leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestCodexExternalSandboxRestrictsSpawnedCommandEnvironment(t *testing.T) {
	args := codexArguments(ProviderConfig{
		CodexApproval: "never",
		WebSearch:     true,
		Env: []string{
			"PATH=/usr/bin",
			"HOME=/provider",
			"HTTPS_PROXY=http://egress:3128",
			"CODEX_API_KEY=must-not-be-forwarded",
			"OPENLINKER_AGENT_TOKEN=must-not-be-forwarded",
		},
	}, "/workspace", "danger-full-access", "", true)
	joined := strings.Join(args, " ")
	for _, expected := range []string{
		"--sandbox danger-full-access",
		"--disable code_mode",
		"--disable code_mode_host",
		`shell_environment_policy.inherit="none"`,
		`shell_environment_policy.set={PATH="/usr/bin",HOME="/provider",HTTPS_PROXY="http://egress:3128"}`,
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("Codex external sandbox args do not include %s: %s", expected, joined)
		}
	}
	for _, forbidden := range []string{"CODEX_API_KEY", "OPENLINKER_AGENT_TOKEN", "TOKEN", "SECRET"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("Codex spawned-command environment allowlist contains %s: %s", forbidden, joined)
		}
	}
}

func TestCodexProviderReusesTrustedConversationSession(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "codex-fake")
	logPath := filepath.Join(dir, "args.log")
	scriptBody := `#!/bin/sh
set -eu
mode="new"
for item in "$@"; do
  if [ "$item" = "resume" ]; then mode="resume"; fi
done
printf '%s\n' "$*" >> "$TEST_LOG"
printf '%s\n' "${ALL_PROXY-}" > "$TEST_LOG.proxy"
cat > "$TEST_LOG.$mode.prompt"
printf '%s\n' '{"type":"thread.started","thread_id":"11111111-1111-4111-8111-111111111111"}'
printf '%s\n' '{"type":"item.completed","item":{"id":"item-1","type":"agent_message","text":"provider answer"}}'
`
	if err := os.WriteFile(script, []byte(scriptBody), 0o700); err != nil {
		t.Fatal(err)
	}
	config := ProviderConfig{
		Provider: "codex", Bin: script, Workspace: dir, Sandbox: "read-only", CodexApproval: "never",
		CodexBaseURL: "https://router.example/v1",
		Timeout:      5e9, SessionReuse: true, SessionStore: filepath.Join(dir, "sessions.json"),
		Env: append(os.Environ(), "TEST_LOG="+logPath, "ALL_PROXY=socks5://proxy.example:1080"), EnvAllowlist: []string{"TEST_LOG"},
	}
	provider := CodexProvider{Config: config}
	run := RunContext{
		RunID: "run-1", Input: map[string]any{"text": "current request"}, Metadata: map[string]any{},
		Conversation: &ConversationContext{
			ID: "conversation-1", SessionKey: "conversation-1", RootContextID: "conversation-1",
			CurrentRunID: "run-1", Source: "core",
			HistoryBeforeCurrent: []ConversationMessage{{Role: "user", Content: "earlier request"}},
		},
	}
	first, err := provider.Run(context.Background(), run)
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != "success" {
		t.Fatalf("first result = %#v", first)
	}
	if first.Output.(map[string]any)["summary"] != "provider answer" {
		t.Fatalf("first output = %#v", first.Output)
	}
	run.RunID = "run-2"
	run.Conversation.CurrentRunID = "run-2"
	second, err := provider.Run(context.Background(), run)
	if err != nil {
		t.Fatal(err)
	}
	output := second.Output.(map[string]any)
	if output["codex_session_resumed"] != true {
		t.Fatalf("second output = %#v", output)
	}
	args, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(args), "exec resume") || !strings.Contains(string(args), "--ignore-user-config") || !strings.Contains(string(args), "--ignore-rules") {
		t.Fatalf("Codex args = %s", args)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(args)), "\n") {
		if !strings.Contains(line, "--skip-git-repo-check") {
			t.Fatalf("Codex args do not support a non-Git workspace: %s", line)
		}
		for _, expected := range []string{
			`model_provider="openlinker_proxy"`,
			`model_providers.openlinker_proxy.name="OpenLinker-compatible provider"`,
			`model_providers.openlinker_proxy.base_url="https://router.example/v1"`,
			`model_providers.openlinker_proxy.env_key="CODEX_API_KEY"`,
			`model_providers.openlinker_proxy.wire_api="responses"`,
			`model_providers.openlinker_proxy.supports_websockets=false`,
		} {
			if !strings.Contains(line, expected) {
				t.Fatalf("Codex args do not include %s: %s", expected, line)
			}
		}
		if strings.Contains(line, "openai_base_url") {
			t.Fatalf("Codex args incorrectly reused the WebSocket-capable built-in provider: %s", line)
		}
	}
	if strings.Contains(string(args), "--output-last-message") {
		t.Fatalf("Codex args reintroduced a cross-UID output file: %s", args)
	}
	proxy, err := os.ReadFile(logPath + ".proxy")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(proxy)) != "socks5://proxy.example:1080" {
		t.Fatalf("Codex ALL_PROXY = %q", proxy)
	}
	newPrompt, _ := os.ReadFile(logPath + ".new.prompt")
	resumePrompt, _ := os.ReadFile(logPath + ".resume.prompt")
	if !strings.Contains(string(newPrompt), "earlier request") {
		t.Fatalf("new prompt did not include trusted fallback history: %s", newPrompt)
	}
	if strings.Contains(string(resumePrompt), "earlier request") {
		t.Fatalf("resume prompt duplicated prior history: %s", resumePrompt)
	}
	info, err := os.Stat(config.SessionStore)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("session mode = %o", info.Mode().Perm())
	}
}

func TestClaudeProviderUsesSafeModeAndResumes(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "claude-fake")
	logPath := filepath.Join(dir, "args.log")
	scriptBody := `#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$TEST_LOG"
cat >/dev/null
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"result":"claude answer","session_id":"22222222-2222-4222-8222-222222222222"}'
`
	if err := os.WriteFile(script, []byte(scriptBody), 0o700); err != nil {
		t.Fatal(err)
	}
	provider := ClaudeProvider{Config: ProviderConfig{
		Provider: "claude", Bin: script, Workspace: dir, Permission: "dontAsk", Timeout: 5e9,
		SessionReuse: true, SessionStore: filepath.Join(dir, "sessions.json"),
		Env: append(os.Environ(), "TEST_LOG="+logPath), EnvAllowlist: []string{"TEST_LOG"},
	}}
	run := RunContext{RunID: "run-1", Input: map[string]any{"text": "hello"}, Metadata: map[string]any{}, Conversation: &ConversationContext{
		ID: "conversation-2", SessionKey: "conversation-2", CurrentRunID: "run-1", Source: "core",
	}}
	if _, err := provider.Run(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	run.RunID, run.Conversation.CurrentRunID = "run-2", "run-2"
	if _, err := provider.Run(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	args, _ := os.ReadFile(logPath)
	text := string(args)
	if !strings.Contains(text, "--safe-mode") || !strings.Contains(text, "--no-chrome") || !strings.Contains(text, "--resume 22222222-2222-4222-8222-222222222222") {
		t.Fatalf("Claude args = %s", text)
	}
}

type captureProvider struct{ run RunContext }

func (provider *captureProvider) Run(_ context.Context, run RunContext) (openlinker.RuntimeResult, error) {
	provider.run = run
	return openlinker.RuntimeResult{Status: "success", Output: map[string]any{"ok": true}}, nil
}

func TestHandlerTrustsOnlyCoreConversation(t *testing.T) {
	provider := &captureProvider{}
	handler := Handler{Provider: provider}
	assignment := openlinker.RuntimeContext{
		RunID: "run-1", AgentID: "agent-1", Input: map[string]any{"text": "hello"},
		Authority: &openlinker.RuntimeAuthorityContext{
			PrincipalScopeID: "principal-1",
		},
		Metadata: openlinker.RuntimeJSONMap{"conversation": map[string]any{
			"id": "spoofed", "session_key": "spoofed", "current_run_id": "run-1", "source": "caller",
		}},
	}
	if _, err := handler.Handle(context.Background(), assignment); err != nil {
		t.Fatal(err)
	}
	if provider.run.Conversation != nil {
		t.Fatalf("caller conversation was trusted: %#v", provider.run.Conversation)
	}
	if provider.run.Authority != assignment.Authority {
		t.Fatalf("Runtime authority was not propagated: %#v", provider.run.Authority)
	}
	if _, exists := provider.run.Metadata["conversation"]; exists {
		t.Fatalf("conversation control metadata leaked into provider task metadata: %#v", provider.run.Metadata)
	}
	assignment.Metadata["conversation"] = map[string]any{
		"id": "trusted", "session_key": "trusted", "current_run_id": "run-1", "source": "core",
		"history_before_current": []any{
			map[string]any{
				"run_id": "run-previous", "role": "user", "content": "first question",
			},
			map[string]any{
				"run_id": "run-previous", "role": "agent", "content": "first answer",
			},
		},
	}
	if _, err := handler.Handle(context.Background(), assignment); err != nil {
		t.Fatal(err)
	}
	if provider.run.Conversation == nil ||
		provider.run.Conversation.SessionKey != "trusted" ||
		len(provider.run.Conversation.HistoryBeforeCurrent) != 2 {
		t.Fatalf("Core conversation missing: %#v", provider.run.Conversation)
	}
	for _, expected := range []string{"first question", "first answer"} {
		if !strings.Contains(buildPrompt("Codex", provider.run, true, false), expected) {
			t.Fatalf("initial provider prompt missing %q", expected)
		}
	}
	if _, exists := provider.run.Metadata["conversation"]; exists {
		t.Fatalf("trusted conversation was duplicated into provider task metadata: %#v", provider.run.Metadata)
	}

	assignment.Metadata["conversation"] = map[string]any{
		"id": "malformed", "session_key": "trusted", "source": "core",
	}
	if _, err := handler.Handle(context.Background(), assignment); err != nil {
		t.Fatal(err)
	}
	if provider.run.Conversation != nil {
		t.Fatalf("malformed Core conversation was trusted: %#v", provider.run.Conversation)
	}
	if _, exists := provider.run.Metadata["conversation"]; exists {
		t.Fatalf("malformed conversation leaked into provider task metadata: %#v", provider.run.Metadata)
	}
}

func TestSessionStoreRejectsInsecureExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	if err := os.WriteFile(path, []byte(`{"sessions":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := saveSessionID(path, "codex", t.TempDir(), "conversation", "session"); err == nil {
		t.Fatal("expected insecure existing session store to be rejected")
	}
}
