package browserplugin

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserprotocol"
)

type fakeExecutor struct {
	mu          sync.Mutex
	actions     []browserprotocol.Action
	observation browserprotocol.Observation
	failure     *browserprotocol.Failure
	execute     func(context.Context, browserprotocol.Action) (
		browserprotocol.Observation,
		*browserprotocol.Failure,
	)
}

type directEvidenceExecutor struct {
	identity browserprotocol.Identity
	actions  []browserprotocol.Action
}

func (executor *directEvidenceExecutor) Identity() browserprotocol.Identity {
	return executor.identity
}

func (executor *directEvidenceExecutor) Execute(
	_ context.Context,
	action browserprotocol.Action,
) (browserprotocol.Observation, *browserprotocol.Failure) {
	executor.actions = append(executor.actions, action)
	if action.Kind == browserprotocol.ActionPreflight {
		return browserprotocol.Observation{
			Environment: &browserprotocol.EnvironmentEvidence{
				BrowserEngine:       "chromium",
				BrowserDistribution: "playwright_chromium",
				BrowserVersion:      "149.0.7827.55",
				BrowserMajorVersion: 149,
				BrowserLocale:       "en-US",
				BrowserTimezone:     "UTC",
				FontContractVersion: "openlinker.browser.fonts.v1",
				FontManifestSHA256:  strings.Repeat("a", 64),
			},
		}, nil
	}
	return browserprotocol.Observation{PageStateID: "state-2"}, nil
}

func (executor *fakeExecutor) Execute(
	ctx context.Context,
	action browserprotocol.Action,
) (browserprotocol.Observation, *browserprotocol.Failure) {
	executor.mu.Lock()
	executor.actions = append(executor.actions, action)
	executor.mu.Unlock()
	if executor.execute != nil {
		return executor.execute(ctx, action)
	}
	return executor.observation, executor.failure
}

func TestServerListsOnlyClientOwnedBrowserTool(t *testing.T) {
	input := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}` + "\n" +
			`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n" +
			`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}` + "\n",
	)
	var output bytes.Buffer
	server := &Server{
		Host:    "codex",
		Version: "runtime-test-version",
		IO:      IO{Getenv: func(string) string { return "" }},
		ClientFactory: func() (Executor, error) {
			t.Fatal("tools/list must not initialize Browser Runtime")
			return nil, nil
		},
	}
	if err := server.Serve(context.Background(), input, &output); err != nil {
		t.Fatal(err)
	}
	responses := decodeResponses(t, output.String())
	initialized := responses["1"]["result"].(map[string]any)
	serverInfo := initialized["serverInfo"].(map[string]any)
	if serverInfo["name"] != "openlinker-browser-codex" {
		t.Fatalf("serverInfo = %#v", serverInfo)
	}
	if serverInfo["version"] != "runtime-test-version" {
		t.Fatalf("Browser MCP handshake did not use its injected executable version: %#v", serverInfo)
	}
	listed := responses["2"]["result"].(map[string]any)
	tools := listed["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %#v", tools)
	}
	tool := tools[0].(map[string]any)
	if tool["name"] != "browser_session" {
		t.Fatalf("tool = %#v", tool)
	}
	schemaRaw, _ := json.Marshal(tool["inputSchema"])
	if !strings.Contains(string(schemaRaw), `"observation"`) ||
		strings.Contains(string(schemaRaw), `"none"`) {
		t.Fatalf("schema observation modes = %s", schemaRaw)
	}
	if !strings.Contains(string(schemaRaw), "Multi-action batches may contain only scroll") ||
		strings.Contains(string(schemaRaw), "exact completed-action count") {
		t.Fatalf("restricted batch contract = %s", schemaRaw)
	}
	annotations := tool["annotations"].(map[string]any)
	if annotations["destructiveHint"] != true {
		t.Fatalf("Browser tool must require destructive approval: %#v", annotations)
	}
	for _, forbidden := range []string{
		"run_id",
		"agent_id",
		"principal_scope_id",
		"conversation_id",
		"browser_session_id",
		"session_epoch",
		"attachment_id",
		"control_epoch",
		"controller",
		"browser_interaction_policy",
		"browser_interaction_policy_generation",
		"browser_mutation_origins",
		"browser_mutation_origins_sha256",
		"channel_credential",
		"api_key",
	} {
		if strings.Contains(string(schemaRaw), forbidden) {
			t.Fatalf("schema exposes trusted or secret field %q: %s", forbidden, schemaRaw)
		}
	}
}

func TestDirectNativeServerUsesLeasePolicyAndPreflightsAttachmentEvidence(
	t *testing.T,
) {
	canonical, digest, failure := browserprotocol.CanonicalMutationOrigins(
		"full",
		[]string{"https://example.com"},
	)
	if failure != nil {
		t.Fatal(failure)
	}
	identity := browserprotocol.Identity{
		RunID:                              "11111111-1111-4111-8111-111111111111",
		AgentID:                            "22222222-2222-4222-8222-222222222222",
		PrincipalScopeID:                   "principal-owner",
		BrowserSessionID:                   "33333333-3333-4333-8333-333333333333",
		SessionEpoch:                       1,
		AttachmentID:                       "44444444-4444-4444-8444-444444444444",
		ControlEpoch:                       2,
		Controller:                         browserprotocol.ControllerAgent,
		BrowserInteractionPolicy:           "full",
		BrowserInteractionPolicyGeneration: 7,
		BrowserMutationOrigins:             canonical,
		BrowserMutationOriginsSHA256:       digest,
	}
	executor := &directEvidenceExecutor{identity: identity}
	input := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}` + "\n" +
			`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"browser_session","arguments":{"operation":"act","actions":[{"kind":"select","value":"option-a"}]}}}` + "\n",
	)
	var output bytes.Buffer
	server := &Server{
		Host: "codex",
		IO:   IO{Getenv: func(string) string { return "" }},
		ClientFactory: func() (Executor, error) {
			return executor, nil
		},
		IdentitySupplier: func() (browserprotocol.Identity, error) {
			return identity, nil
		},
	}
	if err := server.Serve(context.Background(), input, &output); err != nil {
		t.Fatal(err)
	}
	responses := decodeResponses(t, output.String())
	listedRaw, _ := json.Marshal(responses["1"]["result"])
	if !strings.Contains(string(listedRaw), `"select"`) ||
		!strings.Contains(string(listedRaw), "exact completed-action count") ||
		strings.Contains(string(listedRaw), "Multi-action batches may contain only scroll") {
		t.Fatalf("full tools/list schema = %s", listedRaw)
	}
	result := responses["2"]["result"].(map[string]any)
	structured := result["structuredContent"].(map[string]any)
	evidence := structured["attachment_evidence"].(map[string]any)
	if evidence["browser_interaction_policy"] != "full" ||
		evidence["browser_interaction_policy_generation"] != float64(7) ||
		evidence["browser_contract_id"] != browserprotocol.ContractID {
		t.Fatalf("direct attachment evidence = %#v", evidence)
	}
	if len(executor.actions) != 2 ||
		executor.actions[0].Kind != browserprotocol.ActionPreflight ||
		executor.actions[1].Kind != browserprotocol.ActionSelect {
		t.Fatalf("direct Browser actions = %#v", executor.actions)
	}
}

func TestAttachmentEvidenceEmitsOncePerValidatedEpochWithoutFullVersion(
	t *testing.T,
) {
	t.Parallel()
	controlEpoch := uint64(1)
	server := &Server{
		EvidenceSupplier: func() (EvidenceSnapshot, error) {
			return EvidenceSnapshot{
				Environment: browserprotocol.EnvironmentEvidence{
					BrowserEngine:       "chromium",
					BrowserDistribution: "playwright_chromium",
					BrowserVersion:      "149.0.7827.55",
					BrowserMajorVersion: 149,
					BrowserLocale:       "en-US",
					BrowserTimezone:     "UTC",
					FontContractVersion: "openlinker.browser.fonts.v1",
					FontManifestSHA256:  strings.Repeat("a", 64),
				},
				BrowserSessionID:                   "11111111-1111-4111-8111-111111111111",
				SessionEpoch:                       1,
				ControlEpoch:                       controlEpoch,
				BrowserInteractionPolicy:           "restricted",
				BrowserInteractionPolicyGeneration: 1,
				BrowserMutationOrigins:             []string{},
				BrowserMutationOriginsSHA256:       browserprotocol.RestrictedMutationOriginsSHA256,
			}, nil
		},
	}
	first := toolResult{StructuredContent: map[string]any{"status": "ok"}}
	if evidence, ok := server.takeAttachmentEvidence(); ok {
		addAttachmentEvidence(&first, evidence)
	} else {
		t.Fatal("first MCP result did not receive attachment evidence")
	}
	structured := first.StructuredContent.(map[string]any)
	evidence := structured["attachment_evidence"].(map[string]any)
	if evidence["browser_major_version"] != 149 ||
		evidence["browser_version"] != nil ||
		evidence["browser_interaction_policy"] != "restricted" ||
		evidence["browser_contract_id"] != browserprotocol.ContractID {
		t.Fatalf("attachment evidence = %#v", evidence)
	}
	if _, ok := server.takeAttachmentEvidence(); ok {
		t.Fatal("same MCP epoch emitted attachment evidence twice")
	}
	controlEpoch++
	if _, ok := server.takeAttachmentEvidence(); !ok {
		t.Fatal("new control epoch did not emit attachment evidence")
	}
}

func TestBrowserResultsExposeOnlyBoundedActionEvidence(t *testing.T) {
	t.Parallel()
	retryAfter := 30_000
	failure := browserprotocol.NewFailure(
		browserprotocol.ErrorRateLimited,
		"Website rate limit was reached",
		true,
	)
	failure.SiteOutcome = browserprotocol.ErrorRateLimited
	failure.RetryAfterMS = &retryAfter
	result := browserErrorResult(failure)
	structured := result.StructuredContent.(map[string]any)
	if structured["site_outcome"] != browserprotocol.ErrorRateLimited ||
		structured["retry_after_ms"] != retryAfter {
		t.Fatalf("rate-limit evidence = %#v", structured)
	}
	observation := observationResult("observe", browserprotocol.Observation{
		PageStateID:                 "state-1",
		SiteOutcome:                 browserprotocol.ErrorChallengeSuspected,
		ClassifierRulesVersion:      browserprotocol.ChallengeClassifierRulesVersion,
		ChallengeReleaseUnavailable: true,
	})
	structured = observation.StructuredContent.(map[string]any)
	if structured["site_outcome"] != browserprotocol.ErrorChallengeSuspected ||
		structured["challenge_release_unavailable"] != true {
		t.Fatalf("sticky challenge evidence = %#v", structured)
	}
}

func TestServerReturnsStructuredObservationAndImage(t *testing.T) {
	executor := &fakeExecutor{observation: browserprotocol.Observation{
		PageStateID:          "state-1",
		Origin:               "https://example.com",
		Title:                "Example",
		AXTree:               json.RawMessage(`{"role":"document"}`),
		AXTreeTimedOut:       true,
		DOMDiffTimedOut:      true,
		Viewport:             &browserprotocol.Viewport{Width: 1280, Height: 720},
		NavigationGeneration: 3,
		ClickEffect:          browserprotocol.ClickEffectFocused,
		TargetCategory:       browserprotocol.TargetCategoryTextInput,
		Screenshot: &browserprotocol.Screenshot{
			MIMEType: "image/png",
			Data:     []byte("png-fixture"),
			Width:    1280,
			Height:   720,
		},
	}}
	input := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"browser_session","arguments":{"operation":"observe","observation":"both"},"_meta":{"threadId":"thread-1"}}}` + "\n",
	)
	var output bytes.Buffer
	server := &Server{
		Host: "codex",
		IO:   IO{Getenv: func(string) string { return "" }},
		ClientFactory: func() (Executor, error) {
			return executor, nil
		},
	}
	if err := server.Serve(context.Background(), input, &output); err != nil {
		t.Fatal(err)
	}
	result := decodeResponses(t, output.String())["1"]["result"].(map[string]any)
	if result["isError"] == true {
		t.Fatalf("result = %#v", result)
	}
	structured := result["structuredContent"].(map[string]any)
	if structured["page_state_id"] != "state-1" ||
		structured["origin"] != "https://example.com" ||
		structured["title"] != "Example" ||
		structured["ax_tree_timed_out"] != true ||
		structured["dom_diff_timed_out"] != true ||
		structured["navigation_generation"] != float64(3) ||
		structured["click_effect"] != string(browserprotocol.ClickEffectFocused) ||
		structured["target_category"] != string(browserprotocol.TargetCategoryTextInput) {
		t.Fatalf("structuredContent = %#v", structured)
	}
	viewport := structured["viewport"].(map[string]any)
	screenshot := structured["screenshot"].(map[string]any)
	if viewport["width"] != float64(1280) || viewport["height"] != float64(720) ||
		screenshot["width"] != float64(1280) || screenshot["height"] != float64(720) {
		t.Fatalf("observation dimensions = viewport %#v screenshot %#v", viewport, screenshot)
	}
	content := result["content"].([]any)
	var textObservation map[string]any
	if err := json.Unmarshal([]byte(content[0].(map[string]any)["text"].(string)), &textObservation); err != nil {
		t.Fatalf("text-only MCP client must receive the observation: %v", err)
	}
	textJSON, _ := json.Marshal(textObservation)
	structuredJSON, _ := json.Marshal(structured)
	if !bytes.Equal(textJSON, structuredJSON) {
		t.Fatalf("text and structured observations differ: %s / %s", textJSON, structuredJSON)
	}
	if len(content) != 2 || content[1].(map[string]any)["type"] != "image" ||
		content[1].(map[string]any)["mimeType"] != "image/png" {
		t.Fatalf("content = %#v", content)
	}
	executor.mu.Lock()
	defer executor.mu.Unlock()
	if len(executor.actions) != 1 ||
		executor.actions[0].Kind != browserprotocol.ActionScreenshot ||
		executor.actions[0].Observation != browserprotocol.ObservationBoth {
		t.Fatalf("actions = %#v", executor.actions)
	}
}

func TestServerReturnsOnlyValidatedBlockedClickMetadata(t *testing.T) {
	navigationRemaining := 1
	runRemaining := 9
	failure := browserprotocol.NewFailure(
		browserprotocol.ErrorHighImpactActionBlocked,
		"Browser click target is blocked",
		false,
	)
	failure.TargetCategory = browserprotocol.TargetCategoryButton
	failure.PageStateID = "state-blocked"
	failure.NavigationGeneration = 4
	failure.BlockedClickNavigationAttemptsRemaining = &navigationRemaining
	failure.BlockedClickRunAttemptsRemaining = &runRemaining
	executor := &fakeExecutor{failure: failure}
	input := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"browser_session","arguments":{"operation":"act","actions":[{"kind":"click","x":100,"y":200}]}}}` + "\n",
	)
	var output bytes.Buffer
	server := &Server{
		Host: "codex",
		IO:   IO{Getenv: func(string) string { return "" }},
		ClientFactory: func() (Executor, error) {
			return executor, nil
		},
	}
	if err := server.Serve(context.Background(), input, &output); err != nil {
		t.Fatal(err)
	}
	result := decodeResponses(t, output.String())["1"]["result"].(map[string]any)
	structured := result["structuredContent"].(map[string]any)
	if result["isError"] != true ||
		structured["target_category"] != string(browserprotocol.TargetCategoryButton) ||
		structured["page_state_id"] != "state-blocked" ||
		structured["navigation_generation"] != float64(4) ||
		structured["blocked_click_navigation_attempts_remaining"] != float64(1) ||
		structured["blocked_click_run_attempts_remaining"] != float64(9) {
		t.Fatalf("blocked click result = %#v", result)
	}
}

func TestServerBatchSuppressesIntermediateObservationsAndReportsProgress(t *testing.T) {
	failedIndex := 2
	executor := &fakeExecutor{
		execute: func(
			_ context.Context,
			action browserprotocol.Action,
		) (browserprotocol.Observation, *browserprotocol.Failure) {
			if action.Kind != browserprotocol.ActionBatch {
				t.Fatalf("action = %#v", action)
			}
			failure := browserprotocol.NewFailure(
				browserprotocol.ErrorRuntimeUnavailable,
				"fixture failed",
				true,
			)
			failure.ActionIndex = &failedIndex
			return browserprotocol.Observation{}, failure
		},
	}
	input := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"browser_session","arguments":{"operation":"act","observation":"both","actions":[{"kind":"scroll","delta_y":1},{"kind":"wait","duration_ms":1},{"kind":"screenshot"}]}}}` + "\n",
	)
	var output bytes.Buffer
	server := &Server{
		Host: "codex",
		IO:   IO{Getenv: func(string) string { return "" }},
		ClientFactory: func() (Executor, error) {
			return executor, nil
		},
	}
	if err := server.Serve(context.Background(), input, &output); err != nil {
		t.Fatal(err)
	}
	result := decodeResponses(t, output.String())["1"]["result"].(map[string]any)
	if result["isError"] != true {
		t.Fatalf("result = %#v", result)
	}
	structured := result["structuredContent"].(map[string]any)
	if structured["completed_actions"] != float64(2) {
		t.Fatalf("structuredContent = %#v", structured)
	}
	if structured["failed_action_index"] != float64(2) {
		t.Fatalf("structuredContent = %#v", structured)
	}
	executor.mu.Lock()
	defer executor.mu.Unlock()
	if len(executor.actions) != 1 ||
		executor.actions[0].Kind != browserprotocol.ActionBatch ||
		executor.actions[0].Observation != browserprotocol.ObservationBoth ||
		len(executor.actions[0].Actions) != 3 ||
		executor.actions[0].Actions[0].Kind != browserprotocol.ActionScroll ||
		executor.actions[0].Actions[1].Kind != browserprotocol.ActionWait ||
		executor.actions[0].Actions[2].Kind != browserprotocol.ActionScreenshot {
		t.Fatalf("batch observation modes = %#v", executor.actions)
	}
}

func TestServerCheckpointRequestsNoFullObservation(t *testing.T) {
	executor := &fakeExecutor{observation: browserprotocol.Observation{
		PageStateID: "checkpoint-state",
	}}
	input := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"browser_session","arguments":{"operation":"checkpoint","observation":"semantic"}}}` + "\n",
	)
	var output bytes.Buffer
	server := &Server{
		Host: "claude",
		IO:   IO{Getenv: func(string) string { return "" }},
		ClientFactory: func() (Executor, error) {
			return executor, nil
		},
	}
	if err := server.Serve(context.Background(), input, &output); err != nil {
		t.Fatal(err)
	}
	executor.mu.Lock()
	defer executor.mu.Unlock()
	if len(executor.actions) != 1 ||
		executor.actions[0].Kind != browserprotocol.ActionCheckpoint ||
		executor.actions[0].Observation != browserprotocol.ObservationNone {
		t.Fatalf("checkpoint actions = %#v", executor.actions)
	}
}

func TestServerRejectsCallerAuthorityAndUnsafeBatch(t *testing.T) {
	executor := &fakeExecutor{}
	input := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"browser_session","arguments":{"operation":"observe","run_id":"11111111-1111-4111-8111-111111111111"}}}` + "\n" +
			`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"browser_session","arguments":{"operation":"observe","browser_interaction_policy":"full"}}}` + "\n" +
			`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"browser_session","arguments":{"operation":"observe","browser_mutation_origins":["https://example.com"]}}}` + "\n" +
			`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"browser_session","arguments":{"operation":"act","actions":[{"kind":"click","x":1,"y":2},{"kind":"wait","duration_ms":1}]}}}` + "\n",
	)
	var output bytes.Buffer
	server := &Server{
		Host: "claude",
		IO:   IO{Getenv: func(string) string { return "" }},
		ClientFactory: func() (Executor, error) {
			return executor, nil
		},
	}
	if err := server.Serve(context.Background(), input, &output); err != nil {
		t.Fatal(err)
	}
	responses := decodeResponses(t, output.String())
	for _, id := range []string{"1", "2", "3", "4"} {
		result := responses[id]["result"].(map[string]any)
		if result["isError"] != true {
			t.Fatalf("response %s = %#v", id, responses[id])
		}
	}
	executor.mu.Lock()
	defer executor.mu.Unlock()
	if len(executor.actions) != 0 {
		t.Fatalf("rejected calls executed actions: %#v", executor.actions)
	}
}

func TestServerCloseRejectsLaterActions(t *testing.T) {
	executor := &fakeExecutor{}
	server := &Server{
		Host: "codex",
		IO:   IO{Getenv: func(string) string { return "" }},
		ClientFactory: func() (Executor, error) {
			return executor, nil
		},
	}
	var closeOutput bytes.Buffer
	if err := server.Serve(
		context.Background(),
		strings.NewReader(
			`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"browser_session","arguments":{"operation":"close"}}}`+"\n",
		),
		&closeOutput,
	); err != nil {
		t.Fatal(err)
	}
	closeResult := decodeResponses(t, closeOutput.String())["1"]["result"].(map[string]any)
	if closeResult["structuredContent"].(map[string]any)["status"] != "closed" {
		t.Fatalf("close result = %#v", closeResult)
	}
	if len(executor.actions) != 1 || executor.actions[0].Kind != browserprotocol.ActionClose {
		t.Fatalf("close actions = %#v", executor.actions)
	}
	var observeOutput bytes.Buffer
	if err := server.Serve(
		context.Background(),
		strings.NewReader(
			`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"browser_session","arguments":{"operation":"observe"}}}`+"\n",
		),
		&observeOutput,
	); err != nil {
		t.Fatal(err)
	}
	observeResult := decodeResponses(t, observeOutput.String())["2"]["result"].(map[string]any)
	if observeResult["isError"] != true {
		t.Fatalf("observe result = %#v", observeResult)
	}
}

func TestServerCancellationInterruptsBrowserAction(t *testing.T) {
	reader, writer := io.Pipe()
	defer writer.Close()
	started := make(chan struct{})
	executor := &fakeExecutor{
		execute: func(
			ctx context.Context,
			_ browserprotocol.Action,
		) (browserprotocol.Observation, *browserprotocol.Failure) {
			close(started)
			<-ctx.Done()
			return browserprotocol.Observation{}, browserprotocol.NewFailure(
				browserprotocol.ErrorCanceled,
				"browser request was canceled",
				false,
			)
		},
	}
	var output bytes.Buffer
	server := &Server{
		Host: "codex",
		IO:   IO{Getenv: func(string) string { return "" }},
		ClientFactory: func() (Executor, error) {
			return executor, nil
		},
	}
	done := make(chan error, 1)
	go func() {
		done <- server.Serve(context.Background(), reader, &output)
	}()
	_, _ = io.WriteString(
		writer,
		`{"jsonrpc":"2.0","id":"action-1","method":"tools/call","params":{"name":"browser_session","arguments":{"operation":"observe"}}}`+"\n",
	)
	<-started
	_, _ = io.WriteString(
		writer,
		`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":"action-1"}}`+"\n",
	)
	_ = writer.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Browser plugin cancellation did not finish")
	}
	responses := decodeResponses(t, output.String())
	result := responses[`"action-1"`]["result"].(map[string]any)
	if result["isError"] != true {
		t.Fatalf("result = %#v", result)
	}
}

func decodeResponses(
	t *testing.T,
	output string,
) map[string]map[string]any {
	t.Helper()
	responses := map[string]map[string]any{}
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var response map[string]any
		if err := json.Unmarshal([]byte(line), &response); err != nil {
			t.Fatalf("invalid JSON-RPC output %q: %v", line, err)
		}
		idRaw, _ := json.Marshal(response["id"])
		key := string(idRaw)
		if number, ok := response["id"].(float64); ok {
			key = strings.TrimSuffix(strings.TrimSuffix(
				json.Number(fmtFloat(number)).String(),
				".0",
			), ".")
		}
		responses[key] = response
	}
	return responses
}

func fmtFloat(value float64) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}
