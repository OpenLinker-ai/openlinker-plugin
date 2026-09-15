package agent

import "strings"

// WebSearchPolicy describes configuration, not model/gateway tool availability.
// Source is a fixed configuration key; it never contains an environment value.
type WebSearchPolicy struct {
	Provider   string `json:"provider"`
	Configured bool   `json:"configured"`
	Effective  bool   `json:"effective"`
	Source     string `json:"source"`
	Overridden bool   `json:"overridden"`
}

func resolveWebSearchPolicy(config Config, getenv func(string) string) (WebSearchPolicy, error) {
	policy := WebSearchPolicy{Provider: config.Provider, Configured: config.WebSearch, Effective: config.WebSearch, Source: "configuration"}
	name := "OPENLINKER_AGENT_WEB_SEARCH"
	if config.Provider == "codex" || config.Provider == "claude" {
		name = "OPENLINKER_" + strings.ToUpper(config.Provider) + "_WEB_SEARCH"
	} else {
		policy.Provider = "unknown"
	}
	if strings.TrimSpace(envValue(getenv, name)) == "" {
		name = "OPENLINKER_AGENT_WEB_SEARCH"
	}
	if strings.TrimSpace(envValue(getenv, name)) != "" {
		policy.Source = name
		if err := applyBoolean(getenv, name, &policy.Effective); err != nil {
			return WebSearchPolicy{}, err
		}
	}
	policy.Overridden = policy.Configured != policy.Effective
	return policy, nil
}

// SearchPolicy returns the snapshot captured when configure/runtime options were
// resolved, rather than re-reading the caller's potentially different environment.
func (config Config) SearchPolicy() *WebSearchPolicy {
	if config.webSearchPolicy == nil {
		return nil
	}
	policy := *config.webSearchPolicy
	return &policy
}
