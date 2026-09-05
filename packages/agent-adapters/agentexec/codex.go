package agentexec

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	openlinker "github.com/OpenLinker-ai/openlinker-go"
)

type CodexProvider struct{ Config ProviderConfig }

func (provider CodexProvider) Run(ctx context.Context, run RunContext) (openlinker.RuntimeResult, error) {
	if run.Emit != nil {
		_ = run.Emit("run.message.delta", map[string]any{"text": "Codex is processing the task."})
	}
	config := provider.Config
	config = providerConfigForBrowserRun(config, run.Browser)
	bin := strings.TrimSpace(config.Bin)
	if bin == "" {
		bin = "codex"
	}
	workspace := strings.TrimSpace(config.Workspace)
	if workspace == "" {
		workspace, _ = os.Getwd()
	}
	sandbox := strings.TrimSpace(config.Sandbox)
	if sandbox == "" {
		sandbox = "read-only"
	}
	timeout := config.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	sessionKey := conversationSessionKey(run)
	sessionPath := sessionStorePath(config.SessionStore, "codex", workspace)
	sessionID := ""
	clientMode := providerSessionClientMode(config)
	clientModeGeneration := uint64(1)
	if config.SessionReuse && sessionKey != "" {
		unlock := lockSession("codex", workspace, sessionKey)
		defer unlock()
		var modeChanged bool
		sessionID, clientModeGeneration, modeChanged = loadSessionForClientMode(
			sessionPath,
			"codex",
			workspace,
			sessionKey,
			clientMode,
		)
		if modeChanged && run.Browser != nil && run.Browser.Rotate != nil {
			if rotateErr := run.Browser.Rotate(); rotateErr != nil {
				return openlinker.RuntimeResult{}, fmt.Errorf(
					"rotate Browser attachment after Codex client-mode change: %w",
					rotateErr,
				)
			}
		}
	}
	resumed := sessionID != ""
	recovered := false
	var stdoutText, stderrText string
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		args := codexArguments(config, workspace, sandbox, sessionID, sessionKey != "")
		stdoutText, stderrText, err = runCodexCommand(
			requestCtx,
			cancel,
			bin,
			args,
			workspace,
			buildCodexPrompt(
				run,
				sessionID == "",
				config.WebSearch,
				browserProfileEnabled(config),
			),
			config,
			run.Emit,
		)
		if err == nil {
			break
		}
		if errors.Is(requestCtx.Err(), context.DeadlineExceeded) {
			return openlinker.RuntimeResult{}, fmt.Errorf("Codex timed out after %s", timeout)
		}
		if sessionID != "" && attempt == 0 && missingProviderSession(stderrText) {
			if deleteErr := deleteSessionID(sessionPath, "codex", workspace, sessionKey); deleteErr != nil {
				return openlinker.RuntimeResult{}, fmt.Errorf("Codex session recovery failed: %w", deleteErr)
			}
			if run.Browser != nil && run.Browser.Rotate != nil {
				if rotateErr := run.Browser.Rotate(); rotateErr != nil {
					return openlinker.RuntimeResult{}, fmt.Errorf("rotate Browser attachment after Codex session recovery: %w", rotateErr)
				}
			}
			sessionID = ""
			recovered = true
			continue
		}
		return openlinker.RuntimeResult{}, fmt.Errorf("Codex failed: %w: %s", err, boundedText(stderrText, 500, "no diagnostic output"))
	}
	if config.SessionReuse && sessionKey != "" {
		if observed := extractCodexSessionID(stdoutText + "\n" + stderrText); observed != "" {
			if err := saveSessionForClientMode(
				sessionPath,
				"codex",
				workspace,
				sessionKey,
				observed,
				clientMode,
				clientModeGeneration,
			); err != nil {
				return openlinker.RuntimeResult{}, sessionPersistenceError("Codex", err)
			}
		}
	}
	summary := strings.TrimSpace(stdoutText)
	if sessionKey != "" {
		// Persistent Codex executions use JSONL so the thread ID can be saved.
		// Parse the final agent message from the same bounded stdout stream. This
		// also keeps the official container's Runtime and Provider UIDs isolated:
		// no cross-UID temporary output file is required.
		summary = extractCodexFinalMessage(stdoutText)
	}
	if summary == "" {
		return openlinker.RuntimeResult{}, errors.New("Codex completed without a final message")
	}
	result := map[string]any{
		"handled_by": "codex", "codex_sandbox": sandbox,
		"codex_model": modelLabel(config.Model), "summary": summary,
	}
	if config.SessionReuse && sessionKey != "" {
		result["codex_session_reuse"] = true
		result["codex_session_key_hash"] = sessionKeyHash("codex", workspace, sessionKey)
		result["codex_session_resumed"] = resumed
		result["codex_session_recovered"] = recovered
	}
	if browserProfileEnabled(config) {
		result["browser_client_mode_generation"] = clientModeGeneration
	}
	return openlinker.RuntimeResult{
		Status: "success", Output: result,
		Events: []openlinker.RuntimeEvent{{EventType: "run.message.delta", Payload: map[string]any{"text": summary}}},
	}, nil
}

func codexArguments(config ProviderConfig, workspace, sandbox, sessionID string, persistent bool) []string {
	args := []string{}
	args = append(args, codexBrowserMCPArguments(config)...)
	if value := strings.TrimSpace(config.CodexApproval); value != "" {
		args = append(args, "--ask-for-approval", value)
	}
	if config.WebSearch {
		args = append(args, "--search")
	} else {
		args = append(args, "-c", `web_search="disabled"`)
	}
	if value := strings.TrimSpace(config.CodexBaseURL); value != "" {
		// OpenAI-compatible routers commonly implement the HTTP Responses API
		// without the optional Responses WebSocket transport. Keep the built-in
		// OpenAI provider untouched and describe the router as a native custom
		// provider so Codex does not attempt an unsupported WebSocket upgrade.
		args = append(args,
			"-c", `model_provider="openlinker_proxy"`,
			"-c", `model_providers.openlinker_proxy.name="OpenLinker-compatible provider"`,
			"-c", fmt.Sprintf("model_providers.openlinker_proxy.base_url=%q", value),
			"-c", `model_providers.openlinker_proxy.env_key="CODEX_API_KEY"`,
			"-c", `model_providers.openlinker_proxy.wire_api="responses"`,
			"-c", `model_providers.openlinker_proxy.supports_websockets=false`,
		)
	}
	if sandbox == "danger-full-access" {
		// This mode is intended for an external isolation boundary such as the
		// official hardened Provider container. Keep model-spawned commands from
		// inheriting provider credentials even though the Codex process itself
		// needs them to call the configured model endpoint. Native Browser runs
		// also disable every built-in action surface so their explicit
		// non-interactive MCP bypass exposes only the bounded Browser Plugin.
		args = append(args,
			"--disable", "code_mode",
			"--disable", "code_mode_host",
			"-c", `shell_environment_policy.inherit="none"`,
			"-c", "shell_environment_policy.set="+codexCommandEnvironment(config.Env),
		)
		if nativeBrowserClientEnabled(config) {
			args = append(args,
				"--disable", "shell_tool",
				"--disable", "multi_agent",
				"-c", "tools.view_image=false",
			)
		}
	}
	if sessionID != "" {
		args = append(args, "-C", workspace, "exec", "resume", "--skip-git-repo-check")
		args = append(args, codexExecutionAccessArguments(config, sandbox)...)
		args = append(args, codexConfigIsolationArguments(config)...)
		args = append(args, "--json")
		if config.Model != "" {
			args = append(args, "--model", config.Model)
		}
		return append(args, sessionID, "-")
	}
	args = append(args, "exec", "--skip-git-repo-check")
	args = append(args, codexExecutionAccessArguments(config, sandbox)...)
	args = append(args, codexConfigIsolationArguments(config)...)
	args = append(args, "-C", workspace, "--color", "never")
	if persistent {
		args = append(args, "--json")
	} else {
		args = append(args, "--ephemeral")
	}
	if config.Model != "" {
		args = append(args, "--model", config.Model)
	}
	return append(args, "-")
}

func codexExecutionAccessArguments(config ProviderConfig, sandbox string) []string {
	if nativeBrowserClientEnabled(config) && sandbox == "danger-full-access" {
		// Codex exec currently cancels even pre-approved MCP calls unless its
		// explicit non-interactive bypass is active. This path is valid only for
		// the externally isolated native Browser Provider image, whose other
		// built-in action surfaces are disabled above.
		return []string{"--dangerously-bypass-approvals-and-sandbox"}
	}
	return []string{"--sandbox", sandbox}
}

func codexConfigIsolationArguments(config ProviderConfig) []string {
	if nativeBrowserClientEnabled(config) {
		return []string{"--ignore-rules"}
	}
	return []string{"--ignore-user-config", "--ignore-rules"}
}

func codexCommandEnvironment(environment []string) string {
	if environment == nil {
		environment = os.Environ()
	}
	values := make(map[string]string, len(environment))
	for _, item := range environment {
		key, value, ok := strings.Cut(item, "=")
		if ok {
			values[key] = value
		}
	}
	safeKeys := []string{
		"PATH", "Path", "PATHEXT",
		"HOME", "USERPROFILE", "HOMEDRIVE", "HOMEPATH",
		"SYSTEMROOT", "SystemRoot", "WINDIR", "COMSPEC",
		"TMPDIR", "TEMP", "TMP", "LANG",
		"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy",
		"NO_PROXY", "no_proxy", "SSL_CERT_FILE",
	}
	pairs := make([]string, 0, len(safeKeys))
	for _, key := range safeKeys {
		value, ok := values[key]
		if !ok {
			continue
		}
		encoded, _ := json.Marshal(value)
		pairs = append(pairs, key+"="+string(encoded))
	}
	return "{" + strings.Join(pairs, ",") + "}"
}

func runCodexCommand(
	ctx context.Context,
	cancel context.CancelFunc,
	bin string,
	args []string,
	workspace string,
	prompt string,
	config ProviderConfig,
	emit func(string, any) error,
) (string, string, error) {
	command := exec.CommandContext(ctx, bin, args...) // #nosec G204 -- operator-configured official provider binary, no shell.
	configureProviderProcess(command)
	command.Dir = workspace
	environment := config.Env
	if environment == nil {
		environment = os.Environ()
	}
	allowlist := append([]string{"CODEX_API_KEY", "CODEX_HOME", "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "ALL_PROXY", "CODEX_CA_CERTIFICATE", "SSL_CERT_FILE"}, config.EnvAllowlist...)
	command.Env = sanitizedEnvironment(environment, allowlist)
	command.Stdin = strings.NewReader(prompt)
	stdout := newLimitedOutputBuffer(cancel)
	stderr := newLimitedOutputBuffer(cancel)
	observer := newCodexJSONLObserver(emit, browserProfileEnabled(config))
	command.Stdout, command.Stderr = io.MultiWriter(stdout, observer), stderr
	err := command.Run()
	observer.Flush()
	if limitErr := outputLimitError("Codex", stdout, stderr); limitErr != nil {
		return stdout.String(), stderr.String(), limitErr
	}
	return stdout.String(), stderr.String(), err
}

func extractCodexSessionID(output string) string {
	scanner := bufio.NewScanner(strings.NewReader(output))
	scanner.Buffer(make([]byte, 1024), maxProviderOutputBytes)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "{") {
			continue
		}
		var event any
		if json.Unmarshal([]byte(line), &event) == nil {
			if id := findCodexSessionID(event); id != "" {
				return id
			}
		}
	}
	return ""
}

func extractCodexFinalMessage(output string) string {
	scanner := bufio.NewScanner(strings.NewReader(output))
	scanner.Buffer(make([]byte, 1024), maxProviderOutputBytes)
	final := ""
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "{") {
			continue
		}
		var event any
		if json.Unmarshal([]byte(line), &event) == nil {
			if message := findCodexFinalMessage(event); message != "" {
				final = message
			}
		}
	}
	return strings.TrimSpace(final)
}

func findCodexFinalMessage(value any) string {
	switch typed := value.(type) {
	case map[string]any:
		if eventType, _ := typed["type"].(string); eventType == "agent_message" {
			if message, _ := typed["text"].(string); strings.TrimSpace(message) != "" {
				return strings.TrimSpace(message)
			}
		}
		for _, nested := range typed {
			if message := findCodexFinalMessage(nested); message != "" {
				return message
			}
		}
	case []any:
		for _, nested := range typed {
			if message := findCodexFinalMessage(nested); message != "" {
				return message
			}
		}
	}
	return ""
}

func findCodexSessionID(value any) string {
	switch typed := value.(type) {
	case map[string]any:
		for _, key := range []string{"thread_id", "session_id", "conversation_id"} {
			if id, ok := typed[key].(string); ok && strings.TrimSpace(id) != "" {
				return strings.TrimSpace(id)
			}
		}
		for _, nested := range typed {
			if id := findCodexSessionID(nested); id != "" {
				return id
			}
		}
	case []any:
		for _, nested := range typed {
			if id := findCodexSessionID(nested); id != "" {
				return id
			}
		}
	}
	return ""
}

func modelLabel(model string) string {
	if strings.TrimSpace(model) == "" {
		return "default"
	}
	return strings.TrimSpace(model)
}
