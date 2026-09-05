package agentexec

import (
	"context"
	"encoding/json"
	"strconv"
	"sync"
	"time"

	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserplugin"
	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserprotocol"
)

const (
	maxBrowserMutationJournalEntries = 256
	maxBrowserMutationJournalBytes   = 128 << 10
)

type browserMutationJournalEntry struct {
	ActionID                   string                         `json:"action_id"`
	Timestamp                  string                         `json:"timestamp"`
	Origin                     string                         `json:"origin_without_query,omitempty"`
	ActionKind                 browserprotocol.ActionKind     `json:"action_kind"`
	TargetCategory             browserprotocol.TargetCategory `json:"coarse_target_category,omitempty"`
	PageStateIDBefore          string                         `json:"page_state_id_before,omitempty"`
	NavigationGenerationBefore uint64                         `json:"navigation_generation_before,omitempty"`
	Result                     string                         `json:"result"`
}

type browserMutationJournal struct {
	mu                  sync.Mutex
	entries             []browserMutationJournalEntry
	encodedBytes        int
	nextActionID        uint64
	lastOrigin          string
	lastPageStateID     string
	lastNavigation      uint64
	completed           int
	failed              int
	unknown             int
	originBlocked       int
	mutationRequests    int
	dropped             int
	mutationOriginsHash string
}

type browserJournalExecutor struct {
	base    browserplugin.Executor
	journal *browserMutationJournal
}

func newBrowserMutationJournal(identity browserprotocol.Identity) *browserMutationJournal {
	return &browserMutationJournal{
		entries:             make([]browserMutationJournalEntry, 0, maxBrowserMutationJournalEntries),
		mutationOriginsHash: identity.BrowserMutationOriginsSHA256,
	}
}

func (journal *browserMutationJournal) wrap(
	executor browserplugin.Executor,
) browserplugin.Executor {
	return &browserJournalExecutor{base: executor, journal: journal}
}

func (executor *browserJournalExecutor) Execute(
	ctx context.Context,
	action browserprotocol.Action,
) (browserprotocol.Observation, *browserprotocol.Failure) {
	observation, failure := executor.base.Execute(ctx, action)
	executor.journal.record(action, observation, failure, time.Now().UTC())
	return observation, failure
}

func (journal *browserMutationJournal) record(
	action browserprotocol.Action,
	observation browserprotocol.Observation,
	failure *browserprotocol.Failure,
	now time.Time,
) {
	if journal == nil {
		return
	}
	journal.mu.Lock()
	defer journal.mu.Unlock()
	actions := []browserprotocol.Action{action}
	if action.Kind == browserprotocol.ActionBatch {
		actions = action.Actions
	}
	completedActions := len(actions)
	if failure != nil {
		completedActions = 0
		if failure.CompletedActions != nil {
			completedActions = *failure.CompletedActions
		} else if failure.ActionIndex != nil {
			completedActions = *failure.ActionIndex
		}
	}
	mutationRequestsObserved := observation.MutationRequestsObserved
	if failure != nil {
		mutationRequestsObserved = failure.MutationRequestsObserved
	}
	journal.mutationRequests += mutationRequestsObserved
	hasNonPotentialNested := false
	for index, nested := range actions {
		isPotential := potentialBrowserMutation(nested.Kind)
		if !isPotential {
			hasNonPotentialNested = true
		}
		observedDuringFailedAction := failure != nil &&
			index == completedActions && mutationRequestsObserved > 0
		observedDuringSingleAction := len(actions) == 1 &&
			mutationRequestsObserved > 0
		if (!isPotential && !observedDuringFailedAction && !observedDuringSingleAction) ||
			(failure != nil && index > completedActions) {
			continue
		}
		result := "completed"
		category := observation.TargetCategory
		if failure != nil && index == completedActions {
			category = failure.TargetCategory
			switch failure.Code {
			case browserprotocol.ErrorMutationOutcomeUnknown:
				result = "outcome_unknown"
				journal.unknown++
			case browserprotocol.ErrorMutationOriginBlocked:
				result = "failed"
				journal.originBlocked++
			default:
				result = "failed"
				journal.failed++
			}
		} else {
			journal.completed++
		}
		journal.nextActionID++
		entry := browserMutationJournalEntry{
			ActionID:                   strconv.FormatUint(journal.nextActionID, 10),
			Timestamp:                  now.Format(time.RFC3339Nano),
			Origin:                     journal.lastOrigin,
			ActionKind:                 nested.Kind,
			TargetCategory:             category,
			PageStateIDBefore:          journal.lastPageStateID,
			NavigationGenerationBefore: journal.lastNavigation,
			Result:                     result,
		}
		journal.appendBounded(entry)
	}
	if action.Kind == browserprotocol.ActionBatch && failure == nil &&
		mutationRequestsObserved > 0 && hasNonPotentialNested {
		journal.completed++
		journal.nextActionID++
		journal.appendBounded(browserMutationJournalEntry{
			ActionID:                   strconv.FormatUint(journal.nextActionID, 10),
			Timestamp:                  now.Format(time.RFC3339Nano),
			Origin:                     journal.lastOrigin,
			ActionKind:                 browserprotocol.ActionBatch,
			PageStateIDBefore:          journal.lastPageStateID,
			NavigationGenerationBefore: journal.lastNavigation,
			Result:                     "completed",
		})
	}
	if failure == nil {
		journal.lastOrigin = observation.Origin
		journal.lastPageStateID = observation.PageStateID
		journal.lastNavigation = observation.NavigationGeneration
	}
}

func (journal *browserMutationJournal) appendBounded(
	entry browserMutationJournalEntry,
) {
	if len(journal.entries) >= maxBrowserMutationJournalEntries {
		journal.dropped++
		return
	}
	encoded, err := json.Marshal(entry)
	if err != nil || journal.encodedBytes+len(encoded)+1 > maxBrowserMutationJournalBytes {
		journal.dropped++
		return
	}
	journal.entries = append(journal.entries, entry)
	journal.encodedBytes += len(encoded) + 1
}

func (journal *browserMutationJournal) summary(status string) map[string]any {
	if journal == nil {
		return nil
	}
	journal.mu.Lock()
	defer journal.mu.Unlock()
	return map[string]any{
		"browser_mutation_summary": map[string]any{
			"completed":                       journal.completed,
			"failed":                          journal.failed,
			"outcome_unknown":                 journal.unknown,
			"origin_blocked":                  journal.originBlocked,
			"mutation_requests_observed":      journal.mutationRequests,
			"journal_entries":                 len(journal.entries),
			"journal_entries_dropped":         journal.dropped,
			"journal_plaintext_bytes":         journal.encodedBytes,
			"browser_mutation_origins_sha256": journal.mutationOriginsHash,
			"terminal_outcome":                status,
		},
	}
}

func potentialBrowserMutation(kind browserprotocol.ActionKind) bool {
	switch kind {
	case browserprotocol.ActionClick,
		browserprotocol.ActionTypeNonSecret,
		browserprotocol.ActionKeypress,
		browserprotocol.ActionSelect:
		return true
	default:
		return false
	}
}
