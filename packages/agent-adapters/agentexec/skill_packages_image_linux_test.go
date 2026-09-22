//go:build linux

package agentexec

import (
	"context"
	"errors"
	"os"
	"testing"
)

// Run this binary in Dockerfile.providers' filesystem as its Runtime UID, with
// network disabled. No model or registered Agent is involved.
func TestSkillPackagesProviderImageGate(t *testing.T) {
	if os.Getenv("OPENLINKER_TEST_PROVIDER_IMAGE") != "1" {
		t.Skip("requires the Provider image fixture")
	}
	if os.Geteuid() != 10001 || os.Getegid() != 10001 {
		t.Fatal("not running as the image Runtime identity")
	}
	workspace, err := os.Stat("/workspace")
	if err != nil {
		t.Fatal(err)
	}
	if workspace.Mode().Perm() != 0555 {
		t.Fatalf("unexpected image workspace mode: %v", workspace.Mode())
	}
	bin := os.Getenv("OPENLINKER_CODEX_BIN")
	if bin != "/usr/local/bin/openlinker-provider-launcher" {
		t.Fatalf("unexpected launcher %q", bin)
	}
	handler, err := NewHandler(ProviderConfig{Provider: "codex", Bin: bin, Workspace: "/workspace"})
	if err != nil {
		t.Fatal(err)
	}
	if features := handler.SkillPackageFeatures(); len(features) != 0 {
		t.Fatalf("image advertised %v", features)
	}
	_, err = handler.Provider.Run(context.Background(), RunContext{PackageSnapshot: testPackageSnapshot("codex", "private")})
	if !errors.Is(err, ErrSkillPackagesUnsupported) {
		t.Fatalf("must reject before cache write or launcher exec: %v", err)
	}
	if _, err = os.Stat("/workspace/.openlinker-skills"); !os.IsNotExist(err) {
		t.Fatalf("unexpected cache: %v", err)
	}
}
