package browserprotocol

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestRequestValidateAcceptsOnlyBoundedPhaseOneActions(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	x, y, delta, wait := 10, 20, 100, 250
	actions := []Action{
		{Kind: ActionNavigate, URL: "https://example.com/path"},
		{Kind: ActionClick, X: &x, Y: &y},
		{Kind: ActionTypeNonSecret, Text: "public search text"},
		{Kind: ActionScroll, DeltaY: &delta},
		{Kind: ActionKeypress, Key: "Enter"},
		{Kind: ActionWait, DurationMS: &wait},
		{Kind: ActionBack},
		{Kind: ActionForward},
		{Kind: ActionScreenshot},
		{Kind: ActionCheckpoint},
		{Kind: ActionClose},
	}
	for _, action := range actions {
		action := action
		t.Run(string(action.Kind), func(t *testing.T) {
			request := validRequest(now)
			request.Action = action
			if failure := request.Validate(now); failure != nil {
				t.Fatalf("Validate() failure = %v", failure)
			}
		})
	}
}

func TestActionValidateRejectsArbitraryOrSmuggledCapabilities(t *testing.T) {
	t.Parallel()
	x, y := 10, 20
	cases := []Action{
		{Kind: "javascript", Text: "document.cookie"},
		{Kind: "shell", Text: "id"},
		{Kind: ActionClick, X: &x, Y: &y, Text: "unexpected"},
		{Kind: ActionTypeNonSecret, Text: ""},
		{Kind: ActionKeypress, Key: "a"},
		{Kind: ActionNavigate, URL: "file:///etc/passwd"},
		{Kind: ActionNavigate, URL: "https://user:secret@example.com/"},
		{Kind: ActionKeypress, Key: "Space"},
	}
	for _, action := range cases {
		if failure := action.Validate(); failure == nil || failure.Code != ErrorProtocolInvalid {
			t.Errorf("Validate(%#v) failure = %#v, want %s", action, failure, ErrorProtocolInvalid)
		}
	}
	if failure := (Action{
		Kind:  ActionSelect,
		X:     &x,
		Y:     &y,
		Value: "option-a",
	}).Validate(); failure == nil || failure.Code != ErrorActionRejected {
		t.Fatalf("select failure = %#v, want %s", failure, ErrorActionRejected)
	}
}

func TestActionValidateAcceptsOnlyBoundedSafeBatches(t *testing.T) {
	t.Parallel()
	delta, wait := 100, 25
	valid := Action{
		Kind:        ActionBatch,
		Observation: ObservationBoth,
		Actions: []Action{
			{Kind: ActionScroll, DeltaY: &delta},
			{Kind: ActionWait, DurationMS: &wait},
			{Kind: ActionScreenshot},
		},
	}
	if failure := valid.Validate(); failure != nil {
		t.Fatalf("valid batch failure = %v", failure)
	}
	cases := []Action{
		{Kind: ActionBatch, Actions: []Action{{Kind: ActionScreenshot}}},
		{
			Kind: ActionBatch,
			Actions: []Action{
				{Kind: ActionClick},
				{Kind: ActionScreenshot},
			},
		},
		{
			Kind: ActionBatch,
			Actions: []Action{
				{Kind: ActionWait, DurationMS: &wait, Observation: ObservationSemantic},
				{Kind: ActionScreenshot},
			},
		},
		{
			Kind: ActionBatch,
			Actions: []Action{
				{
					Kind:    ActionBatch,
					Actions: []Action{{Kind: ActionScreenshot}, {Kind: ActionScreenshot}},
				},
				{Kind: ActionScreenshot},
			},
		},
	}
	for _, action := range cases {
		if failure := action.Validate(); failure == nil ||
			failure.Code != ErrorProtocolInvalid {
			t.Errorf("Validate(%#v) failure = %#v", action, failure)
		}
	}
	tooMany := Action{Kind: ActionBatch}
	for range 9 {
		tooMany.Actions = append(tooMany.Actions, Action{Kind: ActionScreenshot})
	}
	if failure := tooMany.Validate(); failure == nil ||
		failure.Code != ErrorProtocolInvalid {
		t.Errorf("too many actions failure = %#v", failure)
	}
}

func TestCoordinatesUseTheFixedBrowserViewport(t *testing.T) {
	t.Parallel()
	for _, point := range [][2]int{{0, 0}, {1279, 719}} {
		x, y := point[0], point[1]
		if failure := (Action{Kind: ActionClick, X: &x, Y: &y}).Validate(); failure != nil {
			t.Errorf("point %v failure = %v", point, failure)
		}
	}
	for _, point := range [][2]int{{1280, 0}, {0, 720}, {-1, 0}} {
		x, y := point[0], point[1]
		if failure := (Action{Kind: ActionClick, X: &x, Y: &y}).Validate(); failure == nil {
			t.Errorf("point %v was accepted", point)
		}
	}
}

func TestCanonicalMutationOriginsEnforcesExactHTTPSPolicy(t *testing.T) {
	t.Parallel()
	canonical, digest, failure := CanonicalMutationOrigins("full", []string{
		"https://Example.COM:443",
		"https://xn--bcher-kva.example",
		"https://example.com",
	})
	if failure != nil {
		t.Fatal(failure)
	}
	want := []string{"https://example.com", "https://xn--bcher-kva.example"}
	if !slices.Equal(canonical, want) || digest == "" {
		t.Fatalf("canonical origins = %#v digest=%q", canonical, digest)
	}
	if validation := ValidateMutationOrigins("full", canonical, digest); validation != nil {
		t.Fatalf("canonical origins rejected: %v", validation)
	}
	for _, origins := range [][]string{
		nil,
		{},
		{"http://example.com"},
		{"https://*.example.com"},
		{"https://user@example.com"},
		{"https://example.com/path"},
		{"https://example.com."},
		{"https://127.1"},
		{"https://2130706433"},
		{"https://0x7f.1"},
		{"https://0177.1"},
		{"https://09.1"},
		{"https://1.2.3.999"},
		{"https://１２７。１"},
		{"https://[::ffff:127.0.0.1]"},
		{"https://[::ffff:7f00:1]"},
	} {
		if _, _, invalid := CanonicalMutationOrigins("full", origins); invalid == nil {
			t.Errorf("invalid full origins accepted: %#v", origins)
		}
	}
	if canonical, _, failure := CanonicalMutationOrigins(
		"full",
		[]string{"https://123.example"},
	); failure != nil || !slices.Equal(canonical, []string{"https://123.example"}) {
		t.Fatalf("ordinary numeric DNS label was rejected: %#v failure=%v", canonical, failure)
	}
	if canonical, _, failure := CanonicalMutationOrigins(
		"full",
		[]string{"HTTPS://BÜCHER.example:0443", "https://example.com:08443"},
	); failure != nil || !slices.Equal(canonical, []string{
		"https://example.com:8443",
		"https://xn--bcher-kva.example",
	}) {
		t.Fatalf("IDNA/default-port origin was not canonicalized: %#v failure=%v", canonical, failure)
	}
	if canonical, digest, failure := CanonicalMutationOrigins("restricted", []string{}); failure != nil ||
		len(canonical) != 0 || digest != RestrictedMutationOriginsSHA256 {
		t.Fatalf("restricted origins = %#v %q failure=%v", canonical, digest, failure)
	}
}

func TestFullPolicyRequestAdmitsV2SelectAndMutationBatch(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	request := validRequest(now)
	origins, digest, failure := CanonicalMutationOrigins(
		"full",
		[]string{"https://public.example"},
	)
	if failure != nil {
		t.Fatal(failure)
	}
	request.Identity.BrowserInteractionPolicy = "full"
	request.Identity.BrowserMutationOrigins = origins
	request.Identity.BrowserMutationOriginsSHA256 = digest
	request.Action = Action{Kind: ActionSelect, Value: "option-a"}
	if failure := request.Validate(now); failure != nil {
		t.Fatalf("full select rejected: %v", failure)
	}
	request.Action = Action{Kind: ActionKeypress, Key: "Space"}
	if failure := request.Validate(now); failure != nil {
		t.Fatalf("full Space keypress rejected: %v", failure)
	}
	x, y := 10, 20
	request.Action = Action{
		Kind: ActionBatch,
		Actions: []Action{
			{Kind: ActionClick, X: &x, Y: &y},
			{Kind: ActionSelect, Value: "option-a"},
		},
	}
	if failure := request.Validate(now); failure != nil {
		t.Fatalf("full mutation batch rejected: %v", failure)
	}
	request.Identity.BrowserInteractionPolicy = "restricted"
	request.Identity.BrowserMutationOrigins = []string{}
	request.Identity.BrowserMutationOriginsSHA256 = RestrictedMutationOriginsSHA256
	if failure := request.Validate(now); failure == nil {
		t.Fatal("restricted request accepted a mutation batch")
	}
}

func TestMutationFailureEvidenceIsStrictAndBounded(t *testing.T) {
	t.Parallel()
	falseValue, trueValue := false, true
	fresh := false
	originFailure := NewFailure(
		ErrorMutationOriginBlocked,
		"mutation origin blocked",
		false,
	)
	originFailure.RetrySameAction = &falseValue
	originFailure.AttachmentUsable = &trueValue
	originFailure.FreshObservationRequired = &fresh
	originFailure.ObservedOrigin = "https://public.example"
	originFailure.BrowserMutationOrigins = []string{"https://public.example"}
	if validation := ValidateFailure(originFailure); validation != nil {
		t.Fatalf("valid origin failure rejected: %v", validation)
	}
	attempted, undispatched := 1, 2
	unknown := NewFailure(
		ErrorMutationOutcomeUnknown,
		"mutation outcome unknown",
		false,
	)
	unknown.RetrySameAction = &falseValue
	unknown.AttachmentUsable = &trueValue
	unknown.FreshObservationRequired = &trueValue
	unknown.MutationOutcomeReason = "text_entry_target_changed_after_unit"
	unknown.AttemptedUnits = &attempted
	unknown.UndispatchedUnits = &undispatched
	if validation := ValidateFailure(unknown); validation != nil {
		t.Fatalf("valid uncertainty failure rejected: %v", validation)
	}
	unknown.RetrySameAction = nil
	if validation := ValidateFailure(unknown); validation == nil {
		t.Fatal("uncertainty failure without explicit retry policy was accepted")
	}
}

func TestNavigateRejectsNonPublicLiteralAndLocalHostnames(t *testing.T) {
	t.Parallel()
	blocked := []string{
		"http://127.0.0.1/",
		"http://10.0.0.1/",
		"http://100.64.0.1/",
		"http://169.254.169.254/",
		"http://192.0.2.1/",
		"http://[::1]/",
		"http://[::ffff:127.0.0.1]/",
		"http://[2001:db8::1]/",
		"https://[2606:4700:4700::1111]/",
		"https://metadata.google.internal/",
		"https://service/",
	}
	for _, raw := range blocked {
		if failure := (Action{Kind: ActionNavigate, URL: raw}).Validate(); failure == nil {
			t.Errorf("Validate(%q) succeeded, want blocked", raw)
		}
	}
	for _, raw := range []string{"https://example.com/"} {
		if failure := (Action{Kind: ActionNavigate, URL: raw}).Validate(); failure != nil {
			t.Errorf("Validate(%q) failure = %v", raw, failure)
		}
	}
}

func TestRequestValidateRejectsExpiredAndUnboundedDeadlines(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	request := validRequest(now)
	request.Deadline = now
	if failure := request.Validate(now); failure == nil || failure.Code != ErrorDeadlineExceeded {
		t.Fatalf("expired deadline failure = %#v", failure)
	}
	request.Deadline = now.Add(MaxActionDeadline + time.Second)
	if failure := request.Validate(now); failure == nil || failure.Code != ErrorProtocolInvalid {
		t.Fatalf("unbounded deadline failure = %#v", failure)
	}
}

func TestObservationValidateEnforcesPayloadLimitsAndJSON(t *testing.T) {
	t.Parallel()
	valid := Observation{
		PageStateID: "state-1",
		Viewport: &Viewport{
			Width:  BrowserViewportWidth,
			Height: BrowserViewportHeight,
		},
		NavigationGeneration: 1,
		Screenshot: &Screenshot{
			MIMEType: "image/png",
			Data:     []byte("png"),
			Width:    BrowserViewportWidth,
			Height:   BrowserViewportHeight,
		},
		AXTree:  json.RawMessage(`{"role":"document"}`),
		DOMDiff: json.RawMessage(`{"changed":[]}`),
		Origin:  "https://example.com",
		Title:   "Example",
	}
	if failure := valid.Validate(); failure != nil {
		t.Fatalf("valid observation failure = %v", failure)
	}
	invalidJSON := valid
	invalidJSON.AXTree = json.RawMessage(`{"broken"`)
	if failure := invalidJSON.Validate(); failure == nil || failure.Code != ErrorOutputInvalid {
		t.Fatalf("invalid JSON failure = %#v", failure)
	}
	large := valid
	large.Screenshot = &Screenshot{
		MIMEType: "image/png",
		Data:     bytes.Repeat([]byte{'x'}, MaxScreenshotBytes+1),
		Width:    BrowserViewportWidth,
		Height:   BrowserViewportHeight,
	}
	if failure := large.Validate(); failure == nil || failure.Code != ErrorOutputTooLarge {
		t.Fatalf("large screenshot failure = %#v", failure)
	}
}

func TestObservationValidationSeparatesEngineAndClosedVariants(t *testing.T) {
	t.Parallel()
	engine := Observation{
		PageStateID: "state-1",
		Viewport: &Viewport{
			Width:  BrowserViewportWidth,
			Height: BrowserViewportHeight,
		},
		NavigationGeneration: 1,
		ClickEffect:          ClickEffectFocused,
		TargetCategory:       TargetCategoryTextInput,
	}
	if failure := engine.ValidateEngine(); failure != nil {
		t.Fatalf("valid Engine observation failure = %v", failure)
	}
	invalidPair := engine
	invalidPair.TargetCategory = TargetCategoryLink
	if failure := invalidPair.ValidateEngine(); failure == nil ||
		failure.Code != ErrorOutputInvalid {
		t.Fatalf("invalid click pair failure = %#v", failure)
	}
	closed := Observation{PageStateID: "closed-0123456789abcdef"}
	if failure := closed.ValidateClosed(); failure != nil {
		t.Fatalf("valid closed observation failure = %v", failure)
	}
	closed.Viewport = engine.Viewport
	if failure := closed.ValidateClosed(); failure == nil ||
		failure.Code != ErrorOutputInvalid {
		t.Fatalf("closed Engine-field failure = %#v", failure)
	}
}

func TestEnvironmentEvidenceIsStrictAndEngineBound(t *testing.T) {
	t.Parallel()
	valid := EnvironmentEvidence{
		BrowserEngine:       "chromium",
		BrowserDistribution: "playwright_chromium",
		BrowserVersion:      "140.0.7339.1",
		BrowserMajorVersion: 140,
		BrowserLocale:       "en-US",
		BrowserTimezone:     "Asia/Singapore",
		FontContractVersion: "openlinker.browser.fonts.v1",
		FontManifestSHA256:  strings.Repeat("a", 64),
	}
	if failure := valid.Validate(); failure != nil {
		t.Fatalf("valid environment evidence rejected: %v", failure)
	}
	cases := []EnvironmentEvidence{
		func() EnvironmentEvidence {
			value := valid
			value.BrowserDistribution = "google_chrome"
			return value
		}(),
		func() EnvironmentEvidence {
			value := valid
			value.BrowserLocale = "en US"
			return value
		}(),
		func() EnvironmentEvidence {
			value := valid
			value.BrowserTimezone = "../UTC"
			return value
		}(),
		func() EnvironmentEvidence {
			value := valid
			value.FontManifestSHA256 = strings.Repeat("A", 64)
			return value
		}(),
	}
	for _, evidence := range cases {
		if failure := evidence.Validate(); failure == nil ||
			failure.Code != ErrorOutputInvalid {
			t.Errorf("invalid environment evidence accepted: %#v", evidence)
		}
	}
}

func TestBackendSelectionEvidenceIsStrictAndRedacted(t *testing.T) {
	t.Parallel()
	official := BackendSelectionEvidence{
		RequestedMode:       "auto",
		SelectedBackend:     "official_chrome_extension",
		AssetManifestSHA256: strings.Repeat("a", 64),
		ExtensionID:         "abcdefghijklmnopabcdefghijklmnop",
		ExtensionVersion:    "1.2.3.4",
		NativeHostProtocol:  "openlinker.native-chrome.v2",
		ProfileGeneration:   7,
	}
	if failure := official.Validate(); failure != nil {
		t.Fatalf("valid official backend evidence rejected: %v", failure)
	}
	isolatedFallback := BackendSelectionEvidence{
		RequestedMode:   "auto",
		SelectedBackend: "isolated_chromium",
		FallbackReason:  "official_extension_unavailable",
	}
	if failure := isolatedFallback.Validate(); failure != nil {
		t.Fatalf("valid isolated fallback evidence rejected: %v", failure)
	}
	invalid := []BackendSelectionEvidence{
		{RequestedMode: "auto", SelectedBackend: "official_chrome_extension"},
		{RequestedMode: "official-chrome", SelectedBackend: "isolated_chromium"},
		{RequestedMode: "official-chrome", SelectedBackend: "isolated_chromium", FallbackReason: "official_extension_unavailable"},
		{
			RequestedMode:       "isolated",
			SelectedBackend:     "official_chrome_extension",
			AssetManifestSHA256: official.AssetManifestSHA256,
			ExtensionID:         official.ExtensionID,
			ExtensionVersion:    official.ExtensionVersion,
			NativeHostProtocol:  official.NativeHostProtocol,
			ProfileGeneration:   official.ProfileGeneration,
		},
		{RequestedMode: "auto", SelectedBackend: "isolated_chromium", FallbackReason: "BROWSER_TARGET_BLOCKED"},
		{RequestedMode: "isolated", SelectedBackend: "isolated_chromium", ExtensionID: official.ExtensionID},
	}
	for _, evidence := range invalid {
		if failure := evidence.Validate(); failure == nil ||
			failure.Code != ErrorOutputInvalid {
			t.Errorf("invalid backend evidence accepted: %#v", evidence)
		}
	}
}

func TestBackendModeIsAllowedOnlyOnPreflight(t *testing.T) {
	t.Parallel()
	if failure := (Action{
		Kind:        ActionPreflight,
		BackendMode: "openlinker-native-chrome",
	}).Validate(); failure != nil {
		t.Fatalf("valid canonical backend preflight rejected: %v", failure)
	}
	if failure := (Action{
		Kind:        ActionPreflight,
		BackendMode: "official-chrome",
	}).Validate(); failure != nil {
		t.Fatalf("valid backend preflight rejected: %v", failure)
	}
	if failure := (Action{
		Kind:        ActionPreflight,
		BackendMode: "unknown",
	}).Validate(); failure == nil || failure.Code != ErrorProtocolInvalid {
		t.Fatalf("invalid backend mode failure = %#v", failure)
	}
	if failure := (Action{
		Kind:        ActionNavigate,
		URL:         "https://example.com",
		BackendMode: "isolated",
	}).Validate(); failure == nil || failure.Code != ErrorProtocolInvalid {
		t.Fatalf("non-preflight backend mode failure = %#v", failure)
	}
}

func TestObservationModeDefaultsToSemanticAndRejectsUnknownValues(t *testing.T) {
	t.Parallel()
	if got := ObservationDefault.Effective(); got != ObservationSemantic {
		t.Fatalf("default observation = %q, want %q", got, ObservationSemantic)
	}
	for _, mode := range []ObservationMode{
		ObservationDefault,
		ObservationSemantic,
		ObservationScreenshot,
		ObservationBoth,
		ObservationNone,
	} {
		if failure := mode.Validate(); failure != nil {
			t.Errorf("Validate(%q) failure = %v", mode, failure)
		}
	}
	if failure := ObservationMode("verbose").Validate(); failure == nil {
		t.Fatal("unknown observation mode was accepted")
	}
}

func TestErrorResponseBoundsUntrustedRequestID(t *testing.T) {
	t.Parallel()
	response := ErrorResponse(strings.Repeat("x", 1024), NewFailure(ErrorProtocolInvalid, "bad", false))
	if len(response.RequestID) != 128 {
		t.Fatalf("request ID length = %d, want 128", len(response.RequestID))
	}
}

func TestValidateFailureBoundsBatchActionIndex(t *testing.T) {
	t.Parallel()
	validIndex := 7
	valid := NewFailure(ErrorRuntimeUnavailable, "failed", true)
	valid.ActionIndex = &validIndex
	if failure := ValidateFailure(valid); failure != nil {
		t.Fatalf("valid failure rejected: %v", failure)
	}
	for _, index := range []int{-1, 8} {
		invalid := NewFailure(ErrorRuntimeUnavailable, "failed", true)
		invalid.ActionIndex = &index
		if failure := ValidateFailure(invalid); failure == nil ||
			failure.Code != ErrorOutputInvalid {
			t.Errorf("action index %d failure = %#v", index, failure)
		}
	}
}

func TestValidateFailureKeepsViewerReservedButRejectsRemovedRecoveryCode(t *testing.T) {
	t.Parallel()
	if failure := ValidateFailure(
		NewFailure(ErrorViewerUnavailable, "viewer is unavailable", true),
	); failure != nil {
		t.Fatalf("reserved Viewer failure rejected: %v", failure)
	}
	if failure := ValidateFailure(
		NewFailure(
			ErrorCode("BROWSER_CONVERSATION_RECOVERY_FAILED"),
			"removed conversation recovery state",
			false,
		),
	); failure == nil || failure.Code != ErrorOutputInvalid {
		t.Fatalf("removed recovery code failure = %#v", failure)
	}
}

func TestValidateFailureRegistersRetryCodesAndBoundsTargetCategory(t *testing.T) {
	t.Parallel()
	if failure := ValidateFailure(
		NewFailure(ErrorCloseRetryExhausted, "budget exhausted", false),
	); failure != nil {
		t.Fatalf("%s was not registered: %v", ErrorCloseRetryExhausted, failure)
	}
	blocked := NewFailure(
		ErrorHighImpactActionBlocked,
		"blocked",
		false,
	)
	blocked.TargetCategory = TargetCategoryButton
	blocked.PageStateID = "state-1"
	blocked.NavigationGeneration = 1
	if failure := ValidateFailure(blocked); failure != nil {
		t.Fatalf("valid target category rejected: %v", failure)
	}
	missingCategory := NewFailure(
		ErrorHighImpactActionBlocked,
		"blocked",
		false,
	)
	missingCategory.PageStateID = "state-1"
	missingCategory.NavigationGeneration = 1
	if failure := ValidateFailure(missingCategory); failure == nil ||
		failure.Code != ErrorOutputInvalid {
		t.Fatalf("missing target category failure = %#v", failure)
	}
	blocked.TargetCategory = TargetCategory("page-controlled")
	if failure := ValidateFailure(blocked); failure == nil ||
		failure.Code != ErrorOutputInvalid {
		t.Fatalf("unknown target category failure = %#v", failure)
	}
	navigationRemaining := 0
	runRemaining := 8
	exhausted := NewFailure(
		ErrorClickRetryExhausted,
		"budget exhausted",
		false,
	)
	exhausted.TargetCategory = TargetCategoryButton
	exhausted.PageStateID = "state-1"
	exhausted.NavigationGeneration = 1
	exhausted.BlockedClickNavigationAttemptsRemaining = &navigationRemaining
	exhausted.BlockedClickRunAttemptsRemaining = &runRemaining
	if failure := ValidateFailure(exhausted); failure != nil {
		t.Fatalf("%s was not registered: %v", ErrorClickRetryExhausted, failure)
	}
	unrelated := NewFailure(ErrorRuntimeUnavailable, "failed", true)
	unrelated.TargetCategory = TargetCategoryButton
	unrelated.PageStateID = "state-1"
	unrelated.NavigationGeneration = 1
	if failure := ValidateFailure(unrelated); failure == nil ||
		failure.Code != ErrorOutputInvalid {
		t.Fatalf("unrelated click evidence failure = %#v", failure)
	}
}

func TestValidateFailureAcceptsOnlyBoundedSiteEvidence(t *testing.T) {
	t.Parallel()
	retryAfter := 30_000
	denials := 3
	valid := []*Failure{
		func() *Failure {
			failure := NewFailure(ErrorOriginRateLimited, "budget exhausted", true)
			failure.SiteOutcome = ErrorOriginRateLimited
			failure.RetryAfterMS = &retryAfter
			failure.ChallengeReleaseUnavailable = true
			return failure
		}(),
		func() *Failure {
			failure := NewFailure(ErrorRuntimeUnavailable, "runtime unavailable", true)
			failure.ChallengeReleaseUnavailable = true
			return failure
		}(),
		func() *Failure {
			failure := NewFailure(ErrorAccessDenied, "access denied", true)
			failure.SiteOutcome = ErrorAccessDenied
			failure.ConsecutiveAccessDenials = &denials
			failure.OriginBlockedForAttachment = true
			return failure
		}(),
		func() *Failure {
			failure := NewFailure(ErrorRateLimited, "rate limited", true)
			failure.SiteOutcome = ErrorRateLimited
			failure.RetryAfterMS = &retryAfter
			return failure
		}(),
		func() *Failure {
			failure := NewFailure(ErrorChallengeSuspected, "challenge suspected", true)
			failure.SiteOutcome = ErrorChallengeSuspected
			failure.ClassifierRulesVersion = ChallengeClassifierRulesVersion
			failure.ChallengeReleaseUnavailable = true
			return failure
		}(),
		func() *Failure {
			failure := NewFailure(ErrorChallengeRequired, "challenge required", false)
			failure.SiteOutcome = ErrorChallengeRequired
			failure.ClassifierRulesVersion = ChallengeClassifierRulesVersion
			return failure
		}(),
	}
	for _, failure := range valid {
		if validation := ValidateFailure(failure); validation != nil {
			t.Errorf("valid site failure rejected: %#v: %v", failure, validation)
		}
	}

	mismatch := NewFailure(ErrorRuntimeUnavailable, "wrong code", true)
	mismatch.SiteOutcome = ErrorAccessDenied
	if failure := ValidateFailure(mismatch); failure == nil ||
		failure.Code != ErrorOutputInvalid {
		t.Fatalf("mismatched site outcome failure = %#v", failure)
	}
	oversizedRetry := 15*60*1000 + 1
	invalidRetry := NewFailure(ErrorRateLimited, "rate limited", true)
	invalidRetry.SiteOutcome = ErrorRateLimited
	invalidRetry.RetryAfterMS = &oversizedRetry
	if failure := ValidateFailure(invalidRetry); failure == nil ||
		failure.Code != ErrorOutputInvalid {
		t.Fatalf("oversized retry failure = %#v", failure)
	}
}

func validRequest(now time.Time) Request {
	return Request{
		ContractID:        ContractID,
		ChannelCredential: strings.Repeat("a", 64),
		RequestID:         "77777777-7777-4777-8777-777777777777",
		Deadline:          now.Add(30 * time.Second),
		Identity: Identity{
			RunID:                              "11111111-1111-4111-8111-111111111111",
			AgentID:                            "22222222-2222-4222-8222-222222222222",
			PrincipalScopeID:                   "scope_333333333333",
			BrowserSessionID:                   "44444444-4444-4444-8444-444444444444",
			SessionEpoch:                       1,
			AttachmentID:                       "55555555-5555-4555-8555-555555555555",
			ControlEpoch:                       1,
			Controller:                         ControllerAgent,
			BrowserInteractionPolicy:           "restricted",
			BrowserInteractionPolicyGeneration: 1,
			BrowserMutationOrigins:             []string{},
			BrowserMutationOriginsSHA256:       RestrictedMutationOriginsSHA256,
		},
		Action: Action{Kind: ActionScreenshot},
	}
}
