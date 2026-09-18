package agentexec

import "testing"

// Claude runs with dontAsk, so search is only usable when the two web tools are
// allowed; turning search off must still deny them even if a caller allowed them.
func TestClaudeWebSearchGrantsExactlyTheWebTools(t *testing.T) {
	for _, test := range []struct {
		name       string
		webSearch  bool
		allowed    []string
		wantAllow  string
		wantDenied bool
	}{
		{name: "on", webSearch: true, allowed: []string{"Read"}, wantAllow: "Read,WebSearch,WebFetch"},
		{name: "on-without-other-tools", webSearch: true, wantAllow: "WebSearch,WebFetch"},
		{name: "on-keeps-existing-grant-once", webSearch: true, allowed: []string{"WebSearch", "Read"}, wantAllow: "WebSearch,Read,WebFetch"},
		{name: "off", allowed: []string{"Read"}, wantAllow: "Read", wantDenied: true},
		{name: "off-overrides-caller-grant", allowed: []string{"WebSearch", "WebFetch"}, wantAllow: "WebSearch,WebFetch", wantDenied: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			args := claudeArguments(ProviderConfig{Provider: "claude", WebSearch: test.webSearch, AllowedTools: test.allowed}, "dontAsk", "")
			if got := flagValue(args, "--allowedTools"); got != test.wantAllow {
				t.Fatalf("--allowedTools = %q, want %q: %q", got, test.wantAllow, args)
			}
			if denied := flagValue(args, "--disallowedTools") == "WebSearch,WebFetch"; denied != test.wantDenied {
				t.Fatalf("web tools denied = %t, want %t: %q", denied, test.wantDenied, args)
			}
			if flagValue(args, "--permission-mode") != "dontAsk" {
				t.Fatalf("search must not change the permission mode: %q", args)
			}
		})
	}
}

func flagValue(args []string, name string) string {
	for index, arg := range args {
		if arg == name && index+1 < len(args) {
			return args[index+1]
		}
	}
	return ""
}
