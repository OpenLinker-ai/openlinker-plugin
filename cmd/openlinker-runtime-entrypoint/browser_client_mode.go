package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"strings"

	"github.com/OpenLinker-ai/openlinker-plugin/packages/agent-adapters/browserclientmode"
)

const (
	officialCodexBrowserPlugin  = "/opt/openlinker/agent-runtime-plugin/codex"
	officialClaudeBrowserPlugin = "/opt/openlinker/agent-runtime-plugin/claude"
)

var runBrowserClientHostCommand browserclientmode.RunHostCommand = func(
	args ...string,
) ([]byte, error) {
	command := exec.Command("/usr/local/bin/openlinker-provider-launcher", args...) // #nosec G204 -- fixed binary and internally constructed arguments.
	command.Env = browserClientHostEnvironment(os.Environ())
	var stdout bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &bytes.Buffer{}
	if err := command.Run(); err != nil {
		return nil, err
	}
	if stdout.Len() > 1<<20 {
		return nil, errors.New("Provider Plugin command output exceeded the limit")
	}
	return stdout.Bytes(), nil
}

func browserClientHostEnvironment(environment []string) []string {
	blocked := map[string]struct{}{
		"OPENLINKER_AGENT_TOKEN":      {},
		"OPENLINKER_AGENT_TOKEN_FILE": {},
		"OPENLINKER_USER_TOKEN":       {},
		"CODEX_API_KEY":               {},
		"CODEX_API_KEY_FILE":          {},
		"ANTHROPIC_API_KEY":           {},
		"ANTHROPIC_API_KEY_FILE":      {},
	}
	result := make([]string, 0, len(environment))
	for _, item := range environment {
		key, _, ok := strings.Cut(item, "=")
		if ok {
			if _, skip := blocked[key]; skip {
				continue
			}
		}
		result = append(result, item)
	}
	return result
}

func selectBrowserClientMode(
	provider,
	requested,
	pluginPath string,
	requireImmutable bool,
) (browserclientmode.Selection, error) {
	return browserclientmode.Select(browserclientmode.Options{
		Provider:         provider,
		Requested:        requested,
		PluginPath:       pluginPath,
		RequireImmutable: requireImmutable,
		RunHostCommand:   runBrowserClientHostCommand,
	})
}
