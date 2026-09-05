//go:build !windows

package browserruntime

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserprotocol"
)

func TestProcessEngineSuccessFailureAndProtocolReset(t *testing.T) {
	t.Parallel()
	engine := testProcessEngine(t)
	identity := validRuntimeRequest().Identity

	observation, failure := engine.Execute(
		testActionContext(t),
		identity,
		browserprotocol.Action{Kind: browserprotocol.ActionNavigate, URL: "https://success.example"},
	)
	if failure != nil {
		t.Fatal(failure)
	}
	if observation.PageStateID != "state-1" ||
		observation.Origin != "https://success.example" ||
		observation.Title != "" {
		t.Fatalf("observation = %#v", observation)
	}

	_, failure = engine.Execute(
		testActionContext(t),
		identity,
		browserprotocol.Action{Kind: browserprotocol.ActionNavigate, URL: "https://blocked.example"},
	)
	if failure == nil || failure.Code != browserprotocol.ErrorTargetBlocked {
		t.Fatalf("failure = %#v, want target blocked", failure)
	}

	_, failure = engine.Execute(
		testActionContext(t),
		identity,
		browserprotocol.Action{Kind: browserprotocol.ActionNavigate, URL: "https://malformed.example"},
	)
	if failure == nil || failure.Code != browserprotocol.ErrorOutputInvalid {
		t.Fatalf("failure = %#v, want output invalid", failure)
	}
	observation, failure = engine.Execute(
		testActionContext(t),
		identity,
		browserprotocol.Action{Kind: browserprotocol.ActionScreenshot},
	)
	if failure != nil || observation.PageStateID != "state-1" {
		t.Fatalf("process did not restart cleanly: observation=%#v failure=%#v", observation, failure)
	}
}

func TestProcessEnginePreservesBoundedMutationFailureEvidence(t *testing.T) {
	t.Parallel()
	engine := testProcessEngine(t)
	identity := validRuntimeRequest().Identity

	_, failure := engine.Execute(
		testActionContext(t),
		identity,
		browserprotocol.Action{
			Kind: browserprotocol.ActionNavigate,
			URL:  "https://mutation-blocked.example",
		},
	)
	if failure == nil ||
		failure.Code != browserprotocol.ErrorMutationOriginBlocked ||
		failure.RetrySameAction == nil || *failure.RetrySameAction ||
		failure.AttachmentUsable == nil || !*failure.AttachmentUsable ||
		failure.FreshObservationRequired == nil || *failure.FreshObservationRequired ||
		failure.ObservedOrigin != "https://mutation-blocked.example" ||
		len(failure.BrowserMutationOrigins) != 1 ||
		failure.BrowserMutationOrigins[0] != "https://allowed.example" {
		t.Fatalf("mutation-origin failure = %#v", failure)
	}

	_, failure = engine.Execute(
		testActionContext(t),
		identity,
		browserprotocol.Action{
			Kind: browserprotocol.ActionNavigate,
			URL:  "https://mutation-unknown.example",
		},
	)
	if failure == nil ||
		failure.Code != browserprotocol.ErrorMutationOutcomeUnknown ||
		failure.MutationOutcomeReason != "fixture_dispatch_uncertain" ||
		failure.RetrySameAction == nil || *failure.RetrySameAction ||
		failure.AttachmentUsable == nil || !*failure.AttachmentUsable ||
		failure.FreshObservationRequired == nil || !*failure.FreshObservationRequired ||
		failure.AttemptedUnits == nil || *failure.AttemptedUnits != 1 ||
		failure.UndispatchedUnits == nil || *failure.UndispatchedUnits != 2 ||
		failure.ActionIndex == nil || *failure.ActionIndex != 1 ||
		failure.CompletedActions == nil || *failure.CompletedActions != 1 ||
		failure.MutationRequestsObserved != 1 {
		t.Fatalf("mutation-unknown failure = %#v", failure)
	}
}

func TestEngineDiagnosticsAreLocalBoundedAndRedacted(t *testing.T) {
	var output strings.Builder
	writer := newEngineDiagnosticWriter(&output)
	_, _ = writer.Write([]byte(
		"launch failed at https://user:pass@example.com/private?token=value " +
			"Authorization: Bear",
	))
	_, _ = writer.Write([]byte("er sk-super-secret-value\n"))
	for index := 0; index < 100; index++ {
		_, _ = writer.Write([]byte(strings.Repeat("x", maxEngineLogLine) + "\n"))
	}
	writer.Flush()
	logged := output.String()
	for _, secret := range []string{
		"https://user:pass@example.com",
		"token=value",
		"sk-super-secret-value",
		"Bearer",
	} {
		if strings.Contains(logged, secret) {
			t.Fatalf("engine diagnostic leaked %q: %s", secret, logged)
		}
	}
	if !strings.Contains(logged, "browser-engine: launch failed at [url]") {
		t.Fatalf("bounded diagnostic lost safe context: %q", logged)
	}
	if len(logged) > maxEngineLogBytes {
		t.Fatalf("engine diagnostic bytes = %d, want <= %d", len(logged), maxEngineLogBytes)
	}
}

func TestProcessEngineCancellationKillsChildAndRecovers(t *testing.T) {
	t.Parallel()
	engine := testProcessEngine(t)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	_, failure := engine.Execute(
		ctx,
		validRuntimeRequest().Identity,
		browserprotocol.Action{Kind: browserprotocol.ActionWait, DurationMS: processIntPointer(5000)},
	)
	if failure == nil || failure.Code != browserprotocol.ErrorDeadlineExceeded {
		t.Fatalf("failure = %#v, want deadline exceeded", failure)
	}
	observation, failure := engine.Execute(
		testActionContext(t),
		validRuntimeRequest().Identity,
		browserprotocol.Action{Kind: browserprotocol.ActionScreenshot},
	)
	if failure != nil || observation.PageStateID != "state-1" {
		t.Fatalf("recovered observation=%#v failure=%#v", observation, failure)
	}
}

func TestProcessEngineCancellationKillsProcessGroupHoldingDiagnosticPipe(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	engine, err := NewProcessEngine(ProcessEngineOptions{
		Command: []string{
			os.Args[0],
			"-test.run=^TestProcessEngineHelper$",
			"--",
			"browser-engine-helper",
		},
		Environment: []string{"HOME=" + home},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, failure := engine.Execute(
		ctx,
		validRuntimeRequest().Identity,
		browserprotocol.Action{
			Kind: browserprotocol.ActionNavigate,
			URL:  "https://process-tree-hang.example",
		},
	)
	if failure == nil || failure.Code != browserprotocol.ErrorDeadlineExceeded {
		t.Fatalf("failure = %#v, want deadline exceeded", failure)
	}
	if elapsed := time.Since(started); elapsed > 4*time.Second {
		t.Fatalf("cancellation took %s, want bounded process cleanup", elapsed)
	}

	rawPID, err := os.ReadFile(filepath.Join(home, "grandchild.pid"))
	if err != nil {
		t.Fatal(err)
	}
	grandchildPID, err := strconv.Atoi(strings.TrimSpace(string(rawPID)))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for processExists(grandchildPID) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if processExists(grandchildPID) {
		t.Fatalf("grandchild process %d survived process-group reset", grandchildPID)
	}

	observation, failure := engine.Execute(
		testActionContext(t),
		validRuntimeRequest().Identity,
		browserprotocol.Action{Kind: browserprotocol.ActionScreenshot},
	)
	if failure != nil || observation.PageStateID != "state-1" {
		t.Fatalf("recovered observation=%#v failure=%#v", observation, failure)
	}
}

func TestProcessEngineGracefulCloseKillsDiagnosticPipeDescendant(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	engine, err := NewProcessEngine(ProcessEngineOptions{
		Command: []string{
			os.Args[0],
			"-test.run=^TestProcessEngineHelper$",
			"--",
			"browser-engine-helper",
		},
		Environment: []string{"HOME=" + home},
	})
	if err != nil {
		t.Fatal(err)
	}
	observation, failure := engine.Execute(
		testActionContext(t),
		validRuntimeRequest().Identity,
		browserprotocol.Action{
			Kind: browserprotocol.ActionNavigate,
			URL:  "https://diagnostic-descendant.example",
		},
	)
	if failure != nil || observation.PageStateID != "state-1" {
		t.Fatalf("observation=%#v failure=%#v", observation, failure)
	}
	rawPID, err := os.ReadFile(filepath.Join(home, "grandchild.pid"))
	if err != nil {
		t.Fatal(err)
	}
	grandchildPID, err := strconv.Atoi(strings.TrimSpace(string(rawPID)))
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 4*time.Second {
		t.Fatalf("graceful close took %s, want bounded cleanup", elapsed)
	}
	deadline := time.Now().Add(2 * time.Second)
	for processExists(grandchildPID) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if processExists(grandchildPID) {
		t.Fatalf("grandchild process %d survived graceful close", grandchildPID)
	}
}

func TestProcessEngineRejectsForbiddenEnvironment(t *testing.T) {
	t.Parallel()
	command := []string{os.Args[0], "-test.run=^TestProcessEngineHelper$", "--", "browser-engine-helper"}
	for _, environment := range [][]string{
		{"OPENAI_API_KEY=secret"},
		{"NO_PROXY=localhost"},
		{"HOME=/tmp", "HOME=/other"},
		{"HOME=relative"},
		{"OPENLINKER_BROWSER_EGRESS_PROXY=http://user:secret@proxy:3128"},
		{"OPENLINKER_NATIVE_CHROME_ENABLED=false"},
		{"OPENLINKER_NATIVE_CHROME_EXTENSION_ID=invalid"},
		{"OPENLINKER_NATIVE_CHROME_ASSET_MANIFEST_SHA256=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"},
	} {
		if _, err := NewProcessEngine(ProcessEngineOptions{
			Command:     command,
			Environment: environment,
		}); err == nil {
			t.Fatalf("NewProcessEngine(%q) succeeded", environment)
		}
	}
}

func TestProcessEngineAcceptsLockedNativeChromeEnvironment(t *testing.T) {
	t.Parallel()
	command := []string{os.Args[0], "-test.run=^TestProcessEngineHelper$", "--", "browser-engine-helper"}
	environment := []string{
		"HOME=/browser-home",
		"TMPDIR=/browser-tmp",
		"NO_PROXY=",
		"OPENLINKER_BROWSER_EGRESS_PROXY=http://172.18.0.2:3128",
		"OPENLINKER_BROWSER_PROFILE_DIR=/browser-tmp/profiles/official_chrome_extension/active",
		"OPENLINKER_BROWSER_EXECUTABLE_PATH=/opt/google/chrome/chrome",
		"OPENLINKER_BROWSER_ENGINE=chrome",
		"OPENLINKER_BROWSER_DISTRIBUTION=chrome_for_testing",
		"OPENLINKER_BROWSER_VERSION=151.0.7922.77",
		"OPENLINKER_BROWSER_LOCALE=en-US",
		"OPENLINKER_BROWSER_TIMEZONE=UTC",
		"OPENLINKER_BROWSER_FONT_CONTRACT_VERSION=openlinker.browser.fonts.v1",
		"OPENLINKER_BROWSER_FONT_MANIFEST_SHA256=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"OPENLINKER_BROWSER_PROFILE_GENERATION=2",
		"OPENLINKER_BROWSER_MAX_ACTIONS_PER_ORIGIN_MINUTE=120",
		"OPENLINKER_BROWSER_MAX_NAVIGATIONS_PER_ORIGIN_MINUTE=20",
		"PLAYWRIGHT_BROWSERS_PATH=/ms-playwright",
		"LANG=C.UTF-8",
		"OPENLINKER_NATIVE_CHROME_BINARY=/opt/google/chrome/chrome",
		"OPENLINKER_NATIVE_CHROME_EXTENSION_ROOT=/opt/openlinker/native-chrome/extension",
		"OPENLINKER_NATIVE_CHROME_EXTENSION_ID=abcdefghijklmnopabcdefghijklmnop",
		"OPENLINKER_NATIVE_CHROME_EXTENSION_VERSION=1.2.3.4",
		"OPENLINKER_NATIVE_CHROME_ACTIVATION_PATH=/openlinker-runtime/index.html",
		"OPENLINKER_NATIVE_CHROME_HOST=/opt/openlinker/native-chrome/bin/openlinker-native-chrome-host",
		"OPENLINKER_NATIVE_CHROME_PROTOCOL=openlinker.native-chrome.v2",
		"OPENLINKER_NATIVE_CHROME_ASSET_MANIFEST_SHA256=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"OPENLINKER_NATIVE_CHROME_ENABLED=true",
		"OPENLINKER_NATIVE_CHROME_REQUIRE_ORIGIN=true",
		"OPENLINKER_NATIVE_CHROME_SOCKET=/browser-tmp/profiles/official_chrome_extension/native-host.sock",
	}
	if _, err := NewProcessEngine(ProcessEngineOptions{Command: command, Environment: environment}); err != nil {
		t.Fatal(err)
	}
}

func TestProcessEngineOpsObserverNeverStartsProcessAndUsesSeparateSocket(t *testing.T) {
	root, err := os.MkdirTemp("", "ol-engine-ops-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	socketPath := filepath.Join(root, "ops.sock")
	engine, err := NewProcessEngine(ProcessEngineOptions{
		Command: []string{
			os.Args[0], "-test.run=^TestProcessEngineHelper$", "--", "browser-engine-helper",
		},
		Environment: []string{
			"OPENLINKER_BROWSER_OPS_OBSERVER_ENABLED=true",
			"OPENLINKER_BROWSER_ENGINE_OPS_SOCKET=" + socketPath,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Close() })
	identity := validRuntimeRequest().Identity
	if _, busy, observerErr := engine.ObserveActiveOps(
		testActionContext(t), identity, browserprotocol.OpsObserverStatusOperation,
	); observerErr == nil || observerErr.Code != browserprotocol.OpsObserverRunNotActive || busy || engine.process != nil {
		t.Fatalf("inactive observation started process: busy=%v error=%v process=%v", busy, observerErr, engine.process)
	}
	if _, failure := engine.Execute(
		testActionContext(t), identity,
		browserprotocol.Action{Kind: browserprotocol.ActionScreenshot},
	); failure != nil {
		t.Fatal(failure)
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	done := make(chan error, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			done <- acceptErr
			return
		}
		defer connection.Close()
		var request engineOpsObserverRequest
		if decodeErr := json.NewDecoder(connection).Decode(&request); decodeErr != nil {
			done <- decodeErr
			return
		}
		if request.Identity.RunID != identity.RunID ||
			request.Operation != browserprotocol.OpsObserverStatusOperation {
			done <- errors.New("Ops Observer authority did not match")
			return
		}
		done <- json.NewEncoder(connection).Encode(engineOpsObserverResponse{
			ContractID: engineOpsObserverContractID,
			ActionID:   request.ActionID,
			Status:     "ok",
			PageURL:    "https://example.com/path",
			PageTitle:  "Example",
		})
	}()
	engine.mu.Lock()
	page, busy, observerErr := engine.ObserveActiveOps(
		testActionContext(t), identity, browserprotocol.OpsObserverStatusOperation,
	)
	engine.mu.Unlock()
	if observerErr != nil || busy || page.PageURL != "https://example.com/path" ||
		page.PageTitle != "Example" {
		t.Fatalf("Ops observation = %#v, busy=%v, error=%v", page, busy, observerErr)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestProcessEngineHelper(t *testing.T) {
	if len(os.Args) == 0 {
		return
	}
	if os.Args[len(os.Args)-1] == "browser-engine-grandchild" {
		for {
			time.Sleep(time.Hour)
		}
	}
	if os.Args[len(os.Args)-1] != "browser-engine-helper" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 64<<10), browserprotocol.MaxRequestBytes)
	counter := 0
	for scanner.Scan() {
		var request engineRequest
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			os.Exit(2)
		}
		counter++
		switch request.Action.URL {
		case "https://process-tree-hang.example":
			startDiagnosticGrandchild()
			for {
				time.Sleep(time.Hour)
			}
		case "https://diagnostic-descendant.example":
			startDiagnosticGrandchild()
			writeHelperResponse(engineResponse{
				ContractID: request.ContractID,
				ActionID:   request.ActionID,
				Status:     "ok",
				Observation: &browserprotocol.Observation{
					PageStateID: "state-1",
					Viewport: &browserprotocol.Viewport{
						Width:  browserprotocol.BrowserViewportWidth,
						Height: browserprotocol.BrowserViewportHeight,
					},
					NavigationGeneration: 1,
					Origin:               request.Action.URL,
				},
			})
		case "https://blocked.example":
			writeHelperResponse(engineResponse{
				ContractID: engineContractID,
				ActionID:   request.ActionID,
				Status:     "error",
				Error: browserprotocol.NewFailure(
					browserprotocol.ErrorTargetBlocked,
					"fixture target is blocked",
					false,
				),
			})
		case "https://malformed.example":
			fmt.Println("{")
		case "https://mutation-blocked.example":
			failure := browserprotocol.NewFailure(
				browserprotocol.ErrorMutationOriginBlocked,
				"fixture mutation origin is blocked",
				false,
			)
			failure.RetrySameAction = processBoolPointer(false)
			failure.AttachmentUsable = processBoolPointer(true)
			failure.FreshObservationRequired = processBoolPointer(false)
			failure.ObservedOrigin = "https://mutation-blocked.example"
			failure.BrowserMutationOrigins = []string{"https://allowed.example"}
			writeHelperResponse(engineResponse{
				ContractID: engineContractID,
				ActionID:   request.ActionID,
				Status:     "error",
				Error:      failure,
			})
		case "https://mutation-unknown.example":
			failure := browserprotocol.NewFailure(
				browserprotocol.ErrorMutationOutcomeUnknown,
				"fixture mutation outcome is unknown",
				false,
			)
			failure.RetrySameAction = processBoolPointer(false)
			failure.AttachmentUsable = processBoolPointer(true)
			failure.FreshObservationRequired = processBoolPointer(true)
			failure.MutationOutcomeReason = "fixture_dispatch_uncertain"
			failure.AttemptedUnits = processIntPointer(1)
			failure.UndispatchedUnits = processIntPointer(2)
			failure.ActionIndex = processIntPointer(1)
			failure.CompletedActions = processIntPointer(1)
			failure.MutationRequestsObserved = 1
			writeHelperResponse(engineResponse{
				ContractID: engineContractID,
				ActionID:   request.ActionID,
				Status:     "error",
				Error:      failure,
			})
		default:
			if request.Action.Kind == browserprotocol.ActionWait {
				time.Sleep(5 * time.Second)
			}
			writeHelperResponse(engineResponse{
				ContractID: engineContractID,
				ActionID:   request.ActionID,
				Status:     "ok",
				Observation: &browserprotocol.Observation{
					PageStateID: fmt.Sprintf("state-%d", counter),
					Viewport: &browserprotocol.Viewport{
						Width:  browserprotocol.BrowserViewportWidth,
						Height: browserprotocol.BrowserViewportHeight,
					},
					NavigationGeneration: 1,
					Origin:               strings.TrimSuffix(request.Action.URL, "/"),
					Title:                os.Getenv("HOME"),
				},
			})
		}
	}
	os.Exit(0)
}

func writeHelperResponse(response engineResponse) {
	raw, err := json.Marshal(response)
	if err != nil {
		os.Exit(3)
	}
	fmt.Println(string(raw))
}

func startDiagnosticGrandchild() {
	child := exec.Command(
		os.Args[0],
		"-test.run=^TestProcessEngineHelper$",
		"--",
		"browser-engine-grandchild",
	)
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr
	if err := child.Start(); err != nil {
		os.Exit(4)
	}
	if err := os.WriteFile(
		filepath.Join(os.Getenv("HOME"), "grandchild.pid"),
		[]byte(strconv.Itoa(child.Process.Pid)+"\n"),
		0o600,
	); err != nil {
		os.Exit(5)
	}
}

func testProcessEngine(t *testing.T) *ProcessEngine {
	t.Helper()
	engine, err := NewProcessEngine(ProcessEngineOptions{
		Command: []string{
			os.Args[0],
			"-test.run=^TestProcessEngineHelper$",
			"--",
			"browser-engine-helper",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = engine.Close()
	})
	return engine
}

func testActionContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func processIntPointer(value int) *int {
	return &value
}

func processBoolPointer(value bool) *bool {
	return &value
}

func processExists(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func TestProcessEngineReportsActionInFlightOnlyWhileExecuting(t *testing.T) {
	t.Parallel()
	engine := testProcessEngine(t)
	identity := validRuntimeRequest().Identity

	if started, inFlight := engine.ActionInFlightSample(); started != 0 || inFlight {
		t.Fatalf("idle Engine sample = (%d, %v)", started, inFlight)
	}

	sampled := make(chan [2]uint64, 1)
	go func() {
		// The helper holds a wait action open, so this samples the Engine while
		// exactly one provider-issued action occupies it.
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			started, inFlight := engine.ActionInFlightSample()
			if inFlight {
				sampled <- [2]uint64{started, 1}
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		sampled <- [2]uint64{0, 0}
	}()

	// The helper sleeps a fixed 5s for any wait action, so give the call room to
	// finish and leave a wide window for the concurrent sampler.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, failure := engine.Execute(
		ctx,
		identity,
		browserprotocol.Action{Kind: browserprotocol.ActionWait, DurationMS: processIntPointer(250)},
	); failure != nil {
		t.Fatal(failure)
	}

	observed := <-sampled
	if observed[1] != 1 {
		t.Fatal("Ops sampling never observed the in-flight action")
	}
	if observed[0] != 1 {
		t.Fatalf("in-flight sample reported started = %d, want 1", observed[0])
	}

	started, inFlight := engine.ActionInFlightSample()
	if inFlight {
		t.Fatal("settled Engine still reports an in-flight action")
	}
	if started != 1 {
		t.Fatalf("settled started count = %d, want 1", started)
	}
}
