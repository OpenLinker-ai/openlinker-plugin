//go:build linux

package main

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestRunAgentStageDropsIdentityBeforeExecingRuntimeOwnedTini(t *testing.T) {
	originalDrop := runtimeStageDropIdentity
	originalExec := runtimeStageExec
	originalEffectiveUID := runtimeStageEffectiveUID
	originalEffectiveGID := runtimeStageEffectiveGID
	originalProvider := fixedProvider
	t.Cleanup(func() {
		runtimeStageDropIdentity = originalDrop
		runtimeStageExec = originalExec
		runtimeStageEffectiveUID = originalEffectiveUID
		runtimeStageEffectiveGID = originalEffectiveGID
		fixedProvider = originalProvider
	})

	fixedProvider = "codex"
	runtimeStageEffectiveUID = func() int { return 0 }
	runtimeStageEffectiveGID = func() int { return 0 }
	t.Setenv("OPENLINKER_AGENT_TOKEN", "ol_agent_test")
	t.Setenv("OPENLINKER_AGENT_TOKEN_FILE", "")
	t.Setenv("CODEX_API_KEY", "codex-test")
	t.Setenv("CODEX_API_KEY_FILE", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY_FILE", "")

	order := make([]string, 0, 2)
	runtimeStageDropIdentity = func() error {
		order = append(order, "drop")
		return nil
	}
	sentinel := errors.New("exec intercepted")
	var gotPath string
	var gotArgv, gotEnvironment []string
	runtimeStageExec = func(path string, argv, environment []string) error {
		order = append(order, "exec")
		gotPath = path
		gotArgv = append([]string(nil), argv...)
		gotEnvironment = append([]string(nil), environment...)
		return sentinel
	}

	err := runAgentStage("/usr/local/bin/openlinker-runtime-entrypoint")
	if !errors.Is(err, sentinel) {
		t.Fatalf("runAgentStage error = %v, want intercepted exec", err)
	}
	if !reflect.DeepEqual(order, []string{"drop", "exec"}) {
		t.Fatalf("stage order = %v, want identity drop before exec", order)
	}
	if gotPath != "/usr/bin/tini" {
		t.Fatalf("supervisor path = %q", gotPath)
	}
	if want := []string{
		"/usr/bin/tini",
		"--",
		"/usr/local/bin/openlinker-runtime-entrypoint",
	}; !reflect.DeepEqual(gotArgv, want) {
		t.Fatalf("supervisor argv = %q, want %q", gotArgv, want)
	}
	if !environmentContains(gotEnvironment, entrypointStageEnv+"=1") {
		t.Fatal("supervisor environment is missing the unprivileged Agent stage marker")
	}
}

func environmentContains(environment []string, want string) bool {
	for _, item := range environment {
		if strings.TrimSpace(item) == want {
			return true
		}
	}
	return false
}
