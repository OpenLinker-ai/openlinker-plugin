package agent

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	openlinker "github.com/OpenLinker-ai/openlinker-go"
)

// PrepareCredential reads the same non-secret identity configuration as Agent
// mode, then delegates offline persistence to the SDK without starting a Provider.
func PrepareCredential(getenv func(string) string, options openlinker.RuntimeCredentialPreparation) (openlinker.PreparedRuntimeCredential, error) {
	config, _, err := loadConfig(getenv)
	if err != nil {
		return openlinker.PreparedRuntimeCredential{}, err
	}
	agentID := firstNonEmpty(envValue(getenv, "OPENLINKER_AGENT_ID"), config.AgentID)
	if !validUUID(agentID) || agentID != options.AgentID {
		return openlinker.PreparedRuntimeCredential{}, errors.New("credential preparation Agent ID differs from installation configuration")
	}
	dir, err := stateDir(config, getenv)
	if err != nil {
		return openlinker.PreparedRuntimeCredential{}, err
	}
	dataDir, err := filepath.Abs(options.DataDir)
	if err != nil || dataDir != filepath.Join(dir, "runtime") {
		return openlinker.PreparedRuntimeCredential{}, errors.New("credential preparation data directory differs from installation configuration")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "node-id"))
	if err != nil {
		return openlinker.PreparedRuntimeCredential{}, err
	}
	options.ConfiguredNodeID = strings.TrimSpace(string(raw))
	if explicit := envValue(getenv, "OPENLINKER_NODE_ID"); explicit != "" && explicit != options.ConfiguredNodeID {
		return openlinker.PreparedRuntimeCredential{}, errors.New("configured and persisted Node IDs differ")
	}
	return openlinker.PrepareRuntimeCredential(options)
}
