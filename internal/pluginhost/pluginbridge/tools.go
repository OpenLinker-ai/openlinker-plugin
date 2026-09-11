package pluginbridge

func toolDefinitions() []toolDefinition {
	readOnly := map[string]any{"readOnlyHint": true, "destructiveHint": false, "idempotentHint": true, "openWorldHint": true}
	write := map[string]any{"readOnlyHint": false, "destructiveHint": false, "idempotentHint": false, "openWorldHint": true}
	idempotentWrite := map[string]any{"readOnlyHint": false, "destructiveHint": false, "idempotentHint": true, "openWorldHint": true}
	control := map[string]any{"readOnlyHint": false, "destructiveHint": false, "idempotentHint": true, "openWorldHint": true}
	stringProperty := func(description string) map[string]any {
		return map[string]any{"type": "string", "description": description}
	}
	stringArray := func(description string) map[string]any {
		return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": description}
	}
	runProperties := map[string]any{
		"agent_id":        stringProperty("Target Agent UUID"),
		"input":           map[string]any{"type": "object", "additionalProperties": true, "description": "Agent input"},
		"metadata":        map[string]any{"type": "object", "additionalProperties": true, "description": "Non-secret caller metadata"},
		"idempotency_key": stringProperty("Stable key for one run-creation intent"),
		"conversation_id": stringProperty("Stable Core conversation/root context ID for multi-turn session reuse"),
		"a2a_context": map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{
				"protocol_context_id": stringProperty("Protocol context ID"), "protocol_task_id": stringProperty("Protocol task ID"),
				"root_context_id": stringProperty("Root conversation ID"), "parent_context_id": stringProperty("Parent context ID"),
				"parent_task_id": stringProperty("Parent task ID"), "parent_run_id": stringProperty("Parent OpenLinker Run ID"),
				"caller_agent_id": stringProperty("Caller Agent UUID"), "target_agent_id": stringProperty("Target Agent UUID"),
				"trace_id": stringProperty("Trace ID"), "reference_task_ids": stringArray("Referenced protocol task IDs"),
				"source": stringProperty("Protocol source"),
			},
		},
	}
	return []toolDefinition{
		{Name: "search_agents", Title: "Search OpenLinker Agents", Description: "Search discoverable OpenLinker Agents.", InputSchema: objectSchema(map[string]any{
			"query": stringProperty("Search query"), "tags": stringArray("Tags or skill IDs"),
			"page": map[string]any{"type": "integer", "minimum": 0}, "size": map[string]any{"type": "integer", "minimum": 1, "maximum": 100},
			"callable_only": map[string]any{"type": "boolean"},
		}, nil), Annotations: readOnly},
		{Name: "get_agent", Title: "Get OpenLinker Agent", Description: "Get one Agent by slug.", InputSchema: objectSchema(map[string]any{"slug": stringProperty("Agent slug")}, []string{"slug"}), Annotations: readOnly},
		{Name: "create_task", Title: "Create OpenLinker Task", Description: "Resolve a private task into skills and Agent recommendations.", InputSchema: objectSchema(map[string]any{
			"query": stringProperty("Natural-language task intent"), "template_id": stringProperty("Optional task template ID"),
			"skill_ids": stringArray("Required skill IDs"), "mcp_tools": stringArray("Required MCP tools"), "agent_slugs": stringArray("Candidate Agent slugs"),
		}, []string{"query"}), Annotations: write},
		{Name: "run_agent", Title: "Run OpenLinker Agent", Description: "Run an Agent synchronously.", InputSchema: objectSchema(runProperties, []string{"agent_id", "input"}), Annotations: write},
		{Name: "start_agent_run", Title: "Start OpenLinker Agent Run", Description: "Start an asynchronous Agent Run. A stable idempotency key is required.", InputSchema: objectSchema(runProperties, []string{"agent_id", "input", "idempotency_key"}), Annotations: idempotentWrite},
		{Name: "get_run", Title: "Get OpenLinker Run", Description: "Get one Run and its execution evidence.", InputSchema: objectSchema(map[string]any{"run_id": stringProperty("OpenLinker Run UUID")}, []string{"run_id"}), Annotations: readOnly},
		{Name: "list_run_events", Title: "List OpenLinker Run Events", Description: "List durable events for one Run.", InputSchema: objectSchema(map[string]any{
			"run_id": stringProperty("OpenLinker Run UUID"), "after_sequence": map[string]any{"type": "integer", "minimum": 0},
			"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 500},
		}, []string{"run_id"}), Annotations: readOnly},
		{Name: "list_run_artifacts", Title: "List OpenLinker Run Artifacts", Description: "List artifacts produced by a Run.", InputSchema: objectSchema(map[string]any{"run_id": stringProperty("OpenLinker Run UUID")}, []string{"run_id"}), Annotations: readOnly},
		{Name: "cancel_run", Title: "Cancel OpenLinker Run", Description: "Request durable cancellation for a caller-owned Run.", InputSchema: objectSchema(map[string]any{"run_id": stringProperty("OpenLinker Run UUID")}, []string{"run_id"}), Annotations: control},
		{Name: "configure_agent_mode", Title: "Configure OpenLinker Agent Mode", Description: "Write non-secret Agent mode configuration. Credentials are never accepted as tool arguments.", InputSchema: objectSchema(map[string]any{
			"provider": map[string]any{"type": "string", "enum": []string{"codex", "claude"}}, "agent_id": stringProperty("Existing Agent UUID"),
			"workspace": stringProperty("Provider workspace path"), "openlinker_url": stringProperty("Public OpenLinker platform URL"),
			"state_dir": stringProperty("Private persistent state directory"), "provider_bin": stringProperty("Provider CLI binary"),
			"model": stringProperty("Provider model"), "transport": map[string]any{"type": "string", "enum": []string{"auto", "websocket", "pull"}},
			"capacity": map[string]any{"type": "integer", "minimum": 1, "maximum": 1024}, "timeout_seconds": map[string]any{"type": "integer", "minimum": 1},
			"session_reuse": map[string]any{"type": "boolean"}, "web_search": map[string]any{"type": "boolean"},
			"codex_base_url": stringProperty("Codex OpenAI-compatible API Base URL"),
			"codex_sandbox":  map[string]any{"type": "string", "enum": []string{"read-only", "workspace-write", "danger-full-access"}},
			"codex_approval": stringProperty("Codex approval mode"), "claude_permission": stringProperty("Claude permission mode"),
			"allowed_tools":              stringArray("Claude allowed tools"),
			"execution_profile":          map[string]any{"type": "string", "enum": []string{"standard", "browser"}},
			"browser_interaction_policy": map[string]any{"type": "string", "enum": []string{"restricted", "full"}},
			"browser_client_mode":        map[string]any{"type": "string", "enum": []string{"auto", "openlinker-native-chrome", "official-chrome", "isolated-native", "isolated-mcp", "native", "mcp"}},
			"browser_native_plugin":      stringProperty("Absolute Runtime-owned native Browser Plugin path"),
			"browser_plugin_bin":         stringProperty("OpenLinker Plugin host binary used for the isolated Browser tool server"),
			"browser_socket":             stringProperty("Private Browser Runtime Unix socket path"),
			"browser_credential_file":    stringProperty("Owner-only Browser channel credential file path; never the credential value"),
			"browser_lease_root":         stringProperty("Private Browser lease directory shared with Browser Runtime"),
			"browser_broker_root":        stringProperty("Private local Browser tool broker directory"),
		}, []string{"provider", "agent_id", "workspace"}), Annotations: control},
		{Name: "enable_agent_mode", Title: "Enable OpenLinker Agent Mode", Description: "Start the local Runtime Worker after explicit user confirmation.", InputSchema: objectSchema(nil, nil), Annotations: control},
		{Name: "disable_agent_mode", Title: "Disable OpenLinker Agent Mode", Description: "Gracefully drain and stop the local Runtime Worker.", InputSchema: objectSchema(nil, nil), Annotations: control},
		{Name: "get_agent_mode_status", Title: "Get OpenLinker Agent Mode Status", Description: "Return redacted local Agent mode status.", InputSchema: objectSchema(nil, nil), Annotations: readOnly},
		{Name: "diagnose_agent_mode", Title: "Diagnose OpenLinker Agent Mode", Description: "Check local configuration and credential presence without exposing values.", InputSchema: objectSchema(map[string]any{"provider": map[string]any{"type": "string", "enum": []string{"codex", "claude"}}}, nil), Annotations: readOnly},
	}
}

func objectSchema(properties map[string]any, required []string) map[string]any {
	if properties == nil {
		properties = map[string]any{}
	}
	schema := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}
