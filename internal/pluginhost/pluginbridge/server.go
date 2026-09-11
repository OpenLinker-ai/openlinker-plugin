package pluginbridge

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	openlinker "github.com/OpenLinker-ai/openlinker-go"
	"github.com/OpenLinker-ai/openlinker-plugin/internal/pluginhost/buildinfo"
	"github.com/OpenLinker-ai/openlinker-plugin/internal/pluginhost/shared"
	"github.com/OpenLinker-ai/openlinker-plugin/packages/agent-adapters/agent"
)

const protocolVersion = "2025-06-18"
const bridgeShutdownTimeout = 15 * time.Second
const agentStartupTimeout = 30 * time.Second

// AgentService is the application lifecycle consumed by the MCP adapter.
// Implementations live in the Plugin module and reuse the SDK Runtime Worker.
type AgentService interface {
	Status() agent.Status
	Enable(context.Context, string) error
	Disable(context.Context) error
}

type Server struct {
	Host    string
	IO      shared.IO
	Options *shared.GlobalOptions
	Agent   AgentService
	mu      sync.Mutex
	agentMu sync.Mutex
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type toolCallParams struct {
	Name      string          `json:"name"`
	Arguments map[string]any  `json:"arguments,omitempty"`
	Meta      json.RawMessage `json:"_meta,omitempty"`
}

type toolResult struct {
	Content           []contentBlock `json:"content"`
	StructuredContent any            `json:"structuredContent,omitempty"`
	IsError           bool           `json:"isError,omitempty"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type scanResult struct {
	line []byte
	err  error
}

type completedRequest struct {
	key      string
	response rpcResponse
}

type cancelledParams struct {
	RequestID json.RawMessage `json:"requestId"`
}

type toolDefinition struct {
	Name        string         `json:"name"`
	Title       string         `json:"title,omitempty"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Annotations map[string]any `json:"annotations,omitempty"`
}

func (server *Server) Serve(ctx context.Context, input io.Reader, output io.Writer) error {
	if server.Options == nil {
		options := shared.DefaultGlobalOptions(server.IO.Getenv)
		server.Options = &options
	}
	if server.Agent == nil {
		server.Agent = agent.NewService(server.IO.Getenv, nil, buildinfo.Version)
	}
	if agent.ModeEnabled(server.IO.Getenv) {
		go func() {
			server.agentMu.Lock()
			defer server.agentMu.Unlock()
			if server.Agent.Status().State == "ready" {
				return
			}
			startupCtx, cancel := context.WithTimeout(ctx, agentStartupTimeout)
			defer cancel()
			if err := server.Agent.Enable(startupCtx, server.Host); err != nil && server.IO.Stderr != nil {
				_, _ = fmt.Fprintf(server.IO.Stderr, "openlinker: Agent mode startup failed: %v\n", err)
			}
		}()
	}
	serveCtx, stop := context.WithCancel(ctx)
	defer stop()
	abandon := make(chan struct{})
	defer close(abandon)
	messages := scanMessages(serveCtx, input)
	responses := make(chan completedRequest, 64)
	cancels := map[string]context.CancelFunc{}
	pending := 0
	inputOpen := true
	encoder := json.NewEncoder(output)
	for inputOpen || pending > 0 {
		select {
		case <-ctx.Done():
			stop()
			cancelRequests(cancels)
			return server.shutdown()
		case scanned, ok := <-messages:
			if !ok {
				inputOpen = false
				messages = nil
				stop()
				cancelRequests(cancels)
				continue
			}
			if scanned.err != nil {
				stop()
				cancelRequests(cancels)
				_ = server.shutdown()
				return scanned.err
			}
			line := bytes.TrimSpace(scanned.line)
			if len(line) == 0 {
				continue
			}
			var request rpcRequest
			if err := json.Unmarshal(line, &request); err != nil {
				if err := encoder.Encode(rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{Code: -32700, Message: "Parse error"}}); err != nil {
					return err
				}
				continue
			}
			if len(request.ID) == 0 {
				if request.Method == "notifications/cancelled" {
					cancelRequest(request.Params, cancels)
				}
				continue
			}
			key := requestKey(request.ID)
			if _, exists := cancels[key]; exists {
				if err := encoder.Encode(rpcResponse{JSONRPC: "2.0", ID: request.ID, Error: &rpcError{Code: -32600, Message: "Duplicate request id"}}); err != nil {
					return err
				}
				continue
			}
			requestCtx, cancel := context.WithCancel(serveCtx)
			cancels[key] = cancel
			pending++
			go func() {
				completed := completedRequest{key: key, response: server.handle(requestCtx, request)}
				select {
				case responses <- completed:
				case <-abandon:
				}
			}()
		case completed := <-responses:
			if cancel, ok := cancels[completed.key]; ok {
				cancel()
				delete(cancels, completed.key)
				pending--
			}
			server.mu.Lock()
			err := encoder.Encode(completed.response)
			server.mu.Unlock()
			if err != nil {
				stop()
				cancelRequests(cancels)
				return err
			}
		}
	}
	return server.shutdown()
}

func requestKey(id json.RawMessage) string {
	return string(bytes.TrimSpace(id))
}

func cancelRequest(raw json.RawMessage, cancels map[string]context.CancelFunc) {
	var params cancelledParams
	if json.Unmarshal(raw, &params) != nil || len(params.RequestID) == 0 {
		return
	}
	if cancel := cancels[requestKey(params.RequestID)]; cancel != nil {
		cancel()
	}
}

func cancelRequests(cancels map[string]context.CancelFunc) {
	for _, cancel := range cancels {
		cancel()
	}
}

func scanMessages(ctx context.Context, input io.Reader) <-chan scanResult {
	results := make(chan scanResult)
	go func() {
		defer close(results)
		scanner := bufio.NewScanner(input)
		scanner.Buffer(make([]byte, 4096), 4<<20)
		for scanner.Scan() {
			line := append([]byte(nil), scanner.Bytes()...)
			select {
			case results <- scanResult{line: line}:
			case <-ctx.Done():
				return
			}
		}
		if err := scanner.Err(); err != nil {
			select {
			case results <- scanResult{err: err}:
			case <-ctx.Done():
			}
		}
	}()
	return results
}

func (server *Server) shutdown() error {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), bridgeShutdownTimeout)
	defer cancel()
	return server.Agent.Disable(shutdownCtx)
}

func (server *Server) handle(ctx context.Context, request rpcRequest) rpcResponse {
	response := rpcResponse{JSONRPC: "2.0", ID: request.ID}
	switch request.Method {
	case "initialize":
		response.Result = map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "openlinker-" + server.Host, "version": buildinfo.Version},
			"instructions":    "Use OpenLinker tools to discover and call Agents. Agent mode remains disabled until explicitly enabled.",
		}
	case "ping":
		response.Result = map[string]any{}
	case "tools/list":
		response.Result = map[string]any{"tools": toolDefinitions()}
	case "tools/call":
		var params toolCallParams
		if err := decodeStrict(request.Params, &params); err != nil || strings.TrimSpace(params.Name) == "" {
			response.Error = &rpcError{Code: -32602, Message: "Invalid tools/call parameters"}
			return response
		}
		result, err := server.callTool(ctx, params.Name, params.Arguments)
		if err != nil {
			response.Result = errorToolResult(err)
		} else {
			response.Result = result
		}
	default:
		response.Error = &rpcError{Code: -32601, Message: "Method not found"}
	}
	return response
}

func (server *Server) callTool(ctx context.Context, name string, arguments map[string]any) (toolResult, error) {
	if name == "configure_agent_mode" || name == "enable_agent_mode" || name == "disable_agent_mode" {
		server.agentMu.Lock()
		defer server.agentMu.Unlock()
	}
	switch name {
	case "configure_agent_mode":
		return server.configureAgent(arguments)
	case "enable_agent_mode":
		if server.Agent.Status().State == "ready" {
			if _, _, err := agent.SetEnabled(server.IO.Getenv, true); err != nil {
				return toolResult{}, err
			}
			return successToolResult(server.agentStatus())
		}
		config, _, err := agent.SetEnabled(server.IO.Getenv, true)
		if err != nil {
			return toolResult{}, err
		}
		startupCtx, cancel := context.WithTimeout(ctx, agentStartupTimeout)
		defer cancel()
		if err := server.Agent.Enable(startupCtx, config.Provider); err != nil {
			_, _, _ = agent.SetEnabled(server.IO.Getenv, false)
			return toolResult{}, err
		}
		return successToolResult(server.agentStatus())
	case "disable_agent_mode":
		if err := server.Agent.Disable(ctx); err != nil {
			return toolResult{}, err
		}
		if _, _, err := agent.SetEnabled(server.IO.Getenv, false); err != nil {
			return toolResult{}, err
		}
		return successToolResult(server.agentStatus())
	case "get_agent_mode_status":
		return successToolResult(server.agentStatus())
	case "diagnose_agent_mode":
		return successToolResult(agent.Diagnose(server.IO.Getenv, stringArgument(arguments, "provider")))
	}
	client, err := shared.UserClient(*server.Options)
	if err != nil {
		return toolResult{}, err
	}
	requestCtx, cancel := context.WithTimeout(ctx, server.Options.Timeout)
	defer cancel()
	var value any
	if err := validateToolArguments(name, arguments); err != nil {
		return toolResult{}, err
	}
	switch name {
	case "search_agents":
		value, err = client.ListAgents(requestCtx, openlinker.ListAgentsParams{
			Query: stringArgument(arguments, "query"), Tags: stringSliceArgument(arguments, "tags"),
			Page: intArgument(arguments, "page"), Size: intArgumentDefault(arguments, "size", 20),
			CallableOnly: boolArgument(arguments, "callable_only"),
		})
	case "get_agent":
		value, err = client.GetAgent(requestCtx, requiredString(arguments, "slug"))
	case "create_task":
		value, err = client.RecommendTask(requestCtx, openlinker.RecommendTaskRequest{
			Query: requiredString(arguments, "query"), TemplateID: stringArgument(arguments, "template_id"),
			SkillIDs: stringSliceArgument(arguments, "skill_ids"), MCPTools: stringSliceArgument(arguments, "mcp_tools"),
			AgentSlugs: stringSliceArgument(arguments, "agent_slugs"),
		})
	case "run_agent", "start_agent_run":
		if name == "start_agent_run" && stringArgument(arguments, "idempotency_key") == "" {
			return toolResult{}, errors.New("start_agent_run requires idempotency_key")
		}
		runRequest := openlinker.RunAgentRequest{
			AgentID: requiredString(arguments, "agent_id"), Input: arguments["input"],
			Metadata: arguments["metadata"], IdempotencyKey: stringArgument(arguments, "idempotency_key"),
			A2AContext: a2aContextArgument(arguments),
		}
		if runRequest.Input == nil {
			runRequest.Input = map[string]any{}
		}
		if name == "run_agent" {
			value, err = client.RunAgent(requestCtx, runRequest)
		} else {
			value, err = client.StartAgentRun(requestCtx, runRequest)
		}
	case "get_run":
		value, err = client.GetRun(requestCtx, requiredString(arguments, "run_id"))
	case "list_run_events":
		value, err = client.ListRunEvents(requestCtx, requiredString(arguments, "run_id"), openlinker.ListRunEventsParams{
			AfterSequence: int32(intArgument(arguments, "after_sequence")), Limit: int32(intArgumentDefault(arguments, "limit", 100)),
		})
	case "list_run_artifacts":
		value, err = client.ListRunArtifacts(requestCtx, requiredString(arguments, "run_id"))
	case "cancel_run":
		value, err = client.CancelRun(requestCtx, requiredString(arguments, "run_id"))
	default:
		return toolResult{}, fmt.Errorf("unknown tool %q", name)
	}
	if err != nil {
		return toolResult{}, err
	}
	return successToolResult(value)
}

func (server *Server) agentStatus() agent.Status {
	status := server.Agent.Status()
	status.Enabled = agent.ModeEnabled(server.IO.Getenv)
	return status
}

func validateToolArguments(name string, arguments map[string]any) error {
	required := map[string][]string{
		"get_agent": {"slug"}, "create_task": {"query"},
		"run_agent": {"agent_id"}, "start_agent_run": {"agent_id", "idempotency_key"},
		"get_run": {"run_id"}, "list_run_events": {"run_id"},
		"list_run_artifacts": {"run_id"}, "cancel_run": {"run_id"},
	}
	for _, key := range required[name] {
		if stringArgument(arguments, key) == "" {
			return fmt.Errorf("%s requires %s", name, key)
		}
	}
	if name == "run_agent" || name == "start_agent_run" {
		if _, ok := arguments["input"]; !ok {
			return fmt.Errorf("%s requires input", name)
		}
	}
	return nil
}

func (server *Server) configureAgent(arguments map[string]any) (toolResult, error) {
	if server.Agent != nil {
		switch server.Agent.Status().State {
		case "starting", "ready", "draining":
			return toolResult{}, errors.New("disable Agent mode before changing its configuration")
		}
	}
	if containsSecretArgument(arguments) {
		return toolResult{}, errors.New("Agent mode secrets must be supplied through environment or _FILE variables")
	}
	var sessionReuse, webSearch *bool
	if value, ok := arguments["session_reuse"].(bool); ok {
		sessionReuse = &value
	}
	if value, ok := arguments["web_search"].(bool); ok {
		webSearch = &value
	}
	config, path, err := agent.ConfigureNonSecret(server.IO.Getenv, agent.ConfigureOptions{
		Provider: stringArgument(arguments, "provider"), AgentID: stringArgument(arguments, "agent_id"),
		Workspace: stringArgument(arguments, "workspace"), OpenLinkerURL: stringArgument(arguments, "openlinker_url"),
		StateDir: stringArgument(arguments, "state_dir"), ProviderBin: stringArgument(arguments, "provider_bin"),
		Model: stringArgument(arguments, "model"), Transport: stringArgument(arguments, "transport"),
		Capacity: int64(intArgument(arguments, "capacity")), TimeoutSeconds: intArgument(arguments, "timeout_seconds"),
		SessionReuse: sessionReuse, WebSearch: webSearch,
		CodexBaseURL: stringArgument(arguments, "codex_base_url"),
		CodexSandbox: stringArgument(arguments, "codex_sandbox"), CodexApproval: stringArgument(arguments, "codex_approval"),
		ClaudePermission: stringArgument(arguments, "claude_permission"), AllowedTools: stringSliceArgument(arguments, "allowed_tools"),
		ExecutionProfile:         stringArgument(arguments, "execution_profile"),
		BrowserInteractionPolicy: stringArgument(arguments, "browser_interaction_policy"),
		BrowserClientMode:        stringArgument(arguments, "browser_client_mode"),
		BrowserNativePlugin:      stringArgument(arguments, "browser_native_plugin"),
		BrowserPluginBin:         stringArgument(arguments, "browser_plugin_bin"),
		BrowserSocket:            stringArgument(arguments, "browser_socket"),
		BrowserCredentialFile:    stringArgument(arguments, "browser_credential_file"),
		BrowserLeaseRoot:         stringArgument(arguments, "browser_lease_root"),
		BrowserBrokerRoot:        stringArgument(arguments, "browser_broker_root"),
	})
	if err != nil {
		return toolResult{}, err
	}
	return successToolResult(map[string]any{
		"configured": true, "config_path": path, "provider": config.Provider, "agent_id": config.AgentID,
		"workspace": config.Workspace, "secrets_written": false, "enabled": config.Enabled,
		"execution_profile":          config.ExecutionProfile,
		"browser_interaction_policy": config.BrowserInteractionPolicy,
		"browser_client_mode":        config.BrowserClientMode,
	})
}

func successToolResult(value any) (toolResult, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return toolResult{}, err
	}
	return toolResult{Content: []contentBlock{{Type: "text", Text: string(raw)}}, StructuredContent: value}, nil
}

func errorToolResult(err error) toolResult {
	message := "OpenLinker tool failed"
	if err != nil && strings.TrimSpace(err.Error()) != "" {
		message = err.Error()
	}
	if len(message) > 1000 {
		message = message[:1000]
	}
	return toolResult{Content: []contentBlock{{Type: "text", Text: message}}, IsError: true}
}

func decodeStrict(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("unexpected trailing JSON")
	}
	return nil
}

func requiredString(arguments map[string]any, key string) string {
	return strings.TrimSpace(stringArgument(arguments, key))
}
func stringArgument(arguments map[string]any, key string) string {
	value, _ := arguments[key].(string)
	return strings.TrimSpace(value)
}
func boolArgument(arguments map[string]any, key string) bool {
	value, _ := arguments[key].(bool)
	return value
}
func intArgument(arguments map[string]any, key string) int {
	switch value := arguments[key].(type) {
	case float64:
		return int(value)
	case json.Number:
		parsed, _ := value.Int64()
		return int(parsed)
	default:
		return 0
	}
}
func intArgumentDefault(arguments map[string]any, key string, fallback int) int {
	if _, ok := arguments[key]; !ok {
		return fallback
	}
	return intArgument(arguments, key)
}
func stringSliceArgument(arguments map[string]any, key string) []string {
	values, _ := arguments[key].([]any)
	result := make([]string, 0, len(values))
	for _, value := range values {
		if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
			result = append(result, strings.TrimSpace(text))
		}
	}
	return result
}

func a2aContextArgument(arguments map[string]any) *openlinker.RunA2AContext {
	conversationID := stringArgument(arguments, "conversation_id")
	value := map[string]any{}
	if provided, ok := arguments["a2a_context"].(map[string]any); ok {
		value = provided
	}
	contextValue := &openlinker.RunA2AContext{
		ProtocolContextID: stringArgument(value, "protocol_context_id"), ProtocolTaskID: stringArgument(value, "protocol_task_id"),
		RootContextID: stringArgument(value, "root_context_id"), ParentContextID: stringArgument(value, "parent_context_id"),
		ParentTaskID: stringArgument(value, "parent_task_id"), ParentRunID: stringArgument(value, "parent_run_id"),
		CallerAgentID: stringArgument(value, "caller_agent_id"), TargetAgentID: stringArgument(value, "target_agent_id"),
		TraceID: stringArgument(value, "trace_id"), Source: stringArgument(value, "source"),
		ReferenceTaskIDs: stringSliceArgument(value, "reference_task_ids"),
	}
	if conversationID != "" {
		if contextValue.ProtocolContextID == "" {
			contextValue.ProtocolContextID = conversationID
		}
		if contextValue.RootContextID == "" {
			contextValue.RootContextID = conversationID
		}
	}
	if contextValue.ProtocolContextID == "" && contextValue.ProtocolTaskID == "" && contextValue.RootContextID == "" &&
		contextValue.ParentContextID == "" && contextValue.ParentTaskID == "" && contextValue.ParentRunID == "" &&
		contextValue.CallerAgentID == "" && contextValue.TargetAgentID == "" && contextValue.TraceID == "" &&
		contextValue.Source == "" && len(contextValue.ReferenceTaskIDs) == 0 {
		return nil
	}
	return contextValue
}

func containsSecretArgument(arguments map[string]any) bool {
	for key, value := range arguments {
		lower := strings.ToLower(key)
		if strings.Contains(lower, "token") || strings.Contains(lower, "secret") || strings.Contains(lower, "api_key") || strings.Contains(lower, "apikey") {
			return true
		}
		switch nested := value.(type) {
		case map[string]any:
			if containsSecretArgument(nested) {
				return true
			}
		case []any:
			for _, item := range nested {
				if object, ok := item.(map[string]any); ok && containsSecretArgument(object) {
					return true
				}
			}
		}
	}
	return false
}
