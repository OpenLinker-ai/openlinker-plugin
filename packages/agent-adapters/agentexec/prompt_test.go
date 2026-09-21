package agentexec

import (
	"strings"
	"testing"
)

// A request to open a named page has to go through the Browser. Answering it from
// search results looks like a success while no page was visited and the
// observation the user is watching stays empty, which is the behaviour the
// 2026-09-20 diagnosis found unexplained by any mechanism.
func TestBrowserPromptBindsNamedPagesToTheBrowser(t *testing.T) {
	t.Parallel()
	run := RunContext{RunID: "run-1", Input: map[string]any{"text": "open example.com"}}
	enabled := buildPrompt("provider-a", run, true)
	for _, expected := range []string{
		"navigate there with the Browser tool",
		"Search results about a page are not a substitute for opening it",
		"on the basis of a Browser observation from this run",
		"say which page you could not reach and why",
	} {
		if !strings.Contains(enabled, expected) {
			t.Fatalf("Browser prompt missing %q: %s", expected, enabled)
		}
	}

	// The rule only exists where the tool does. A Run without the Browser profile
	// must not be told to navigate with a tool it does not have.
	disabled := buildPrompt("provider-a", run, false)
	for _, forbidden := range []string{
		"navigate there with the Browser tool",
		"Search results about a page are not a substitute",
	} {
		if strings.Contains(disabled, forbidden) {
			t.Fatalf("prompt without the Browser profile advertised %q: %s", forbidden, disabled)
		}
	}

	// Every provider shares one prompt builder, so the rule cannot apply to only one.
	if !strings.Contains(buildPrompt("provider-b", run, true), "navigate there with the Browser tool") {
		t.Fatal("a second provider's prompt does not carry the Browser navigation rule")
	}
}
