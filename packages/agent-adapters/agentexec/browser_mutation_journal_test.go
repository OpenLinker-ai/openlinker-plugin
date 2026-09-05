package agentexec

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserprotocol"
)

func TestBrowserMutationJournalIsBoundedAndNeverStoresActionText(t *testing.T) {
	t.Parallel()
	journal := newBrowserMutationJournal(browserprotocol.Identity{
		BrowserMutationOriginsSHA256: strings.Repeat("a", 64),
	})
	observation := browserprotocol.Observation{
		PageStateID:          "page-before",
		NavigationGeneration: 1,
		Origin:               "https://public.example",
		TargetCategory:       browserprotocol.TargetCategoryTextInput,
	}
	journal.record(
		browserprotocol.Action{Kind: browserprotocol.ActionScreenshot},
		observation,
		nil,
		time.Unix(1, 0).UTC(),
	)
	for range 300 {
		journal.record(
			browserprotocol.Action{
				Kind: browserprotocol.ActionTypeNonSecret,
				Text: "SHOULD_NEVER_ENTER_THE_JOURNAL",
			},
			observation,
			nil,
			time.Unix(2, 0).UTC(),
		)
	}
	journal.mu.Lock()
	encoded, err := json.Marshal(journal.entries)
	entries := len(journal.entries)
	encodedBytes := journal.encodedBytes
	dropped := journal.dropped
	journal.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if entries != maxBrowserMutationJournalEntries || dropped != 44 ||
		encodedBytes > maxBrowserMutationJournalBytes {
		t.Fatalf(
			"journal bounds entries=%d dropped=%d bytes=%d",
			entries,
			dropped,
			encodedBytes,
		)
	}
	if strings.Contains(string(encoded), "SHOULD_NEVER_ENTER_THE_JOURNAL") {
		t.Fatal("journal retained Browser action text")
	}
	summary := journal.summary("success")["browser_mutation_summary"].(map[string]any)
	if summary["completed"] != 300 || summary["journal_entries"] != 256 ||
		summary["journal_entries_dropped"] != 44 {
		t.Fatalf("journal summary = %#v", summary)
	}
}

func TestBrowserMutationJournalRecordsUnknownAndExactBatchPrefix(t *testing.T) {
	t.Parallel()
	journal := newBrowserMutationJournal(browserprotocol.Identity{
		BrowserMutationOriginsSHA256: strings.Repeat("b", 64),
	})
	completed := 1
	failedIndex := 1
	falseValue, trueValue := false, true
	failure := browserprotocol.NewFailure(
		browserprotocol.ErrorMutationOutcomeUnknown,
		"mutation outcome unknown",
		false,
	)
	failure.ActionIndex = &failedIndex
	failure.CompletedActions = &completed
	failure.RetrySameAction = &falseValue
	failure.AttachmentUsable = &trueValue
	failure.FreshObservationRequired = &trueValue
	failure.MutationOutcomeReason = "click_dispatch_uncertain"
	journal.record(
		browserprotocol.Action{
			Kind: browserprotocol.ActionBatch,
			Actions: []browserprotocol.Action{
				{Kind: browserprotocol.ActionClick},
				{Kind: browserprotocol.ActionClick},
				{Kind: browserprotocol.ActionClick},
			},
		},
		browserprotocol.Observation{},
		failure,
		time.Unix(3, 0).UTC(),
	)
	journal.mu.Lock()
	defer journal.mu.Unlock()
	if len(journal.entries) != 2 ||
		journal.entries[0].Result != "completed" ||
		journal.entries[1].Result != "outcome_unknown" ||
		journal.unknown != 1 || journal.completed != 1 {
		t.Fatalf("batch journal = %#v", journal.entries)
	}
}

func TestBrowserMutationJournalIncludesPageDrivenMutationRequests(t *testing.T) {
	t.Parallel()
	journal := newBrowserMutationJournal(browserprotocol.Identity{
		BrowserMutationOriginsSHA256: strings.Repeat("c", 64),
	})
	journal.record(
		browserprotocol.Action{Kind: browserprotocol.ActionWait},
		browserprotocol.Observation{
			PageStateID:              "page-after-request",
			NavigationGeneration:     1,
			Origin:                   "https://public.example",
			MutationRequestsObserved: 2,
		},
		nil,
		time.Unix(4, 0).UTC(),
	)
	journal.mu.Lock()
	defer journal.mu.Unlock()
	if len(journal.entries) != 1 ||
		journal.entries[0].ActionKind != browserprotocol.ActionWait ||
		journal.completed != 1 || journal.mutationRequests != 2 {
		t.Fatalf("page-driven mutation journal = %#v", journal.entries)
	}
}
