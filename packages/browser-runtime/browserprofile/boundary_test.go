package browserprofile

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

var forbiddenExportedCryptoPrimitives = map[string]struct{}{
	"Decrypt":         {},
	"Encrypt":         {},
	"MarshalMetadata": {},
	"NewProtector":    {},
	"ParseMetadata":   {},
	"PayloadCipher":   {},
	"Protector":       {},
	"Rewrap":          {},
}

func TestBrowserProfileDoesNotDependOnProviderAgentOrCorePackages(t *testing.T) {
	t.Parallel()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve browserprofile package path")
	}
	dir := filepath.Dir(currentFile)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") ||
			strings.HasSuffix(entry.Name(), "_test.go") {
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
				t.Errorf("%s imports forbidden dependency %q", entry.Name(), importPath)
			}
		}
	}
}

func TestBrowserProfileDoesNotExportCryptoPrimitives(t *testing.T) {
	t.Parallel()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve browserprofile package path")
	}
	dir := filepath.Dir(currentFile)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") ||
			strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			switch declaration := node.(type) {
			case *ast.TypeSpec:
				if _, forbidden := forbiddenExportedCryptoPrimitives[declaration.Name.Name]; forbidden {
					t.Errorf("%s exports forbidden crypto type %s", entry.Name(), declaration.Name.Name)
				}
			case *ast.FuncDecl:
				if _, forbidden := forbiddenExportedCryptoPrimitives[declaration.Name.Name]; forbidden {
					t.Errorf("%s exports forbidden crypto operation %s", entry.Name(), declaration.Name.Name)
				}
			}
			return true
		})
	}
}
