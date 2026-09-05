package agentexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	openlinker "github.com/OpenLinker-ai/openlinker-go"
)

type ClaudeProvider struct{ Config ProviderConfig }

type claudeResponse struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	IsError   bool   `json:"is_error"`
	Result    string `json:"result"`
	SessionID string `json:"session_id"`
}

func (provider ClaudeProvider) Run(ctx context.Context, run RunContext) (openlinker.RuntimeResult, error) {
	if run.Emit != nil {
		_ = run.Emit("run.message.delta", map[string]any{"text": "Claude Code is processing the task."})
	}
	config := provider.Config
	config = providerConfigForBrowserRun(config, run.Browser)
	bin := strings.TrimSpace(config.Bin)
	if bin == "" {
		bin = "claude"
	}
	workspace := strings.TrimSpace(config.Workspace)
	if workspace == "" {
		workspace, _ = os.Getwd()
	}
	permission := strings.TrimSpace(config.Permission)
	if permission == "" {
		permission = "dontAsk"
	}
	timeout := config.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	sessionKey := conversationSessionKey(run)
	sessionPath := sessionStorePath(config.SessionStore, "claude", workspace)
	sessionID := ""
	clientMode := providerSessionClientMode(config)
	clientModeGeneration := uint64(1)
	if config.SessionReuse && sessionKey != "" {
		unlock := lockSession("claude", workspace, sessionKey)
		defer unlock()
		var modeChanged bool
		sessionID, clientModeGeneration, modeChanged = loadSessionForClientMode(
			sessionPath,
			"claude",
			workspace,
			sessionKey,
			clientMode,
		)
		if modeChanged && run.Browser != nil && run.Browser.Rotate != nil {
			if rotateErr := run.Browser.Rotate(); rotateErr != nil {
				return openlinker.RuntimeResult{}, fmt.Errorf(
					"rotate Browser attachment after Claude client-mode change: %w",
					rotateErr,
				)
			}
		}
	}
	resumed := sessionID != ""
	recovered := false
	var response claudeResponse
	for attempt := 0; attempt < 2; attempt++ {
		args := claudeArguments(config, permission, sessionID)
		command := exec.CommandContext(requestCtx, bin, args...) // #nosec G204 -- operator-configured official provider binary, no shell.
		configureProviderProcess(command)
		command.Dir = workspace
		environment := config.Env
		if environment == nil {
			environment = os.Environ()
		}
		allowlist := append([]string{"ANTHROPIC_API_KEY", "CLAUDE_CONFIG_DIR", "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "NODE_EXTRA_CA_CERTS", "SSL_CERT_FILE"}, config.EnvAllowlist...)
		command.Env = sanitizedEnvironment(environment, allowlist)
		command.Stdin = strings.NewReader(
			buildPrompt(
				"Claude Code",
				run,
				sessionID == "",
				browserProfileEnabled(config),
			),
		)
		stdout := newLimitedOutputBuffer(cancel)
		stderr := newLimitedOutputBuffer(cancel)
		command.Stdout, command.Stderr = stdout, stderr
		err := command.Run()
		if limitErr := outputLimitError("Claude", stdout, stderr); limitErr != nil {
			return openlinker.RuntimeResult{}, limitErr
		}
		if err != nil {
			if errors.Is(requestCtx.Err(), context.DeadlineExceeded) {
				return openlinker.RuntimeResult{}, fmt.Errorf("Claude timed out after %s", timeout)
			}
			if sessionID != "" && attempt == 0 && missingProviderSession(stderr.String()) {
				if deleteErr := deleteSessionID(sessionPath, "claude", workspace, sessionKey); deleteErr != nil {
					return openlinker.RuntimeResult{}, fmt.Errorf("Claude session recovery failed: %w", deleteErr)
				}
				if run.Browser != nil && run.Browser.Rotate != nil {
					if rotateErr := run.Browser.Rotate(); rotateErr != nil {
						return openlinker.RuntimeResult{}, fmt.Errorf("rotate Browser attachment after Claude session recovery: %w", rotateErr)
					}
				}
				sessionID = ""
				recovered = true
				continue
			}
			return openlinker.RuntimeResult{}, fmt.Errorf("Claude failed: %w: %s", err, boundedText(stderr.String(), 500, "no diagnostic output"))
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(stdout.String())), &response); err != nil {
			return openlinker.RuntimeResult{}, errors.New("Claude returned invalid JSON output")
		}
		break
	}
	if response.IsError || strings.Contains(strings.ToLower(response.Subtype), "error") {
		return openlinker.RuntimeResult{}, errors.New("Claude returned an unsuccessful result")
	}
	summary := strings.TrimSpace(response.Result)
	if summary == "" {
		return openlinker.RuntimeResult{}, errors.New("Claude completed without a final result")
	}
	if config.SessionReuse && sessionKey != "" && strings.TrimSpace(response.SessionID) != "" {
		if err := saveSessionForClientMode(
			sessionPath,
			"claude",
			workspace,
			sessionKey,
			response.SessionID,
			clientMode,
			clientModeGeneration,
		); err != nil {
			return openlinker.RuntimeResult{}, sessionPersistenceError("Claude", err)
		}
	}
	result := map[string]any{
		"handled_by": "claude", "claude_permission": permission,
		"claude_model": modelLabel(config.Model), "summary": summary,
	}
	if config.SessionReuse && sessionKey != "" {
		result["claude_session_reuse"] = true
		result["claude_session_key_hash"] = sessionKeyHash("claude", workspace, sessionKey)
		result["claude_session_resumed"] = resumed
		result["claude_session_recovered"] = recovered
	}
	if browserProfileEnabled(config) {
		result["browser_client_mode_generation"] = clientModeGeneration
	}
	return openlinker.RuntimeResult{
		Status: "success", Output: result,
		Events: []openlinker.RuntimeEvent{{EventType: "run.message.delta", Payload: map[string]any{"text": summary}}},
	}, nil
}

func claudeArguments(config ProviderConfig, permission, sessionID string) []string {
	args := []string{"--safe-mode", "--no-chrome", "--disable-slash-commands"}
	if directMCPBrowserClientEnabled(config) {
		args = []string{"--bare", "--no-chrome", "--disable-slash-commands", "--strict-mcp-config", "--mcp-config", claudeBrowserMCPConfig(config)}
	} else if nativeBrowserClientEnabled(config) {
		args = []string{
			"--bare",
			"--no-chrome",
			"--plugin-dir",
			config.BrowserNativePlugin,
		}
	}
	args = append(args, "-p", "--output-format", "json", "--permission-mode", permission)
	if config.Model != "" {
		args = append(args, "--model", config.Model)
	}
	allowed := append([]string(nil), config.AllowedTools...)
	if browserProfileEnabled(config) {
		allowed = appendUniqueString(allowed, "mcp__openlinker_browser__browser_session")
	}
	if len(allowed) > 0 {
		args = append(args, "--allowedTools", strings.Join(allowed, ","))
	}
	if !config.WebSearch {
		args = append(args, "--disallowedTools", "WebSearch,WebFetch")
	}
	if sessionID != "" {
		args = append(args, "--resume", sessionID)
	}
	return args
}
