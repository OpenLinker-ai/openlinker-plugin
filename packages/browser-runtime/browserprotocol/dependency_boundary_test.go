package browserprotocol

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Inspect the resolved dependency closure, including platform-selected code and
// test dependencies. A direct-import scan would miss SDK identity dependencies
// hidden behind an otherwise harmless protocol or transport package.
func TestBrowserRuntimeDependencyClosureExcludesWorkerAndCLI(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve Browser Runtime module root")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "..", "..", ".."))
	for _, targetOS := range []string{"linux", "darwin", "windows"} {
		t.Run(targetOS, func(t *testing.T) {
			paths := browserDependencyClosure(t, root, "./packages/browser-runtime/...", targetOS)
			seen := make(map[string]bool, len(paths))
			for _, path := range paths {
				seen[path] = true
				if forbiddenBrowserDependency(path) {
					t.Errorf("Browser Runtime transitively depends on %s", path)
				}
			}
			required := []string{"browserclient", "browserplugin", "browserprofile", "browserprotocol", "egressgateway", "netpolicy"}
			if targetOS != "windows" {
				required = append(required, "browserruntime")
			}
			for _, name := range required {
				path := "github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/" + name
				if !seen[path] {
					t.Errorf("dependency probe did not inspect required package %s", path)
				}
			}
		})
	}
}

func TestBrowserDependencyProbeDetectsIndirectSDKImport(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"go.mod":           "module example.test/browser-boundary\n\ngo 1.23\n\nrequire github.com/OpenLinker-ai/openlinker-go v0.0.0\nreplace github.com/OpenLinker-ai/openlinker-go => ./sdk\n",
		"engine/engine.go": "package engine\nimport _ \"example.test/browser-boundary/bridge\"\n",
		"bridge/bridge.go": "package bridge\nimport _ \"github.com/OpenLinker-ai/openlinker-go\"\n",
		"sdk/go.mod":       "module github.com/OpenLinker-ai/openlinker-go\n\ngo 1.23\n",
		"sdk/sdk.go":       "package openlinker\n",
	}
	for name, content := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	paths := browserDependencyClosure(t, root, "./engine", runtime.GOOS)
	for _, path := range paths {
		if forbiddenBrowserDependency(path) {
			if path != "github.com/OpenLinker-ai/openlinker-go" {
				t.Fatalf("unexpected forbidden dependency: %s", path)
			}
			return
		}
	}
	t.Fatalf("indirect SDK import was not detected: %v", paths)
}

func TestBrowserDependencyBoundaryRejectsEveryForbiddenFamily(t *testing.T) {
	for _, path := range []string{
		"github.com/OpenLinker-ai/openlinker-go",
		"github.com/OpenLinker-ai/openlinker-go/internal/runtimev2",
		"github.com/OpenLinker-ai/openlinker-cli/pkg/shared",
		"github.com/OpenLinker-ai/openlinker-core/pkg/runtime",
		"github.com/OpenLinker-ai/openlinker-plugin/packages/agent-adapters/browserextension",
		"github.com/spf13/cobra",
	} {
		if !forbiddenBrowserDependency(path) {
			t.Errorf("forbidden dependency accepted: %s", path)
		}
	}
	if forbiddenBrowserDependency("github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserprotocol") {
		t.Fatal("Browser protocol must remain usable by the standalone Browser Runtime")
	}
}

func forbiddenBrowserDependency(path string) bool {
	for _, forbidden := range []string{
		"github.com/OpenLinker-ai/openlinker-go",
		"github.com/OpenLinker-ai/openlinker-cli",
		"github.com/OpenLinker-ai/openlinker-core",
		"github.com/OpenLinker-ai/openlinker-plugin/packages/agent-adapters",
		"github.com/spf13/cobra",
	} {
		if path == forbidden || strings.HasPrefix(path, forbidden+"/") {
			return true
		}
	}
	return false
}

func browserDependencyClosure(t *testing.T, root, pattern, targetOS string) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "go", "list", "-mod=readonly", "-deps", "-test", "-json", pattern)
	command.Dir = root
	command.Env = append(os.Environ(),
		"GOWORK=off", "GOTOOLCHAIN=local", "GOPROXY=off", "CGO_ENABLED=0", "GOOS="+targetOS,
	)
	var diagnostic bytes.Buffer
	command.Stderr = &diagnostic
	raw, err := command.Output()
	if err != nil {
		t.Fatalf("resolve %s dependency closure: %v\n%s", targetOS, err, diagnostic.String())
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var paths []string
	for {
		var pkg struct {
			ImportPath string
		}
		if err := decoder.Decode(&pkg); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(fmt.Errorf("decode dependency graph: %w", err))
		}
		if pkg.ImportPath == "" {
			t.Fatal("dependency graph contains an unnamed package")
		}
		paths = append(paths, pkg.ImportPath)
	}
	if len(paths) == 0 {
		t.Fatal("dependency probe returned an empty graph")
	}
	return paths
}
