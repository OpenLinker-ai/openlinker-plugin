//go:build !windows

package agentexec

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	openlinker "github.com/OpenLinker-ai/openlinker-go"
	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserprotocol"
	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserruntime"
)

type brokerTestEngine struct {
	actions chan browserprotocol.ActionKind
}

func (engine brokerTestEngine) Execute(
	_ context.Context,
	identity browserprotocol.Identity,
	action browserprotocol.Action,
) (browserprotocol.Observation, *browserprotocol.Failure) {
	engine.actions <- action.Kind
	if action.Kind == browserprotocol.ActionClose {
		return browserprotocol.Observation{
			PageStateID: "closed-" + identity.AttachmentID,
		}, nil
	}
	observation := browserprotocol.Observation{
		PageStateID: "page-state-1",
		Viewport: &browserprotocol.Viewport{
			Width:  browserprotocol.BrowserViewportWidth,
			Height: browserprotocol.BrowserViewportHeight,
		},
		NavigationGeneration: 1,
		Screenshot: &browserprotocol.Screenshot{
			MIMEType: "image/jpeg",
			Data:     []byte("jpeg"),
			Width:    browserprotocol.BrowserViewportWidth,
			Height:   browserprotocol.BrowserViewportHeight,
		},
		Origin: "https://example.com",
		Title:  "Example",
	}
	if action.Kind == browserprotocol.ActionPreflight {
		observation.Environment = &browserprotocol.EnvironmentEvidence{
			BrowserEngine:       "chromium",
			BrowserDistribution: "playwright_chromium",
			BrowserVersion:      "149.0.7827.55",
			BrowserMajorVersion: 149,
			BrowserLocale:       "en-US",
			BrowserTimezone:     "UTC",
			FontContractVersion: "openlinker.browser.fonts.v1",
			FontManifestSHA256:  strings.Repeat("a", 64),
		}
	}
	return observation, nil
}

type brokerMCPProvider struct {
	observed      bool
	serverVersion string
}

func (provider *brokerMCPProvider) Run(
	_ context.Context,
	run RunContext,
) (openlinker.RuntimeResult, error) {
	connection, err := net.DialTimeout("unix", run.Browser.ToolSocket, time.Second)
	if err != nil {
		return openlinker.RuntimeResult{}, err
	}
	defer connection.Close()
	for _, request := range []map[string]any{
		{
			"jsonrpc": "2.0",
			"id":      1,
			"method":  "initialize",
			"params":  map[string]any{},
		},
		{
			"jsonrpc": "2.0",
			"id":      2,
			"method":  "tools/call",
			"params": map[string]any{
				"name":      "browser_session",
				"arguments": map[string]any{"operation": "observe"},
			},
		},
		{
			"jsonrpc": "2.0",
			"id":      3,
			"method":  "tools/call",
			"params": map[string]any{
				"name":      "browser_session",
				"arguments": map[string]any{"operation": "observe"},
			},
		},
	} {
		raw, marshalErr := json.Marshal(request)
		if marshalErr != nil {
			return openlinker.RuntimeResult{}, marshalErr
		}
		if _, err := connection.Write(append(raw, '\n')); err != nil {
			return openlinker.RuntimeResult{}, err
		}
	}
	_ = connection.SetReadDeadline(time.Now().Add(2 * time.Second))
	scanner := bufio.NewScanner(connection)
	for scanner.Scan() {
		var response map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &response); err != nil {
			return openlinker.RuntimeResult{}, err
		}
		if response["id"] == float64(1) {
			result, _ := response["result"].(map[string]any)
			serverInfo, _ := result["serverInfo"].(map[string]any)
			provider.serverVersion, _ = serverInfo["version"].(string)
		}
		if response["id"] != float64(2) && response["id"] != float64(3) {
			continue
		}
		raw := string(scanner.Bytes())
		if strings.Contains(raw, "page-state-1") &&
			strings.Contains(raw, "https://example.com") &&
			strings.Contains(raw, `"type":"image"`) &&
			strings.Contains(raw, `"attachment_evidence"`) &&
			!strings.Contains(raw, `"browser_version"`) {
			provider.observed = true
		}
		if response["id"] == float64(3) {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return openlinker.RuntimeResult{}, err
	}
	return openlinker.RuntimeResult{
		Status: "success",
		Output: map[string]any{"observed": provider.observed},
	}, nil
}

func TestBrowserToolBrokerKeepsAuthorityOutOfProviderProcess(t *testing.T) {
	root := shortBrowserTestRoot(t)
	controlRoot := filepath.Join(root, "control")
	leaseRoot := filepath.Join(controlRoot, "leases")
	for _, dir := range []string{controlRoot, leaseRoot} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	credentialPath := filepath.Join(controlRoot, "channel")
	credential := strings.Repeat("a", 64)
	if err := os.WriteFile(credentialPath, []byte(credential+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	socketPath := filepath.Join(controlRoot, "runtime.sock")
	actions := make(chan browserprotocol.ActionKind, 4)
	runtimeServer, err := browserruntime.NewServer(browserruntime.ServerOptions{
		SocketPath:        socketPath,
		ChannelCredential: credential,
		Lease: browserruntime.FileLease{
			Path: filepath.Join(leaseRoot, "active-lease.json"),
		},
		Engine: brokerTestEngine{actions: actions},
	})
	if err != nil {
		t.Fatal(err)
	}
	runtimeContext, stopRuntime := context.WithCancel(context.Background())
	defer stopRuntime()
	runtimeDone := make(chan error, 1)
	go func() { runtimeDone <- runtimeServer.Serve(runtimeContext) }()
	waitForUnixSocket(t, socketPath)

	base := &brokerMCPProvider{}
	config := browserProviderTestConfig(leaseRoot)
	config.Version = "v1.2.3-test"
	config.BrowserSocket = socketPath
	config.BrowserCredentialFile = credentialPath
	config.BrowserBrokerRoot = filepath.Join(root, "broker")
	provider, err := newBrowserExecutionProvider(base, config)
	if err != nil {
		t.Fatal(err)
	}
	var progress []map[string]any
	run := browserProviderTestRun("88888888-8888-4888-8888-888888888888")
	run.Emit = func(eventType string, payload any) error {
		if eventType == "run.status.changed" {
			progress = append(progress, payload.(map[string]any))
		}
		return nil
	}
	if _, err := provider.Run(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if !base.observed {
		t.Fatal("MCP Browser observation did not cross trusted broker and Runtime UDS")
	}
	if base.serverVersion != config.Version {
		t.Fatalf("Browser MCP server version = %q, want embedding version %q", base.serverVersion, config.Version)
	}
	if first, second, third, fourth := <-actions, <-actions, <-actions, <-actions; first != browserprotocol.ActionPreflight ||
		second != browserprotocol.ActionScreenshot ||
		third != browserprotocol.ActionScreenshot ||
		fourth != browserprotocol.ActionClose {
		t.Fatalf(
			"Browser actions = [%s %s %s %s], want preflight, screenshot, screenshot, close",
			first,
			second,
			third,
			fourth,
		)
	}
	if len(progress) != 1 ||
		progress[0]["status"] != "provider_tool_started" ||
		progress[0]["provider"] != config.Provider ||
		progress[0]["phase"] != "started" ||
		progress[0]["tool_kind"] != "mcp_tool" {
		t.Fatalf("bounded Browser progress = %#v", progress)
	}
	stopRuntime()
	if err := <-runtimeDone; err != nil {
		t.Fatal(err)
	}
}

func waitForUnixSocket(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		info, err := os.Lstat(path)
		if err == nil && info.Mode()&os.ModeSocket != 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("Unix socket %s was not created: %v", path, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
