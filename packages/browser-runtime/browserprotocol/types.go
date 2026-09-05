package browserprotocol

import (
	"encoding/json"
	"strings"
	"time"
)

const (
	ContractID = "openlinker.browser.v2"

	MaxRequestBytes     = 256 << 10
	MaxResponseBytes    = 8 << 20
	MaxScreenshotBytes  = 4 << 20
	MaxAXTreeBytes      = 1 << 20
	MaxDOMDiffBytes     = 1 << 20
	MaxViewerFrameBytes = 1 << 20
	MaxActionDeadline   = 60 * time.Second
)

type ActionKind string

type ObservationMode string

type Controller string

const (
	ControllerAgent Controller = "agent"
	ControllerNone  Controller = "none"
	ControllerHuman Controller = "human"
)

const (
	ActionNavigate      ActionKind = "navigate"
	ActionClick         ActionKind = "click"
	ActionTypeNonSecret ActionKind = "type_non_secret"
	ActionScroll        ActionKind = "scroll"
	ActionKeypress      ActionKind = "keypress"
	ActionSelect        ActionKind = "select"
	ActionWait          ActionKind = "wait"
	ActionBack          ActionKind = "back"
	ActionForward       ActionKind = "forward"
	ActionScreenshot    ActionKind = "screenshot"
	ActionCheckpoint    ActionKind = "checkpoint"
	ActionClose         ActionKind = "close"
	ActionPreflight     ActionKind = "preflight"
	ActionBatch         ActionKind = "batch"

	ObservationDefault    ObservationMode = ""
	ObservationSemantic   ObservationMode = "semantic"
	ObservationScreenshot ObservationMode = "screenshot"
	ObservationBoth       ObservationMode = "both"
	ObservationNone       ObservationMode = "none"
)

type ErrorCode string

const (
	ErrorProtocolInvalid            ErrorCode = "BROWSER_PROTOCOL_INVALID"
	ErrorRequestTooLarge            ErrorCode = "BROWSER_REQUEST_TOO_LARGE"
	ErrorOutputTooLarge             ErrorCode = "BROWSER_OUTPUT_TOO_LARGE"
	ErrorUnauthorized               ErrorCode = "BROWSER_UNAUTHORIZED"
	ErrorIdentityMismatch           ErrorCode = "BROWSER_IDENTITY_MISMATCH"
	ErrorStaleControlEpoch          ErrorCode = "BROWSER_STALE_CONTROL_EPOCH"
	ErrorRequestReplayed            ErrorCode = "BROWSER_REQUEST_REPLAYED"
	ErrorDeadlineExceeded           ErrorCode = "BROWSER_DEADLINE_EXCEEDED"
	ErrorRuntimeUnavailable         ErrorCode = "BROWSER_RUNTIME_UNAVAILABLE"
	ErrorEngineUnavailable          ErrorCode = "BROWSER_ENGINE_UNAVAILABLE"
	ErrorEgressUnavailable          ErrorCode = "BROWSER_EGRESS_UNAVAILABLE"
	ErrorTargetBlocked              ErrorCode = "BROWSER_TARGET_BLOCKED"
	ErrorProfileLocked              ErrorCode = "BROWSER_PROFILE_LOCKED"
	ErrorProfileCorrupt             ErrorCode = "BROWSER_PROFILE_CORRUPT"
	ErrorProfileEnvironmentMismatch ErrorCode = "BROWSER_PROFILE_ENVIRONMENT_MISMATCH"
	ErrorProfileEngineDowngrade     ErrorCode = "BROWSER_PROFILE_ENGINE_DOWNGRADE_UNSUPPORTED"
	ErrorProfileEngineUpgrade       ErrorCode = "BROWSER_PROFILE_ENGINE_UPGRADE_FAILED"
	ErrorUserActionRequired         ErrorCode = "BROWSER_USER_ACTION_REQUIRED"
	ErrorHighImpactActionBlocked    ErrorCode = "BROWSER_HIGH_IMPACT_ACTION_BLOCKED"
	ErrorMutationOriginBlocked      ErrorCode = "BROWSER_MUTATION_ORIGIN_BLOCKED"
	ErrorMutationOutcomeUnknown     ErrorCode = "BROWSER_MUTATION_OUTCOME_UNKNOWN"
	ErrorAccessDenied               ErrorCode = "BROWSER_ACCESS_DENIED"
	ErrorRateLimited                ErrorCode = "BROWSER_RATE_LIMITED"
	ErrorChallengeSuspected         ErrorCode = "BROWSER_CHALLENGE_SUSPECTED"
	ErrorChallengeRequired          ErrorCode = "BROWSER_CHALLENGE_REQUIRED"
	ErrorOriginRateLimited          ErrorCode = "BROWSER_ORIGIN_RATE_LIMITED"
	ErrorViewerUnavailable          ErrorCode = "BROWSER_VIEWER_UNAVAILABLE"
	ErrorActionLimitExceeded        ErrorCode = "BROWSER_ACTION_LIMIT_EXCEEDED"
	ErrorClickRetryExhausted        ErrorCode = "BROWSER_CLICK_RETRY_EXHAUSTED"
	ErrorCloseRetryExhausted        ErrorCode = "BROWSER_CLOSE_RETRY_EXHAUSTED"
	ErrorCanceled                   ErrorCode = "BROWSER_CANCELED"
	ErrorActionRejected             ErrorCode = "BROWSER_ACTION_REJECTED"
	ErrorOutputInvalid              ErrorCode = "BROWSER_OUTPUT_INVALID"
	ErrorInternal                   ErrorCode = "BROWSER_INTERNAL"
)

const ChallengeClassifierRulesVersion = "openlinker.browser.challenge-rules.v1"

type ClickEffect string

const (
	ClickEffectActivated ClickEffect = "activated"
	ClickEffectFocused   ClickEffect = "focused"
)

type TargetCategory string

const (
	TargetCategoryLink      TargetCategory = "link"
	TargetCategoryTextInput TargetCategory = "text_input"
	TargetCategoryButton    TargetCategory = "button"
	TargetCategoryCustom    TargetCategory = "custom"
	TargetCategoryNone      TargetCategory = "none"
	TargetCategoryOther     TargetCategory = "other"
)

type Identity struct {
	RunID                              string     `json:"run_id"`
	AgentID                            string     `json:"agent_id"`
	PrincipalScopeID                   string     `json:"principal_scope_id"`
	BrowserSessionID                   string     `json:"browser_session_id"`
	SessionEpoch                       uint64     `json:"session_epoch"`
	AttachmentID                       string     `json:"attachment_id"`
	ControlEpoch                       uint64     `json:"control_epoch"`
	Controller                         Controller `json:"controller"`
	BrowserInteractionPolicy           string     `json:"browser_interaction_policy"`
	BrowserInteractionPolicyGeneration int64      `json:"browser_interaction_policy_generation"`
	BrowserMutationOrigins             []string   `json:"browser_mutation_origins"`
	BrowserMutationOriginsSHA256       string     `json:"browser_mutation_origins_sha256"`
}

type Request struct {
	ContractID        string    `json:"contract_id"`
	ChannelCredential string    `json:"channel_credential"`
	RequestID         string    `json:"request_id"`
	Deadline          time.Time `json:"deadline"`
	Identity          Identity  `json:"identity"`
	Action            Action    `json:"action"`
}

type Action struct {
	Kind        ActionKind      `json:"kind"`
	Observation ObservationMode `json:"observation,omitempty"`
	BackendMode string          `json:"backend_mode,omitempty"`
	URL         string          `json:"url,omitempty"`
	X           *int            `json:"x,omitempty"`
	Y           *int            `json:"y,omitempty"`
	DeltaX      *int            `json:"delta_x,omitempty"`
	DeltaY      *int            `json:"delta_y,omitempty"`
	Text        string          `json:"text,omitempty"`
	Key         string          `json:"key,omitempty"`
	Value       string          `json:"value,omitempty"`
	DurationMS  *int            `json:"duration_ms,omitempty"`
	Actions     []Action        `json:"actions,omitempty"`
}

type BackendSelectionEvidence struct {
	RequestedMode       string `json:"requested_mode"`
	SelectedBackend     string `json:"selected_backend"`
	FallbackReason      string `json:"fallback_reason,omitempty"`
	AssetManifestSHA256 string `json:"asset_manifest_sha256,omitempty"`
	ExtensionID         string `json:"extension_id,omitempty"`
	ExtensionVersion    string `json:"extension_version,omitempty"`
	NativeHostProtocol  string `json:"native_host_protocol,omitempty"`
	ProfileGeneration   uint64 `json:"profile_generation,omitempty"`
	SessionRecovered    bool   `json:"session_recovered"`
}

type Screenshot struct {
	MIMEType string `json:"mime_type"`
	Data     []byte `json:"data"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
}

type Viewport struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

type EnvironmentEvidence struct {
	BrowserEngine       string `json:"browser_engine"`
	BrowserDistribution string `json:"browser_distribution"`
	BrowserVersion      string `json:"browser_version"`
	BrowserMajorVersion int    `json:"browser_major_version"`
	BrowserLocale       string `json:"browser_locale"`
	BrowserTimezone     string `json:"browser_timezone"`
	FontContractVersion string `json:"font_contract_version"`
	FontManifestSHA256  string `json:"font_manifest_sha256"`
}

type Observation struct {
	PageStateID                 string                    `json:"page_state_id"`
	Viewport                    *Viewport                 `json:"viewport,omitempty"`
	NavigationGeneration        uint64                    `json:"navigation_generation,omitempty"`
	Screenshot                  *Screenshot               `json:"screenshot,omitempty"`
	AXTree                      json.RawMessage           `json:"ax_tree,omitempty"`
	DOMDiff                     json.RawMessage           `json:"dom_diff,omitempty"`
	AXTreeTimedOut              bool                      `json:"ax_tree_timed_out,omitempty"`
	DOMDiffTimedOut             bool                      `json:"dom_diff_timed_out,omitempty"`
	Origin                      string                    `json:"origin,omitempty"`
	Title                       string                    `json:"title,omitempty"`
	ClickEffect                 ClickEffect               `json:"click_effect,omitempty"`
	TargetCategory              TargetCategory            `json:"target_category,omitempty"`
	Environment                 *EnvironmentEvidence      `json:"environment,omitempty"`
	BackendSelection            *BackendSelectionEvidence `json:"backend_selection,omitempty"`
	SiteOutcome                 ErrorCode                 `json:"site_outcome,omitempty"`
	ClassifierRulesVersion      string                    `json:"classifier_rules_version,omitempty"`
	ChallengeReleaseUnavailable bool                      `json:"challenge_release_unavailable,omitempty"`
	BlockedMutationRequests     int                       `json:"blocked_mutation_requests,omitempty"`
	MutationRequestsObserved    int                       `json:"mutation_requests_observed,omitempty"`

	// EngineInstanceID is Runtime-owned process metadata. It never crosses the
	// Browser wire contract and cannot be supplied by Chromium or page content.
	EngineInstanceID uint64 `json:"-"`
}

type Failure struct {
	Code                                    ErrorCode      `json:"code"`
	Message                                 string         `json:"message"`
	Recoverable                             bool           `json:"recoverable"`
	ActionIndex                             *int           `json:"action_index,omitempty"`
	TargetCategory                          TargetCategory `json:"target_category,omitempty"`
	PageStateID                             string         `json:"page_state_id,omitempty"`
	NavigationGeneration                    uint64         `json:"navigation_generation,omitempty"`
	BlockedClickNavigationAttemptsRemaining *int           `json:"blocked_click_navigation_attempts_remaining,omitempty"`
	BlockedClickRunAttemptsRemaining        *int           `json:"blocked_click_run_attempts_remaining,omitempty"`
	SiteOutcome                             ErrorCode      `json:"site_outcome,omitempty"`
	RetryAfterMS                            *int           `json:"retry_after_ms,omitempty"`
	ClassifierRulesVersion                  string         `json:"classifier_rules_version,omitempty"`
	ConsecutiveAccessDenials                *int           `json:"consecutive_access_denials,omitempty"`
	OriginBlockedForAttachment              bool           `json:"origin_blocked_for_attachment,omitempty"`
	ChallengeReleaseUnavailable             bool           `json:"challenge_release_unavailable,omitempty"`
	RetrySameAction                         *bool          `json:"retry_same_action,omitempty"`
	AttachmentUsable                        *bool          `json:"attachment_usable,omitempty"`
	FreshObservationRequired                *bool          `json:"fresh_observation_required,omitempty"`
	MutationOutcomeReason                   string         `json:"mutation_outcome_reason,omitempty"`
	AttemptedUnits                          *int           `json:"attempted_units,omitempty"`
	UndispatchedUnits                       *int           `json:"undispatched_units,omitempty"`
	CompletedActions                        *int           `json:"completed_actions,omitempty"`
	MutationRequestsObserved                int            `json:"mutation_requests_observed,omitempty"`
	ObservedOrigin                          string         `json:"observed_origin,omitempty"`
	BrowserMutationOrigins                  []string       `json:"browser_mutation_origins,omitempty"`
	HumanControlAvailable                   bool           `json:"human_control_available,omitempty"`

	// EngineInstanceID is populated by the trusted Go ProcessEngine after it
	// decodes an Engine response. It is deliberately excluded from JSON.
	EngineInstanceID   uint64 `json:"-"`
	EngineProcessReset bool   `json:"-"`
}

func (failure *Failure) Error() string {
	if failure == nil {
		return ""
	}
	return string(failure.Code) + ": " + failure.Message
}

type Response struct {
	ContractID  string       `json:"contract_id"`
	RequestID   string       `json:"request_id,omitempty"`
	Status      string       `json:"status"`
	Observation *Observation `json:"observation,omitempty"`
	Error       *Failure     `json:"error,omitempty"`
}

func SuccessResponse(requestID string, observation Observation) Response {
	return Response{
		ContractID:  ContractID,
		RequestID:   requestID,
		Status:      "ok",
		Observation: &observation,
	}
}

func ErrorResponse(requestID string, failure *Failure) Response {
	if failure == nil {
		failure = NewFailure(ErrorInternal, "browser runtime failed", false)
	}
	return Response{
		ContractID: ContractID,
		RequestID:  boundedString(requestID, 128),
		Status:     "error",
		Error:      failure,
	}
}

func NewFailure(code ErrorCode, message string, recoverable bool) *Failure {
	return &Failure{
		Code:        code,
		Message:     boundedString(strings.TrimSpace(message), 500),
		Recoverable: recoverable,
	}
}
