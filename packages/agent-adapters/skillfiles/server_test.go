package skillfiles

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The manifest is what the Host materialized for the Run. Other files on disk
// (an interrupted temporary write, or a file planted in a shared same-UID
// workspace) must never be served, whatever their names look like.
var manifest = []string{"SKILL.md", "references/proof.txt", "references/deploy.pending-review.md"}

func packageFixture(t *testing.T) (string, []string, string) {
	t.Helper()
	base := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(base); err == nil {
		base = resolved
	}
	root := filepath.Join(base, "agent", "digest")
	for name, content := range map[string]string{
		"SKILL.md":                             "---\nname: proof\ndescription: proof\n---\nread references/proof.txt\n",
		"references/proof.txt":                 "PROOF-7c1e",
		"references/deploy.pending-review.md":  "REVIEW-9a2b",
		"references/proof.txt.pending-0123abc": "partial",
		"references/planted.md":                "PLANTED",
		".gitignore":                           "*\n",
	} {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o400); err != nil {
			t.Fatal(err)
		}
	}
	secret := filepath.Join(base, "runtime-secret")
	if err := os.WriteFile(secret, []byte("SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	files := make([]string, 0, len(manifest))
	for _, name := range manifest {
		files = append(files, filepath.Join(root, filepath.FromSlash(name)))
	}
	return root, files, secret
}

func TestReadServesExactlyThePinnedManifest(t *testing.T) {
	root, files, secret := packageFixture(t)
	server, err := New("codex", "test", []string{root}, files)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"references/proof.txt":                "PROOF-7c1e",
		"references/deploy.pending-review.md": "REVIEW-9a2b",
	} {
		if text, err := server.Read(filepath.Join(root, filepath.FromSlash(name))); err != nil || text != want {
			t.Fatalf("manifest file %s: %q %v", name, text, err)
		}
	}
	listing, err := server.Read(root)
	if err != nil || !strings.Contains(listing, "SKILL.md") || !strings.Contains(listing, "references/deploy.pending-review.md") ||
		strings.Contains(listing, "planted") || strings.Contains(listing, "0123abc") || strings.Contains(listing, ".gitignore") {
		t.Fatalf("package listing: %q %v", listing, err)
	}
	if sub, err := server.Read(filepath.Join(root, "references")); err != nil || !strings.Contains(sub, "proof.txt") || strings.Contains(sub, "planted") {
		t.Fatalf("subdirectory listing: %q %v", sub, err)
	}
	for _, name := range []string{
		secret,
		filepath.Join(root, "..", "..", "runtime-secret"),
		filepath.Join(root, ".gitignore"),
		filepath.Join(root, "references", "proof.txt.pending-0123abc"),
		filepath.Join(root, "references", "planted.md"),
		"references/proof.txt",
		filepath.Join(root, "missing.md"),
	} {
		if text, err := server.Read(name); err == nil {
			t.Fatalf("%s must not be readable, got %q", name, text)
		}
	}
	if runtime.GOOS != "windows" {
		// A manifest entry replaced by a symlink must not reach outside the package.
		target := filepath.Join(root, "references", "proof.txt")
		if err := os.Chmod(filepath.Dir(target), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(target); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(secret, target); err != nil {
			t.Fatal(err)
		}
		if text, err := server.Read(target); err == nil {
			t.Fatalf("symlink escape was readable: %q", text)
		}
	}
}

func TestServerRequiresHostChosenRootsAndManifests(t *testing.T) {
	root, files, secret := packageFixture(t)
	for _, roots := range [][]string{nil, {"relative"}, {root + "/../digest"}, {filepath.Join(root, "missing")}} {
		if _, err := New("codex", "test", roots, files); err == nil {
			t.Fatalf("accepted roots %v", roots)
		}
	}
	for _, bad := range [][]string{nil, {secret}, {root}, {filepath.Join(root, ".gitignore")}, {"SKILL.md"}} {
		if _, err := New("codex", "test", []string{root}, bad); err == nil {
			t.Fatalf("accepted manifest %v", bad)
		}
	}
	if _, err := New("browser", "test", []string{root}, files); err == nil {
		t.Fatal("accepted an unknown host")
	}
}

func TestServeExposesOnlyTheReadTool(t *testing.T) {
	root, files, secret := packageFixture(t)
	server, err := New("claude", "test", []string{root}, files)
	if err != nil {
		t.Fatal(err)
	}
	call := func(id int, path string) string {
		raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/call", "params": map[string]any{"name": ToolName, "arguments": map[string]any{"path": path}}})
		return string(raw)
	}
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		call(3, filepath.Join(root, "references", "proof.txt")),
		call(4, secret),
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"shell","arguments":{"path":"/"}}}`,
	}, "\n")
	var output bytes.Buffer
	if err := server.Serve(context.Background(), strings.NewReader(input), &output); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 5 {
		t.Fatalf("expected five responses, got %d: %s", len(lines), output.String())
	}
	var tools struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &tools); err != nil || len(tools.Result.Tools) != 1 || tools.Result.Tools[0].Name != ToolName {
		t.Fatalf("tools/list: %s", lines[1])
	}
	if !strings.Contains(lines[2], "PROOF-7c1e") || strings.Contains(lines[2], `"isError":true`) {
		t.Fatalf("read response: %s", lines[2])
	}
	if strings.Contains(lines[3], "SECRET") || !strings.Contains(lines[3], `"isError":true`) {
		t.Fatalf("outside read response: %s", lines[3])
	}
	if !strings.Contains(lines[4], "-32602") {
		t.Fatalf("unknown tool response: %s", lines[4])
	}
}

// Two packages with identical content share one digest directory. The Host
// passes the same root and manifest twice; the server must deduplicate before
// applying the per-package file limit.
func TestServerAcceptsIdenticalPackagesSharingOneDirectory(t *testing.T) {
	base := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(base); err == nil {
		base = resolved
	}
	root := filepath.Join(base, "agent", "digest")
	var files []string
	for i := 0; i < 32; i++ {
		name := "SKILL.md"
		if i > 0 {
			name = filepath.Join("references", fmt.Sprintf("file-%02d.md", i))
		}
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(name), 0o400); err != nil {
			t.Fatal(err)
		}
		files = append(files, path)
	}
	server, err := New("codex", "test", []string{root, root}, append(append([]string{}, files...), files...))
	if err != nil {
		t.Fatalf("identical packages sharing a directory were rejected: %v", err)
	}
	if text, err := server.Read(files[31]); err != nil || !strings.Contains(text, "file-31") {
		t.Fatalf("shared package file: %q %v", text, err)
	}
	extra := filepath.Join(root, "references", "file-32.md")
	if _, err := New("codex", "test", []string{root}, append(append([]string{}, files...), extra)); err == nil {
		t.Fatal("a package directory with 33 distinct files was accepted")
	}
}
