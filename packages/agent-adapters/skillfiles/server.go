// Package skillfiles serves the files of the skill packages loaded for one Run
// to Provider entries that have no local file-reading tool, such as the Browser
// profile. It is read-only and confined to directories chosen by the Host.
package skillfiles

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

const (
	protocolVersion = "2025-06-18"
	ToolName        = "read_skill_file"
	// Package payloads are at most 64 KiB, so no single file can be larger.
	maxFileBytes   = 64 * 1024
	maxListEntries = 64
	maxRequestSize = 64 * 1024
)

// Same relative path grammar as package validation: no hidden segments, so the
// generated cache marker and pending temporary files are never served.
var relativePattern = regexp.MustCompile(`^[a-zA-Z0-9._/-]+$`)

type Server struct {
	Host    string
	Version string
	roots   []string
}

// New validates the Host-supplied package directories. Tool arguments can only
// select files inside one of them; the model cannot add a root.
func New(host, version string, roots []string) (*Server, error) {
	if host != "codex" && host != "claude" {
		return nil, errors.New("skill files server requires --host codex or --host claude")
	}
	if len(roots) == 0 || len(roots) > 5 {
		return nil, errors.New("skill files server requires one to five package directories")
	}
	clean := make([]string, 0, len(roots))
	for _, root := range roots {
		if !filepath.IsAbs(root) || filepath.Clean(root) != root {
			return nil, errors.New("skill package directories must be clean absolute paths")
		}
		info, err := os.Lstat(root)
		if err != nil || !info.IsDir() {
			return nil, errors.New("skill package directory is unavailable")
		}
		if !slices.Contains(clean, root) {
			clean = append(clean, root)
		}
	}
	return &Server{Host: host, Version: version, roots: clean}, nil
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (server *Server) Serve(ctx context.Context, input io.Reader, output io.Writer) error {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 0, 4096), maxRequestSize)
	encoder := json.NewEncoder(output)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var req request
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			if err := encoder.Encode(response{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{Code: -32700, Message: "Parse error"}}); err != nil {
				return err
			}
			continue
		}
		if len(req.ID) == 0 {
			continue
		}
		if err := encoder.Encode(server.handle(req)); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func (server *Server) handle(req request) response {
	resp := response{JSONRPC: "2.0", ID: req.ID}
	switch req.Method {
	case "initialize":
		resp.Result = map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "openlinker-skills-" + server.Host, "version": server.Version},
			"instructions":    "Read-only access to the skill packages the Agent owner pinned for this Run. Pass an absolute path shown in the task instructions.",
		}
	case "ping":
		resp.Result = map[string]any{}
	case "tools/list":
		resp.Result = map[string]any{"tools": []any{toolDefinition()}}
	case "tools/call":
		var params struct {
			Name      string `json:"name"`
			Arguments struct {
				Path string `json:"path"`
			} `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil || params.Name != ToolName {
			resp.Error = &rpcError{Code: -32602, Message: "Invalid tools/call parameters"}
			return resp
		}
		text, err := server.Read(params.Arguments.Path)
		if err != nil {
			resp.Result = map[string]any{"isError": true, "content": []any{map[string]any{"type": "text", "text": err.Error()}}}
			return resp
		}
		resp.Result = map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}}
	default:
		resp.Error = &rpcError{Code: -32601, Message: "Method not found"}
	}
	return resp
}

func toolDefinition() map[string]any {
	return map[string]any{
		"name":        ToolName,
		"title":       "Read skill package file",
		"description": "Read a file, or list a directory, inside a skill package pinned for this Run. Only package directories named in the task instructions are readable; this tool cannot read other local files.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string", "description": "Absolute path of a package file or directory, for example <package directory>/SKILL.md."},
			},
			"required":             []string{"path"},
			"additionalProperties": false,
		},
		"annotations": map[string]any{"readOnlyHint": true, "openWorldHint": false},
	}
}

// Read resolves name inside exactly one configured root. os.Root rejects any
// symlink or ".." that would leave the package directory.
func (server *Server) Read(name string) (string, error) {
	if !filepath.IsAbs(name) {
		return "", errors.New("path must be absolute and inside a skill package directory")
	}
	name = filepath.Clean(name)
	for _, rootPath := range server.roots {
		relative, err := filepath.Rel(rootPath, name)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			continue
		}
		if relative != "." && !safeRelative(filepath.ToSlash(relative)) {
			return "", errors.New("path is not a readable skill package file")
		}
		root, err := os.OpenRoot(rootPath)
		if err != nil {
			return "", errors.New("skill package directory is unavailable")
		}
		defer root.Close()
		info, err := root.Stat(relative)
		if err != nil {
			return "", errors.New("skill package file not found")
		}
		if info.IsDir() {
			return listDirectory(root, relative, name)
		}
		if !info.Mode().IsRegular() || info.Size() > maxFileBytes {
			return "", errors.New("path is not a readable skill package file")
		}
		file, err := root.Open(relative)
		if err != nil {
			return "", errors.New("skill package file is unreadable")
		}
		defer file.Close()
		content, err := io.ReadAll(io.LimitReader(file, maxFileBytes+1))
		if err != nil || len(content) > maxFileBytes {
			return "", errors.New("skill package file is unreadable")
		}
		return string(content), nil
	}
	return "", errors.New("path is outside the skill packages pinned for this Run")
}

func safeRelative(relative string) bool {
	if !relativePattern.MatchString(relative) {
		return false
	}
	for _, part := range strings.Split(relative, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.Contains(part, ".pending-") {
			return false
		}
	}
	return true
}

func listDirectory(root *os.Root, relative, display string) (string, error) {
	names := []string{}
	err := fs.WalkDir(root.FS(), filepath.ToSlash(relative), func(entry string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		inner := strings.TrimPrefix(strings.TrimPrefix(entry, filepath.ToSlash(relative)), "/")
		if inner == "" {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") || strings.Contains(d.Name(), ".pending-") {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.Type().IsRegular() {
			if len(names) >= maxListEntries {
				return fs.SkipAll
			}
			names = append(names, inner)
		}
		return nil
	})
	if err != nil {
		return "", errors.New("skill package directory is unreadable")
	}
	slices.Sort(names)
	return fmt.Sprintf("Files in %s:\n%s", display, strings.Join(names, "\n")), nil
}
