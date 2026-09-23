package skillfiles

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func packageFixture(t *testing.T) (string, string) {
	t.Helper()
	base := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(base); err == nil {
		base = resolved
	}
	root := filepath.Join(base, "agent", "digest")
	for name, content := range map[string]string{
		"SKILL.md":                     "---\nname: proof\ndescription: proof\n---\nread references/proof.txt\n",
		"references/proof.txt":         "PROOF-7c1e",
		".gitignore":                   "*\n",
		"references/x.md.pending-0123": "partial",
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
	return root, secret
}

func TestReadIsConfinedToPinnedPackageDirectories(t *testing.T) {
	root, secret := packageFixture(t)
	server, err := New("codex", "test", []string{root})
	if err != nil {
		t.Fatal(err)
	}
	if text, err := server.Read(filepath.Join(root, "references", "proof.txt")); err != nil || text != "PROOF-7c1e" {
		t.Fatalf("pinned supporting file: %q %v", text, err)
	}
	listing, err := server.Read(root)
	if err != nil || !strings.Contains(listing, "SKILL.md") || !strings.Contains(listing, "references/proof.txt") ||
		strings.Contains(listing, ".gitignore") || strings.Contains(listing, "pending") {
		t.Fatalf("package listing: %q %v", listing, err)
	}
	for _, name := range []string{
		secret,
		filepath.Join(root, "..", "..", "runtime-secret"),
		filepath.Join(root, ".gitignore"),
		filepath.Join(root, "references", "x.md.pending-0123"),
		"references/proof.txt",
		filepath.Join(root, "missing.md"),
	} {
		if text, err := server.Read(name); err == nil {
			t.Fatalf("%s must not be readable, got %q", name, text)
		}
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(root, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(secret, filepath.Join(root, "escape.md")); err != nil {
			t.Fatal(err)
		}
		if text, err := server.Read(filepath.Join(root, "escape.md")); err == nil {
			t.Fatalf("symlink escape was readable: %q", text)
		}
	}
}

func TestServerRequiresHostChosenRoots(t *testing.T) {
	root, _ := packageFixture(t)
	for _, roots := range [][]string{nil, {"relative"}, {root + "/../digest"}, {filepath.Join(root, "missing")}} {
		if _, err := New("codex", "test", roots); err == nil {
			t.Fatalf("accepted roots %v", roots)
		}
	}
	if _, err := New("browser", "test", []string{root}); err == nil {
		t.Fatal("accepted an unknown host")
	}
}

func TestServeExposesOnlyTheReadTool(t *testing.T) {
	root, secret := packageFixture(t)
	server, err := New("claude", "test", []string{root})
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
