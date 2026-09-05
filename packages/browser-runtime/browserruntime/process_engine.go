//go:build !windows

package browserruntime

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserprotocol"
)

const (
	engineContractID            = "openlinker.browser.engine.v2"
	engineViewerContractID      = "openlinker.browser.engine.viewer.v1"
	engineOpsObserverContractID = "openlinker.browser.engine.ops-observer.v1"
	maxEngineOutputBytes        = browserprotocol.MaxResponseBytes
	maxEngineLogBytes           = 32 << 10
	maxEngineLogLine            = 4 << 10
)

type ProcessEngineOptions struct {
	Command          []string
	Environment      []string
	DiagnosticWriter io.Writer
}

type ProcessEngine struct {
	options       ProcessEngineOptions
	mu            sync.Mutex
	process       *engineProcess
	nextID        uint64
	closed        bool
	opsSocketPath string
	opsReady      atomic.Bool
	// actionsInFlight counts provider-issued Browser actions currently being
	// executed by this Engine, and actionsStarted counts how many have begun.
	// ObserveActiveOps never takes engine.mu, so the Ops Observer can sample
	// both while an action occupies the Engine.
	actionsInFlight atomic.Int64
	actionsStarted  atomic.Uint64
}

type engineProcess struct {
	instanceID uint64
	command    *exec.Cmd
	stdin      io.WriteCloser
	stdoutPipe io.ReadCloser
	stdout     *bufio.Reader
	diagnostic *engineDiagnosticWriter
}

var processEngineInstanceSequence atomic.Uint64
var processEngineOpsSequence atomic.Uint64

type engineRequest struct {
	ContractID string                   `json:"contract_id"`
	ActionID   string                   `json:"action_id"`
	Deadline   string                   `json:"deadline"`
	Identity   browserprotocol.Identity `json:"identity"`
	Action     browserprotocol.Action   `json:"action"`
}

type engineResponse struct {
	ContractID  string                       `json:"contract_id"`
	ActionID    string                       `json:"action_id"`
	Status      string                       `json:"status"`
	Observation *browserprotocol.Observation `json:"observation,omitempty"`
	Error       *browserprotocol.Failure     `json:"error,omitempty"`
}

type engineViewerRequest struct {
	ContractID string                          `json:"contract_id"`
	ActionID   string                          `json:"action_id"`
	Deadline   string                          `json:"deadline"`
	Identity   browserprotocol.Identity        `json:"identity"`
	Operation  browserprotocol.ViewerOperation `json:"operation"`
	Input      *browserprotocol.ViewerInput    `json:"input,omitempty"`
}

type engineViewerResponse struct {
	ContractID string                       `json:"contract_id"`
	ActionID   string                       `json:"action_id"`
	Status     string                       `json:"status"`
	Frame      *browserprotocol.ViewerFrame `json:"frame,omitempty"`
	Error      *browserprotocol.Failure     `json:"error,omitempty"`
}

type engineOpsObserverRequest struct {
	ContractID string                               `json:"contract_id"`
	ActionID   string                               `json:"action_id"`
	Deadline   string                               `json:"deadline"`
	Identity   browserprotocol.Identity             `json:"identity"`
	Operation  browserprotocol.OpsObserverOperation `json:"operation"`
}

type engineOpsObserverResponse struct {
	ContractID string                            `json:"contract_id"`
	ActionID   string                            `json:"action_id"`
	Status     string                            `json:"status"`
	PageURL    string                            `json:"page_url,omitempty"`
	PageTitle  string                            `json:"page_title,omitempty"`
	Frame      *browserprotocol.ViewerFrame      `json:"frame,omitempty"`
	Error      *browserprotocol.OpsObserverError `json:"error,omitempty"`
}

func NewProcessEngine(options ProcessEngineOptions) (*ProcessEngine, error) {
	if len(options.Command) == 0 ||
		!filepath.IsAbs(options.Command[0]) ||
		filepath.Clean(options.Command[0]) != options.Command[0] {
		return nil, errors.New("browser engine command must use an absolute executable path")
	}
	for _, argument := range options.Command[1:] {
		if strings.ContainsRune(argument, 0) {
			return nil, errors.New("browser engine command contains an invalid argument")
		}
	}
	if err := validateEngineEnvironment(options.Environment); err != nil {
		return nil, err
	}
	opsSocketPath := engineEnvironmentValue(
		options.Environment,
		"OPENLINKER_BROWSER_ENGINE_OPS_SOCKET",
	)
	if opsSocketPath != "" &&
		(!filepath.IsAbs(opsSocketPath) || filepath.Clean(opsSocketPath) != opsSocketPath) {
		return nil, errors.New("browser engine Ops Observer socket path is invalid")
	}
	return &ProcessEngine{options: options, opsSocketPath: opsSocketPath}, nil
}

func (engine *ProcessEngine) Execute(
	ctx context.Context,
	identity browserprotocol.Identity,
	action browserprotocol.Action,
) (browserprotocol.Observation, *browserprotocol.Failure) {
	if engine == nil {
		return browserprotocol.Observation{}, runtimeUnavailable("browser engine is nil")
	}
	if failure := identity.Validate(); failure != nil {
		return browserprotocol.Observation{}, failure
	}
	if failure := action.ValidateForPolicy(identity.BrowserInteractionPolicy); failure != nil {
		return browserprotocol.Observation{}, failure
	}
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if engine.closed {
		return browserprotocol.Observation{}, runtimeUnavailable("browser engine is closed")
	}
	engine.actionsStarted.Add(1)
	engine.actionsInFlight.Add(1)
	defer engine.actionsInFlight.Add(-1)
	if err := ctx.Err(); err != nil {
		return browserprotocol.Observation{}, contextFailure(err)
	}
	process, err := engine.ensureProcess()
	if err != nil {
		return browserprotocol.Observation{}, runtimeUnavailable("browser engine could not start")
	}
	engine.nextID++
	actionID := strconv.FormatUint(engine.nextID, 10)
	deadline, ok := ctx.Deadline()
	if !ok {
		engine.resetProcess()
		return browserprotocol.Observation{}, processResetFailure(browserprotocol.NewFailure(
			browserprotocol.ErrorProtocolInvalid,
			"browser engine action requires a deadline",
			false,
		))
	}
	request := engineRequest{
		ContractID: engineContractID,
		ActionID:   actionID,
		Deadline:   deadline.UTC().Format(browserTimeFormat),
		Identity:   identity,
		Action:     action,
	}
	raw, err := json.Marshal(request)
	if err != nil {
		engine.resetProcess()
		return browserprotocol.Observation{}, processResetFailure(browserprotocol.NewFailure(
			browserprotocol.ErrorInternal,
			"browser engine request could not be encoded",
			false,
		))
	}
	raw = append(raw, '\n')
	if err := writeAllProcess(process.stdin, raw); err != nil {
		engine.resetProcess()
		return browserprotocol.Observation{}, processResetFailure(
			runtimeUnavailable("browser engine input failed"),
		)
	}

	type readResult struct {
		value []byte
		err   error
	}
	result := make(chan readResult, 1)
	go func() {
		value, readErr := readBoundedLine(process.stdout, maxEngineOutputBytes)
		result <- readResult{value: value, err: readErr}
	}()

	select {
	case <-ctx.Done():
		_ = engine.resetProcess()
		return browserprotocol.Observation{}, processResetFailure(contextFailure(ctx.Err()))
	case output := <-result:
		if err := ctx.Err(); err != nil {
			engine.resetProcess()
			return browserprotocol.Observation{}, processResetFailure(contextFailure(err))
		}
		if output.err != nil {
			engine.resetProcess()
			if errors.Is(output.err, errEngineOutputTooLarge) {
				return browserprotocol.Observation{}, processResetFailure(browserprotocol.NewFailure(
					browserprotocol.ErrorOutputTooLarge,
					"browser engine response exceeds the output limit",
					false,
				))
			}
			return browserprotocol.Observation{}, processResetFailure(
				runtimeUnavailable("browser engine output failed"),
			)
		}
		response, failure, rejectionReason := decodeEngineResponse(output.value, actionID)
		if failure != nil {
			writeEngineRejectionDiagnostic(
				process,
				"response_envelope",
				browserprotocol.NewFailure(
					browserprotocol.ErrorOutputInvalid,
					rejectionReason,
					false,
				),
				"",
			)
			engine.resetProcess()
			return browserprotocol.Observation{}, processResetFailure(failure)
		}
		if response.Error != nil {
			failure, rejection := normalizeEngineFailure(response.Error)
			if rejection != nil {
				writeEngineRejectionDiagnostic(
					process,
					"structured_failure",
					rejection,
					string(response.Error.Code),
				)
			}
			failure.EngineInstanceID = process.instanceID
			return browserprotocol.Observation{}, failure
		}
		if validationFailure := response.Observation.ValidateEngine(); validationFailure != nil {
			writeEngineRejectionDiagnostic(
				process,
				"observation",
				validationFailure,
				"",
			)
			engine.resetProcess()
			return browserprotocol.Observation{}, processResetFailure(validationFailure)
		}
		response.Observation.EngineInstanceID = process.instanceID
		return *response.Observation, nil
	}
}

func (engine *ProcessEngine) ExecuteViewer(
	ctx context.Context,
	identity browserprotocol.Identity,
	operation browserprotocol.ViewerOperation,
	input *browserprotocol.ViewerInput,
) (*browserprotocol.ViewerFrame, *browserprotocol.Failure) {
	if engine == nil {
		return nil, runtimeUnavailable("browser engine is nil")
	}
	requestValidation := browserprotocol.ViewerRequest{
		ContractID: browserprotocol.ViewerContractID,
		RequestID:  "11111111-1111-4111-8111-111111111111",
		Deadline:   time.Now().UTC().Add(time.Second),
		Identity:   identity,
		Operation:  operation,
		Input:      input,
	}
	if failure := requestValidation.Validate(time.Now().UTC()); failure != nil {
		return nil, failure
	}
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if engine.closed {
		return nil, runtimeUnavailable("browser engine is closed")
	}
	if err := ctx.Err(); err != nil {
		return nil, contextFailure(err)
	}
	process, err := engine.ensureProcess()
	if err != nil {
		return nil, runtimeUnavailable("browser engine could not start")
	}
	engine.nextID++
	actionID := strconv.FormatUint(engine.nextID, 10)
	deadline, ok := ctx.Deadline()
	if !ok {
		engine.resetProcess()
		return nil, processResetFailure(browserprotocol.NewFailure(
			browserprotocol.ErrorProtocolInvalid,
			"browser engine viewer operation requires a deadline",
			false,
		))
	}
	request := engineViewerRequest{
		ContractID: engineViewerContractID,
		ActionID:   actionID,
		Deadline:   deadline.UTC().Format(browserTimeFormat),
		Identity:   identity,
		Operation:  operation,
		Input:      input,
	}
	raw, err := json.Marshal(request)
	if err != nil {
		engine.resetProcess()
		return nil, processResetFailure(browserprotocol.NewFailure(
			browserprotocol.ErrorInternal,
			"browser engine viewer request could not be encoded",
			false,
		))
	}
	raw = append(raw, '\n')
	if err := writeAllProcess(process.stdin, raw); err != nil {
		engine.resetProcess()
		return nil, processResetFailure(
			runtimeUnavailable("browser engine viewer input failed"),
		)
	}
	type readResult struct {
		value []byte
		err   error
	}
	result := make(chan readResult, 1)
	go func() {
		value, readErr := readBoundedLine(process.stdout, maxEngineOutputBytes)
		result <- readResult{value: value, err: readErr}
	}()
	select {
	case <-ctx.Done():
		_ = engine.resetProcess()
		return nil, processResetFailure(contextFailure(ctx.Err()))
	case output := <-result:
		if err := ctx.Err(); err != nil {
			engine.resetProcess()
			return nil, processResetFailure(contextFailure(err))
		}
		if output.err != nil {
			engine.resetProcess()
			if errors.Is(output.err, errEngineOutputTooLarge) {
				return nil, processResetFailure(browserprotocol.NewFailure(
					browserprotocol.ErrorOutputTooLarge,
					"browser engine viewer response exceeds the output limit",
					false,
				))
			}
			return nil, processResetFailure(
				runtimeUnavailable("browser engine viewer output failed"),
			)
		}
		response, failure, rejectionReason := decodeEngineViewerResponse(output.value, actionID)
		if failure != nil {
			writeEngineRejectionDiagnostic(
				process,
				"viewer_response_envelope",
				browserprotocol.NewFailure(
					browserprotocol.ErrorOutputInvalid,
					rejectionReason,
					false,
				),
				"",
			)
			engine.resetProcess()
			return nil, processResetFailure(failure)
		}
		if response.Error != nil {
			failure, rejection := normalizeEngineFailure(response.Error)
			if rejection != nil {
				writeEngineRejectionDiagnostic(
					process,
					"viewer_structured_failure",
					rejection,
					string(response.Error.Code),
				)
			}
			return nil, failure
		}
		if response.Frame != nil {
			if validationFailure := response.Frame.Validate(); validationFailure != nil {
				engine.resetProcess()
				return nil, processResetFailure(validationFailure)
			}
		}
		return response.Frame, nil
	}
}

// ActionInFlightSample reports how many provider-issued Browser actions this
// Engine has started and whether one is executing right now. Callers that need
// to prove an action spanned a whole window sample twice and require both an
// in-flight result and an unchanged started count. It reads atomics rather than
// engine.mu so an observer can sample it while the Engine is occupied.
func (engine *ProcessEngine) ActionInFlightSample() (uint64, bool) {
	if engine == nil {
		return 0, false
	}
	// Load the counter first so a concurrent start cannot make a stale
	// "started" value look unchanged across the caller's window.
	inFlight := engine.actionsInFlight.Load() > 0
	return engine.actionsStarted.Load(), inFlight
}

func (engine *ProcessEngine) ObserveActiveOps(
	ctx context.Context,
	identity browserprotocol.Identity,
	operation browserprotocol.OpsObserverOperation,
) (opsPageObservation, bool, *browserprotocol.OpsObserverError) {
	if engine == nil {
		return opsPageObservation{}, false, browserprotocol.NewOpsObserverError(
			browserprotocol.OpsObserverRunNotActive,
			"requested Run is not active",
		)
	}
	if failure := identity.Validate(); failure != nil {
		return opsPageObservation{}, false, browserprotocol.NewOpsObserverError(
			browserprotocol.OpsObserverInternalError,
			"active Browser identity is invalid",
		)
	}
	switch operation {
	case browserprotocol.OpsObserverStatusOperation,
		browserprotocol.OpsObserverFrameOperation:
	default:
		return opsPageObservation{}, false, browserprotocol.NewOpsObserverError(
			browserprotocol.OpsObserverProtocolError,
			"Ops Observer operation is invalid",
		)
	}
	if !engine.opsReady.Load() || engine.opsSocketPath == "" {
		return opsPageObservation{}, false, browserprotocol.NewOpsObserverError(
			browserprotocol.OpsObserverRunNotActive,
			"requested Run is not active",
		)
	}
	socketPath := engine.opsSocketPath
	observeContext, cancel := context.WithTimeout(
		ctx,
		browserprotocol.MaxOpsObserverCapture,
	)
	defer cancel()
	deadline, ok := observeContext.Deadline()
	if !ok {
		return opsPageObservation{}, false, browserprotocol.NewOpsObserverError(
			browserprotocol.OpsObserverInternalError,
			"Ops Observer deadline is missing",
		)
	}
	dialer := net.Dialer{}
	connection, err := dialer.DialContext(observeContext, "unix", socketPath)
	if err != nil {
		if observeContext.Err() != nil {
			return opsPageObservation{}, true, nil
		}
		return opsPageObservation{}, false, browserprotocol.NewOpsObserverError(
			browserprotocol.OpsObserverRunNotActive,
			"requested Run is not active",
		)
	}
	defer connection.Close()
	if err := connection.SetDeadline(deadline); err != nil {
		return opsPageObservation{}, true, nil
	}
	actionID := strconv.FormatUint(processEngineOpsSequence.Add(1), 10)
	request := engineOpsObserverRequest{
		ContractID: engineOpsObserverContractID,
		ActionID:   actionID,
		Deadline:   deadline.UTC().Format(browserTimeFormat),
		Identity:   identity,
		Operation:  operation,
	}
	if err := json.NewEncoder(connection).Encode(request); err != nil {
		return opsPageObservation{}, true, nil
	}
	if unixConnection, ok := connection.(*net.UnixConn); ok {
		if err := unixConnection.CloseWrite(); err != nil {
			return opsPageObservation{}, true, nil
		}
	}
	raw, err := readBoundedLine(
		bufio.NewReaderSize(connection, 64<<10),
		maxEngineOutputBytes,
	)
	if err != nil {
		return opsPageObservation{}, true, nil
	}
	response, err := decodeEngineOpsObserverResponse(raw, actionID, operation)
	if err != nil {
		return opsPageObservation{}, false, browserprotocol.NewOpsObserverError(
			browserprotocol.OpsObserverInternalError,
			"Browser Engine Ops Observer response is invalid",
		)
	}
	if response.Error != nil {
		if response.Error.Code == browserprotocol.OpsObserverBusyError {
			return opsPageObservation{}, true, nil
		}
		return opsPageObservation{}, false, response.Error
	}
	return opsPageObservation{
		PageURL:   response.PageURL,
		PageTitle: response.PageTitle,
		Frame:     response.Frame,
	}, false, nil
}

func processResetFailure(failure *browserprotocol.Failure) *browserprotocol.Failure {
	if failure != nil {
		failure.EngineProcessReset = true
	}
	return failure
}

func (engine *ProcessEngine) Close() error {
	if engine == nil {
		return nil
	}
	engine.mu.Lock()
	defer engine.mu.Unlock()
	engine.closed = true
	return engine.closeProcess(true)
}

func (engine *ProcessEngine) ensureProcess() (*engineProcess, error) {
	if engine.process != nil {
		return engine.process, nil
	}
	command := exec.Command(engine.options.Command[0], engine.options.Command[1:]...) // #nosec G204 -- trusted fixed image configuration, never caller input.
	configureEngineProcess(command)
	command.Env = make([]string, len(engine.options.Environment))
	copy(command.Env, engine.options.Environment)
	diagnosticTarget := engine.options.DiagnosticWriter
	if diagnosticTarget == nil {
		diagnosticTarget = os.Stderr
	}
	diagnostic := newEngineDiagnosticWriter(diagnosticTarget)
	command.Stderr = diagnostic
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}
	if err := command.Start(); err != nil {
		_ = stdin.Close()
		return nil, err
	}
	engine.process = &engineProcess{
		instanceID: processEngineInstanceSequence.Add(1),
		command:    command,
		stdin:      stdin,
		stdoutPipe: stdout,
		stdout:     bufio.NewReaderSize(stdout, 64<<10),
		diagnostic: diagnostic,
	}
	engine.opsReady.Store(engine.opsSocketPath != "")
	return engine.process, nil
}

func (engine *ProcessEngine) resetProcess() error {
	return engine.closeProcess(false)
}

func (engine *ProcessEngine) closeProcess(graceful bool) error {
	engine.opsReady.Store(false)
	process := engine.process
	engine.process = nil
	if process == nil {
		return nil
	}
	_ = process.stdin.Close()
	var killErr error
	if !graceful && process.command.Process != nil {
		killErr = killEngineProcessGroup(process.command)
		if errors.Is(killErr, os.ErrProcessDone) {
			killErr = nil
		}
		_ = process.stdoutPipe.Close()
	}
	waited := make(chan error, 1)
	go func() {
		waited <- process.command.Wait()
	}()
	var waitErr error
	if graceful {
		select {
		case waitErr = <-waited:
		case <-time.After(10 * time.Second):
			killErr = killEngineProcessGroup(process.command)
			if errors.Is(killErr, os.ErrProcessDone) {
				killErr = nil
			}
			_ = process.stdoutPipe.Close()
			select {
			case waitErr = <-waited:
			case <-time.After(engineProcessWaitDelay + time.Second):
				waitErr = errors.New("browser engine process cleanup timed out")
			}
		}
	} else {
		select {
		case waitErr = <-waited:
		case <-time.After(engineProcessWaitDelay + time.Second):
			waitErr = errors.New("browser engine process reset timed out")
		}
	}
	if graceful {
		survivorKillErr := killEngineProcessGroup(process.command)
		if errors.Is(survivorKillErr, os.ErrProcessDone) {
			survivorKillErr = nil
		}
		killErr = errors.Join(killErr, survivorKillErr)
		if errors.Is(waitErr, exec.ErrWaitDelay) {
			waitErr = nil
		}
	}
	process.diagnostic.Flush()
	var exitError *exec.ExitError
	if errors.As(waitErr, &exitError) {
		waitErr = nil
	}
	return errors.Join(killErr, waitErr)
}

var (
	engineURLDiagnostic         = regexp.MustCompile(`(?i)\b(?:https?|wss?)://[^\s"'<>]+`)
	engineSecretFieldDiagnostic = regexp.MustCompile(
		`(?i)\b(authorization|cookie|token|secret|password|api[_-]?key)\s*[:=].*$`,
	)
	engineSecretValueDiagnostic = regexp.MustCompile(
		`(?i)\b(?:sk-[a-z0-9_-]{8,}|ol_(?:agent|user)_[a-z0-9_-]{8,})\b`,
	)
)

type engineDiagnosticWriter struct {
	target    io.Writer
	mu        sync.Mutex
	pending   []byte
	remaining int
}

func newEngineDiagnosticWriter(target io.Writer) *engineDiagnosticWriter {
	return &engineDiagnosticWriter{target: target, remaining: maxEngineLogBytes}
}

func (writer *engineDiagnosticWriter) Write(value []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	originalLength := len(value)
	for len(value) > 0 && writer.remaining > 0 {
		index := bytes.IndexByte(value, '\n')
		if index < 0 {
			writer.appendPending(value)
			break
		}
		writer.appendPending(value[:index])
		writer.flushLocked()
		value = value[index+1:]
	}
	return originalLength, nil
}

func (writer *engineDiagnosticWriter) Flush() {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	writer.flushLocked()
}

func (writer *engineDiagnosticWriter) appendPending(value []byte) {
	available := maxEngineLogLine - len(writer.pending)
	if available <= 0 {
		return
	}
	if len(value) > available {
		value = value[:available]
	}
	writer.pending = append(writer.pending, value...)
}

func (writer *engineDiagnosticWriter) flushLocked() {
	if len(writer.pending) == 0 || writer.remaining <= 0 {
		writer.pending = writer.pending[:0]
		return
	}
	line := strings.Map(func(character rune) rune {
		if character == '\t' || character >= 0x20 {
			return character
		}
		return ' '
	}, string(writer.pending))
	line = engineURLDiagnostic.ReplaceAllString(line, "[url]")
	line = engineSecretFieldDiagnostic.ReplaceAllString(line, "$1=[redacted]")
	line = engineSecretValueDiagnostic.ReplaceAllString(line, "[redacted]")
	raw := []byte("browser-engine: " + line + "\n")
	if len(raw) > writer.remaining {
		raw = raw[:writer.remaining]
	}
	_, _ = writer.target.Write(raw)
	writer.remaining -= len(raw)
	writer.pending = writer.pending[:0]
}

func decodeEngineResponse(
	raw []byte,
	actionID string,
) (engineResponse, *browserprotocol.Failure, string) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var response engineResponse
	if err := decoder.Decode(&response); err != nil {
		return engineResponse{}, invalidEngineOutput(), "response JSON shape is invalid: " + err.Error()
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return engineResponse{}, invalidEngineOutput(), "response contains trailing JSON"
	}
	if response.ContractID != engineContractID || response.ActionID != actionID {
		return engineResponse{}, invalidEngineOutput(), "response authority fields do not match"
	}
	switch response.Status {
	case "ok":
		if response.Observation == nil || response.Error != nil {
			return engineResponse{}, invalidEngineOutput(), "success response payload is invalid"
		}
	case "error":
		if response.Error == nil || response.Observation != nil {
			return engineResponse{}, invalidEngineOutput(), "error response payload is invalid"
		}
	default:
		return engineResponse{}, invalidEngineOutput(), "response status is invalid"
	}
	return response, nil, ""
}

func decodeEngineViewerResponse(
	raw []byte,
	actionID string,
) (engineViewerResponse, *browserprotocol.Failure, string) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var response engineViewerResponse
	if err := decoder.Decode(&response); err != nil {
		return engineViewerResponse{}, invalidEngineOutput(), "viewer response JSON shape is invalid: " + err.Error()
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return engineViewerResponse{}, invalidEngineOutput(), "viewer response contains trailing JSON"
	}
	if response.ContractID != engineViewerContractID ||
		response.ActionID != actionID {
		return engineViewerResponse{}, invalidEngineOutput(), "viewer response authority fields do not match"
	}
	switch response.Status {
	case "ok":
		if response.Error != nil {
			return engineViewerResponse{}, invalidEngineOutput(), "viewer success payload is invalid"
		}
	case "error":
		if response.Error == nil || response.Frame != nil {
			return engineViewerResponse{}, invalidEngineOutput(), "viewer error payload is invalid"
		}
	default:
		return engineViewerResponse{}, invalidEngineOutput(), "viewer response status is invalid"
	}
	return response, nil, ""
}

func decodeEngineOpsObserverResponse(
	raw []byte,
	actionID string,
	operation browserprotocol.OpsObserverOperation,
) (engineOpsObserverResponse, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var response engineOpsObserverResponse
	if err := decoder.Decode(&response); err != nil {
		return engineOpsObserverResponse{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return engineOpsObserverResponse{}, errors.New("Ops Observer response contains trailing JSON")
		}
		return engineOpsObserverResponse{}, err
	}
	if response.ContractID != engineOpsObserverContractID ||
		response.ActionID != actionID {
		return engineOpsObserverResponse{}, errors.New("Ops Observer response identity does not match")
	}
	switch response.Status {
	case "ok":
		if response.Error != nil ||
			(operation == browserprotocol.OpsObserverFrameOperation) != (response.Frame != nil) {
			return engineOpsObserverResponse{}, errors.New("Ops Observer success payload is invalid")
		}
		if response.Frame != nil {
			if failure := response.Frame.Validate(); failure != nil {
				return engineOpsObserverResponse{}, failure
			}
		}
	case "error":
		if response.Error == nil || response.Frame != nil ||
			response.PageURL != "" || response.PageTitle != "" {
			return engineOpsObserverResponse{}, errors.New("Ops Observer error payload is invalid")
		}
		switch response.Error.Code {
		case browserprotocol.OpsObserverRunNotActive,
			browserprotocol.OpsObserverBusyError,
			browserprotocol.OpsObserverInternalError:
		default:
			return engineOpsObserverResponse{}, errors.New("Ops Observer error code is invalid")
		}
		if response.Error.Message == "" || len(response.Error.Message) > 300 {
			return engineOpsObserverResponse{}, errors.New("Ops Observer error message is invalid")
		}
	default:
		return engineOpsObserverResponse{}, errors.New("Ops Observer response status is invalid")
	}
	return response, nil
}

var errEngineOutputTooLarge = errors.New("browser engine output is too large")

func readBoundedLine(reader *bufio.Reader, limit int) ([]byte, error) {
	var output []byte
	for {
		fragment, more, err := reader.ReadLine()
		if err != nil {
			return nil, err
		}
		if len(output)+len(fragment) > limit {
			return nil, errEngineOutputTooLarge
		}
		output = append(output, fragment...)
		if !more {
			return output, nil
		}
	}
}

func writeAllProcess(writer io.Writer, value []byte) error {
	for len(value) > 0 {
		written, err := writer.Write(value)
		if written < 0 || written > len(value) {
			return io.ErrShortWrite
		}
		value = value[written:]
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func engineEnvironmentValue(environment []string, expected string) string {
	for _, item := range environment {
		key, value, found := strings.Cut(item, "=")
		if found && key == expected {
			return value
		}
	}
	return ""
}

func validateEngineEnvironment(environment []string) error {
	allowed := map[string]bool{
		"HOME":                                                 true,
		"LANG":                                                 true,
		"LC_ALL":                                               true,
		"NO_PROXY":                                             true,
		"OPENLINKER_BROWSER_EGRESS_PROXY":                      true,
		"OPENLINKER_BROWSER_PROFILE_DIR":                       true,
		"OPENLINKER_BROWSER_EXECUTABLE_PATH":                   true,
		"OPENLINKER_BROWSER_ENGINE":                            true,
		"OPENLINKER_BROWSER_DISTRIBUTION":                      true,
		"OPENLINKER_BROWSER_VERSION":                           true,
		"OPENLINKER_BROWSER_LOCALE":                            true,
		"OPENLINKER_BROWSER_TIMEZONE":                          true,
		"OPENLINKER_BROWSER_FONT_CONTRACT_VERSION":             true,
		"OPENLINKER_BROWSER_FONT_MANIFEST_SHA256":              true,
		"OPENLINKER_BROWSER_PROFILE_GENERATION":                true,
		"OPENLINKER_BROWSER_MAX_ACTIONS_PER_ORIGIN_MINUTE":     true,
		"OPENLINKER_BROWSER_MAX_NAVIGATIONS_PER_ORIGIN_MINUTE": true,
		"PLAYWRIGHT_BROWSERS_PATH":                             true,
		"TMPDIR":                                               true,
		"TZ":                                                   true,
		"OPENLINKER_NATIVE_CHROME_BINARY":                      true,
		"OPENLINKER_NATIVE_CHROME_EXTENSION_ROOT":              true,
		"OPENLINKER_NATIVE_CHROME_EXTENSION_ID":                true,
		"OPENLINKER_NATIVE_CHROME_EXTENSION_VERSION":           true,
		"OPENLINKER_NATIVE_CHROME_ACTIVATION_PATH":             true,
		"OPENLINKER_NATIVE_CHROME_HOST":                        true,
		"OPENLINKER_NATIVE_CHROME_PROTOCOL":                    true,
		"OPENLINKER_NATIVE_CHROME_ASSET_MANIFEST_SHA256":       true,
		"OPENLINKER_NATIVE_CHROME_ENABLED":                     true,
		"OPENLINKER_NATIVE_CHROME_REQUIRE_ORIGIN":              true,
		"OPENLINKER_NATIVE_CHROME_SOCKET":                      true,
		"OPENLINKER_BROWSER_OPS_OBSERVER_ENABLED":              true,
		"OPENLINKER_BROWSER_ENGINE_OPS_SOCKET":                 true,
	}
	seen := make(map[string]bool, len(environment))
	for _, item := range environment {
		key, value, found := strings.Cut(item, "=")
		if !found || key == "" || !allowed[key] || seen[key] || strings.ContainsRune(value, 0) {
			return errors.New("browser engine environment contains a forbidden entry")
		}
		if key == "NO_PROXY" && value != "" {
			return errors.New("browser engine NO_PROXY must be empty")
		}
		switch key {
		case "HOME", "TMPDIR", "OPENLINKER_BROWSER_PROFILE_DIR", "PLAYWRIGHT_BROWSERS_PATH",
			"OPENLINKER_BROWSER_EXECUTABLE_PATH",
			"OPENLINKER_NATIVE_CHROME_BINARY", "OPENLINKER_NATIVE_CHROME_EXTENSION_ROOT",
			"OPENLINKER_NATIVE_CHROME_HOST", "OPENLINKER_NATIVE_CHROME_SOCKET",
			"OPENLINKER_BROWSER_ENGINE_OPS_SOCKET":
			if !filepath.IsAbs(value) || filepath.Clean(value) != value {
				return errors.New("browser engine environment path must be absolute and clean")
			}
		case "OPENLINKER_BROWSER_EGRESS_PROXY":
			proxy, err := url.Parse(value)
			if err != nil || proxy.Scheme != "http" || proxy.Host == "" || proxy.User != nil ||
				proxy.Path != "" || proxy.RawQuery != "" || proxy.Fragment != "" {
				return errors.New("browser engine egress proxy must be an HTTP origin without credentials")
			}
		case "OPENLINKER_NATIVE_CHROME_EXTENSION_ID":
			if !validExtensionID(value) {
				return errors.New("browser engine native Chrome extension ID is invalid")
			}
		case "OPENLINKER_NATIVE_CHROME_EXTENSION_VERSION":
			if !validLockedVersion(value) {
				return errors.New("browser engine native Chrome extension version is invalid")
			}
		case "OPENLINKER_NATIVE_CHROME_ACTIVATION_PATH":
			if value != openLinkerActivationPath {
				return errors.New("browser engine native Chrome activation path is invalid")
			}
		case "OPENLINKER_NATIVE_CHROME_PROTOCOL":
			if !validBoundedOpaque(value, 64) {
				return errors.New("browser engine native Chrome protocol is invalid")
			}
		case "OPENLINKER_NATIVE_CHROME_ASSET_MANIFEST_SHA256":
			if len(value) != 64 || strings.Trim(value, "0123456789abcdef") != "" {
				return errors.New("browser engine native Chrome asset digest is invalid")
			}
		case "OPENLINKER_NATIVE_CHROME_ENABLED", "OPENLINKER_NATIVE_CHROME_REQUIRE_ORIGIN":
			if value != "true" {
				return errors.New("browser engine native Chrome gate must be enabled")
			}
		case "OPENLINKER_BROWSER_OPS_OBSERVER_ENABLED":
			if value != "true" {
				return errors.New("browser engine Ops Observer flag must be enabled")
			}
		}
		seen[key] = true
	}
	if seen["OPENLINKER_BROWSER_OPS_OBSERVER_ENABLED"] !=
		seen["OPENLINKER_BROWSER_ENGINE_OPS_SOCKET"] {
		return errors.New("browser engine Ops Observer flag and socket must be configured together")
	}
	return nil
}

func normalizeEngineFailure(
	failure *browserprotocol.Failure,
) (*browserprotocol.Failure, *browserprotocol.Failure) {
	if failure == nil {
		rejection := browserprotocol.NewFailure(
			browserprotocol.ErrorOutputInvalid,
			"browser engine failure is missing",
			false,
		)
		return invalidEngineOutput(), rejection
	}
	if rejection := browserprotocol.ValidateFailure(failure); rejection != nil {
		return invalidEngineOutput(), rejection
	}
	if failure.BlockedClickNavigationAttemptsRemaining != nil ||
		failure.BlockedClickRunAttemptsRemaining != nil {
		rejection := browserprotocol.NewFailure(
			browserprotocol.ErrorOutputInvalid,
			"browser engine failure contains supervisor-owned retry evidence",
			false,
		)
		return invalidEngineOutput(), rejection
	}
	if !allowedEngineErrorCode(failure.Code) {
		rejection := browserprotocol.NewFailure(
			browserprotocol.ErrorOutputInvalid,
			"browser engine failure code is not allowed",
			false,
		)
		return invalidEngineOutput(), rejection
	}
	if failure.TargetCategory != "" &&
		failure.Code != browserprotocol.ErrorHighImpactActionBlocked {
		rejection := browserprotocol.NewFailure(
			browserprotocol.ErrorOutputInvalid,
			"browser engine failure contains unexpected target evidence",
			false,
		)
		return invalidEngineOutput(), rejection
	}
	normalized := browserprotocol.NewFailure(failure.Code, failure.Message, failure.Recoverable)
	if failure.ActionIndex != nil {
		index := *failure.ActionIndex
		normalized.ActionIndex = &index
	}
	normalized.TargetCategory = failure.TargetCategory
	normalized.PageStateID = failure.PageStateID
	normalized.NavigationGeneration = failure.NavigationGeneration
	normalized.SiteOutcome = failure.SiteOutcome
	if failure.RetryAfterMS != nil {
		retryAfterMS := *failure.RetryAfterMS
		normalized.RetryAfterMS = &retryAfterMS
	}
	normalized.ClassifierRulesVersion = failure.ClassifierRulesVersion
	if failure.ConsecutiveAccessDenials != nil {
		denials := *failure.ConsecutiveAccessDenials
		normalized.ConsecutiveAccessDenials = &denials
	}
	normalized.OriginBlockedForAttachment = failure.OriginBlockedForAttachment
	normalized.ChallengeReleaseUnavailable = failure.ChallengeReleaseUnavailable
	if failure.RetrySameAction != nil {
		value := *failure.RetrySameAction
		normalized.RetrySameAction = &value
	}
	if failure.AttachmentUsable != nil {
		value := *failure.AttachmentUsable
		normalized.AttachmentUsable = &value
	}
	if failure.FreshObservationRequired != nil {
		value := *failure.FreshObservationRequired
		normalized.FreshObservationRequired = &value
	}
	normalized.MutationOutcomeReason = failure.MutationOutcomeReason
	normalized.MutationRequestsObserved = failure.MutationRequestsObserved
	if failure.AttemptedUnits != nil {
		value := *failure.AttemptedUnits
		normalized.AttemptedUnits = &value
	}
	if failure.UndispatchedUnits != nil {
		value := *failure.UndispatchedUnits
		normalized.UndispatchedUnits = &value
	}
	if failure.CompletedActions != nil {
		value := *failure.CompletedActions
		normalized.CompletedActions = &value
	}
	normalized.ObservedOrigin = failure.ObservedOrigin
	if failure.BrowserMutationOrigins != nil {
		normalized.BrowserMutationOrigins = append(
			[]string{},
			failure.BrowserMutationOrigins...,
		)
	}
	return normalized, nil
}

func writeEngineRejectionDiagnostic(
	process *engineProcess,
	stage string,
	failure *browserprotocol.Failure,
	engineCode string,
) {
	if process == nil || process.diagnostic == nil || failure == nil {
		return
	}
	line := "supervisor rejected " + stage + ": " + failure.Message
	if engineCode != "" {
		line += " (engine_code=" + engineCode + ")"
	}
	_, _ = process.diagnostic.Write([]byte(line + "\n"))
}

func allowedEngineErrorCode(code browserprotocol.ErrorCode) bool {
	switch code {
	case browserprotocol.ErrorOutputTooLarge,
		browserprotocol.ErrorRuntimeUnavailable,
		browserprotocol.ErrorEngineUnavailable,
		browserprotocol.ErrorEgressUnavailable,
		browserprotocol.ErrorTargetBlocked,
		browserprotocol.ErrorProfileLocked,
		browserprotocol.ErrorProfileCorrupt,
		browserprotocol.ErrorUserActionRequired,
		browserprotocol.ErrorHighImpactActionBlocked,
		browserprotocol.ErrorMutationOriginBlocked,
		browserprotocol.ErrorMutationOutcomeUnknown,
		browserprotocol.ErrorAccessDenied,
		browserprotocol.ErrorRateLimited,
		browserprotocol.ErrorChallengeSuspected,
		browserprotocol.ErrorChallengeRequired,
		browserprotocol.ErrorOriginRateLimited,
		browserprotocol.ErrorActionLimitExceeded,
		browserprotocol.ErrorActionRejected,
		browserprotocol.ErrorCanceled,
		browserprotocol.ErrorOutputInvalid,
		browserprotocol.ErrorInternal:
		return true
	default:
		return false
	}
}

func contextFailure(err error) *browserprotocol.Failure {
	if errors.Is(err, context.DeadlineExceeded) {
		return browserprotocol.NewFailure(
			browserprotocol.ErrorDeadlineExceeded,
			"browser action deadline elapsed",
			true,
		)
	}
	return browserprotocol.NewFailure(
		browserprotocol.ErrorCanceled,
		"browser action was canceled",
		true,
	)
}

func runtimeUnavailable(message string) *browserprotocol.Failure {
	return browserprotocol.NewFailure(
		browserprotocol.ErrorRuntimeUnavailable,
		message,
		true,
	)
}

func invalidEngineOutput() *browserprotocol.Failure {
	return browserprotocol.NewFailure(
		browserprotocol.ErrorOutputInvalid,
		"browser engine response is invalid",
		false,
	)
}

const browserTimeFormat = "2006-01-02T15:04:05.000000000Z07:00"
