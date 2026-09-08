package agentexec

import (
	"context"
	"fmt"
	"github.com/OpenLinker-ai/openlinker-plugin/packages/agent-adapters/codexrpc"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProviderPreflightRejectsVersionsAndMissingFlags(t *testing.T) {
	flags := "app-server generate-json-schema --listen --config --disable"
	for _, test := range []struct {
		version, help string
		ok            bool
	}{
		{"codex-cli 0.153.0", flags, true},
		{"codex-cli 0.152.9", flags, false},
		{"codex-cli 0.154.0-alpha.1", flags, false},
		{"codex-cli 1.0.0", flags, true},
		{"codex-cli 0.153.0", "--json", false},
		{"other-cli 0.153.0", flags, false},
	} {
		bin, _ := reviewFakeCLI(t, "if [ \"$1\" = --version ]; then printf '%s\\n' '"+test.version+"'; elif [ \"$*\" = 'app-server --help' ]; then printf '%s\\n' '"+test.help+"'; else printf '%s\\n' '"+test.help+"'; fi\n")
		_, err := CheckProviderCLI(context.Background(), ProviderConfig{Provider: "codex", Bin: bin})
		if (err == nil) != test.ok {
			t.Fatalf("version=%s help=%s: %v", test.version, test.help, err)
		}
	}
}

func TestCodexPreflightRequiresAppServerAfterVersion(t *testing.T) {
	for _, test := range []struct {
		name, help string
		exit       int
		ok         bool
	}{
		{"supported", "Usage: codex app-server; generate-json-schema --listen --config --disable", 0, true},
		{"absent", "unknown command", 2, false},
		{"empty wrapper response", "", 0, false},
		{"missing schema generator", "Usage: codex app-server", 0, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			log := filepath.Join(t.TempDir(), "probes")
			bin, _ := reviewFakeCLI(t, fmt.Sprintf(`printf '%%s\n' "$*" >> '%s'
case "$*" in
--version) printf '%%s\n' 'codex-cli 0.153.0' ;;
'app-server --help') printf '%%s\n' '%s'; exit %d ;;
'exec --help') printf '%%s\n' '--json --ephemeral --ignore-user-config --ignore-rules --sandbox --disable --dangerously-bypass-approvals-and-sandbox' ;;
*) exit 99 ;;
esac
`, log, test.help, test.exit))
			_, err := CheckProviderCLI(context.Background(), ProviderConfig{Provider: "codex", Bin: bin})
			if (err == nil) != test.ok {
				t.Fatalf("compatibility result: %v", err)
			}
			if err != nil && !strings.Contains(err.Error(), "app-server") {
				t.Fatalf("missing actionable diagnostic: %v", err)
			}
			probes, readErr := os.ReadFile(log)
			if readErr != nil {
				t.Fatal(readErr)
			}
			want := "--version\napp-server --help\n"
			if string(probes) != want {
				t.Fatalf("probes=%q; want=%q", probes, want)
			}
		})
	}
}

func TestInstalledProviderPreflight(t *testing.T) {
	if os.Getenv("OPENLINKER_TEST_INSTALLED_PROVIDER_CLI") != "1" {
		t.Skip("opt-in read-only installed CLI probes")
	}
	for _, provider := range []string{"codex", "claude"} {
		version, err := CheckProviderCLI(context.Background(), ProviderConfig{Provider: provider})
		if err != nil {
			t.Fatal(err)
		}
		t.Log(provider, version)
	}
}

func TestNativeCodexIsolatesConfigAndEnablesOnlyDeclaredPlugin(t *testing.T) {
	config := ProviderConfig{ExecutionProfile: "browser", BrowserClientMode: "native", BrowserNativePlugin: "/trusted/runtime-marketplace"}
	{
		args := strings.Join(codexAppServerArguments(config, "/workspace", "danger-full-access"), " ")
		for _, expected := range []string{`projects={"/workspace"={trust_level="untrusted"}}`, `marketplaces.openlinker-agent-runtime.source="/trusted/runtime-marketplace"`, `plugins={"openlinker@openlinker-agent-runtime"={enabled=true`} {
			if !strings.Contains(args, expected) {
				t.Fatalf("missing %s: %s", expected, args)
			}
		}
	}
}

func TestCodexBaselineMatchesGeneratedProtocol(t *testing.T) {
	if MinimumCodexVersion != codexrpc.ProtocolVersion {
		t.Fatal("Codex compatibility baseline and generated protocol differ")
	}
}
