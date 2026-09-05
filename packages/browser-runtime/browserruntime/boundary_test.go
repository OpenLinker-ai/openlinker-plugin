//go:build !windows

package browserruntime

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestBrowserRuntimeDoesNotDependOnPersistedRuntimeEventPackages(t *testing.T) {
	t.Parallel()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve browserruntime package path")
	}
	dir := filepath.Dir(currentFile)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, spec := range file.Imports {
			importPath, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(importPath, "/packages/agent-adapters") ||
				strings.Contains(importPath, "openlinker-cli") ||
				strings.Contains(importPath, "openlinker-go") ||
				strings.Contains(importPath, "openlinker-core") {
				t.Errorf("%s imports persisted Runtime dependency %q", entry.Name(), importPath)
			}
		}
	}
}
