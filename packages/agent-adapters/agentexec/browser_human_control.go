package agentexec

import (
	"context"
	"errors"
	"sync"
	"time"

	openlinker "github.com/OpenLinker-ai/openlinker-go"
	"github.com/OpenLinker-ai/openlinker-plugin/packages/agent-adapters/browserextension"
	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserclient"
	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserplugin"
	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserprotocol"
)

const browserViewerFrameInterval = 500 * time.Millisecond

type browserHumanControl struct {
	lease      *browserRunLease
	extensions *openlinker.RuntimeExtensions
	emit       func(string, any) error
	// observation is demultiplexed from the same command channel. Extensions
	// share one channel, so a second reader would steal commands from this one;
	// the two capabilities stay separate in state and lifecycle, not in wiring.
	observation *browserObservation

	mu          sync.Mutex
	paused      bool
	resumed     chan struct{}
	terminal    *browserprotocol.Failure
	frameCancel context.CancelFunc
}

type browserHumanControlExecutor struct {
	control  *browserHumanControl
	delegate browserplugin.Executor
}

func newBrowserHumanControl(
	lease *browserRunLease,
	extensions *openlinker.RuntimeExtensions,
	emit func(string, any) error,
) *browserHumanControl {
	return &browserHumanControl{
		lease:       lease,
		extensions:  extensions,
		emit:        emit,
		observation: newBrowserObservation(lease, extensions),
		resumed:     make(chan struct{}),
	}
}

func (control *browserHumanControl) executor() (
	browserplugin.Executor,
	error,
) {
	delegate, err := control.lease.browserClient()
	if err != nil {
		return nil, err
	}
	return &browserHumanControlExecutor{
		control:  control,
		delegate: delegate,
	}, nil
}

func (executor *browserHumanControlExecutor) Execute(
	ctx context.Context,
	action browserprotocol.Action,
) (browserprotocol.Observation, *browserprotocol.Failure) {
	observation, failure := executor.delegate.Execute(ctx, action)
	if failure == nil ||
		failure.Code != browserprotocol.ErrorChallengeRequired {
		return observation, failure
	}
	if executor.control.extensions == nil || !failure.HumanControlAvailable {
		_, _ = executor.delegate.Execute(ctx, browserprotocol.Action{
			Kind: browserprotocol.ActionClose,
		})
		failure.HumanControlAvailable = false
		return browserprotocol.Observation{}, failure
	}
	if waitFailure := executor.control.pauseAndWait(ctx); waitFailure != nil {
		return browserprotocol.Observation{}, waitFailure
	}
	client, err := executor.control.lease.browserClient()
	if err != nil {
		return browserprotocol.Observation{}, browserprotocol.NewFailure(
			browserprotocol.ErrorRuntimeUnavailable,
			"browser attachment could not resume",
			true,
		)
	}
	return client.Execute(ctx, browserprotocol.Action{
		Kind:        browserprotocol.ActionScreenshot,
		Observation: browserprotocol.ObservationSemantic,
	})
}

func (control *browserHumanControl) pauseAndWait(
	ctx context.Context,
) *browserprotocol.Failure {
	control.mu.Lock()
	first := !control.paused
	if first {
		control.paused = true
		control.resumed = make(chan struct{})
		control.terminal = nil
	}
	resumed := control.resumed
	control.mu.Unlock()
	if first {
		identity, err := control.lease.Pause()
		if err != nil {
			control.mu.Lock()
			control.paused = false
			close(resumed)
			control.mu.Unlock()
			return browserprotocol.NewFailure(
				browserprotocol.ErrorRuntimeUnavailable,
				"browser attachment could not pause",
				false,
			)
		}
		emitBrowserControlLifecycle(
			control.emit,
			"paused",
			"user_action_required",
			identity,
		)
	}
	select {
	case <-ctx.Done():
		return browserprotocol.NewFailure(
			browserprotocol.ErrorCanceled,
			"browser human-control wait was canceled",
			false,
		)
	case <-resumed:
		control.mu.Lock()
		terminal := control.terminal
		control.mu.Unlock()
		return terminal
	}
}

func (control *browserHumanControl) run(ctx context.Context) {
	if control == nil || control.extensions == nil {
		return
	}
	commands := control.extensions.Commands()
	for {
		select {
		case <-ctx.Done():
			control.stopFrames()
			control.observation.stop()
			return
		case extension, ok := <-commands:
			if !ok {
				control.stopFrames()
				control.observation.stop()
				return
			}
			if extension.Type == browserextension.ObserverBridgeCommandType {
				control.observation.handleCommand(
					ctx,
					extension.Payload,
					extension.AttemptIdentity,
				)
				continue
			}
			if extension.Type != browserextension.RuntimeViewerCommandMessage {
				continue
			}
			command, err := browserextension.DecodeRuntimeViewerCommand(
				extension.Payload,
			)
			if err != nil ||
				command.AttemptIdentity != extension.AttemptIdentity {
				continue
			}
			control.handleCommand(ctx, command)
		}
	}
}

func (control *browserHumanControl) handleCommand(
	parent context.Context,
	command browserextension.RuntimeViewerCommand,
) {
	identity, err := control.lease.identitySnapshot()
	if err != nil ||
		command.AttemptIdentity.RunID != identity.RunID ||
		command.AttemptIdentity.AgentID != identity.AgentID ||
		command.BrowserSessionID != identity.BrowserSessionID ||
		command.SessionEpoch != identity.SessionEpoch ||
		command.AttachmentID != identity.AttachmentID ||
		command.PreviousControlEpoch != identity.ControlEpoch {
		return
	}
	ctx := parent
	cancel := func() {}
	if !command.DeadlineAt.IsZero() {
		ctx, cancel = context.WithDeadline(parent, command.DeadlineAt)
	}
	defer cancel()
	switch command.Action {
	case browserextension.RuntimeViewerClaim:
		control.claim(ctx, command)
	case browserextension.RuntimeViewerInput:
		control.input(ctx, command)
	case browserextension.RuntimeViewerRelease:
		control.release(ctx, command)
	case browserextension.RuntimeViewerResume:
		control.resume(command)
	case browserextension.RuntimeViewerTerminate:
		control.terminate()
	}
}

func (control *browserHumanControl) claim(
	ctx context.Context,
	command browserextension.RuntimeViewerCommand,
) {
	identity, err := control.lease.transitionController(
		browserprotocol.ControllerNone,
		browserprotocol.ControllerHuman,
		command.ControlEpoch,
	)
	if err != nil {
		return
	}
	client, err := control.viewerClient()
	if err != nil {
		return
	}
	if _, failure := client.ExecuteViewer(
		ctx,
		browserprotocol.ViewerOperationEnter,
		nil,
	); failure != nil {
		return
	}
	emitBrowserControlLifecycle(control.emit, "human", "claimed", identity)
	control.startFrames(command, client)
}

func (control *browserHumanControl) input(
	ctx context.Context,
	command browserextension.RuntimeViewerCommand,
) {
	if command.Input == nil ||
		command.ControlEpoch != command.PreviousControlEpoch {
		return
	}
	client, err := control.viewerClient()
	if err != nil {
		return
	}
	if failure := command.Input.Validate(); failure != nil {
		return
	}
	_, _ = client.ExecuteViewer(
		ctx,
		browserprotocol.ViewerOperationInput,
		command.Input,
	)
}

func (control *browserHumanControl) release(
	ctx context.Context,
	command browserextension.RuntimeViewerCommand,
) {
	client, err := control.viewerClient()
	if err != nil {
		return
	}
	if _, failure := client.ExecuteViewer(
		ctx,
		browserprotocol.ViewerOperationExit,
		nil,
	); failure != nil {
		return
	}
	control.stopFrames()
	identity, err := control.lease.transitionController(
		browserprotocol.ControllerHuman,
		browserprotocol.ControllerNone,
		command.ControlEpoch,
	)
	if err == nil {
		emitBrowserControlLifecycle(control.emit, "released", "released", identity)
	}
}

func (control *browserHumanControl) resume(
	command browserextension.RuntimeViewerCommand,
) {
	identity, err := control.lease.transitionController(
		browserprotocol.ControllerNone,
		browserprotocol.ControllerAgent,
		command.ControlEpoch,
	)
	if err != nil {
		return
	}
	control.mu.Lock()
	resumed := control.resumed
	if control.paused {
		control.paused = false
		close(resumed)
	}
	control.mu.Unlock()
	emitBrowserControlLifecycle(control.emit, "resumed", "agent", identity)
}

func (control *browserHumanControl) terminate() {
	control.stopFrames()
	runtimeErr := control.lease.closeBrowserRuntime()
	leaseErr := control.lease.Close()
	failure := browserprotocol.NewFailure(
		browserprotocol.ErrorDeadlineExceeded,
		"browser human-control lease expired",
		false,
	)
	if errors.Join(runtimeErr, leaseErr) != nil {
		failure = browserprotocol.NewFailure(
			browserprotocol.ErrorRuntimeUnavailable,
			"browser human-control cleanup failed closed",
			false,
		)
	}
	control.mu.Lock()
	control.terminal = failure
	if control.paused {
		control.paused = false
		close(control.resumed)
	}
	control.mu.Unlock()
}

func (control *browserHumanControl) viewerClient() (
	*browserclient.Client,
	error,
) {
	control.lease.mu.Lock()
	if control.lease.closed {
		control.lease.mu.Unlock()
		return nil, errors.New("Browser lease is closed")
	}
	control.lease.runtimeUsed = true
	runPath := control.lease.runPath
	config := control.lease.config
	control.lease.mu.Unlock()
	return browserclient.New(browserclient.Config{
		SocketPath:     config.BrowserSocket,
		CredentialFile: config.BrowserCredentialFile,
		LeaseFile:      runPath,
	})
}

func (control *browserHumanControl) startFrames(
	command browserextension.RuntimeViewerCommand,
	client *browserclient.Client,
) {
	control.stopFrames()
	frameContext, cancel := context.WithCancel(context.Background())
	control.mu.Lock()
	control.frameCancel = cancel
	control.mu.Unlock()
	go func() {
		ticker := time.NewTicker(browserViewerFrameInterval)
		defer ticker.Stop()
		var sequence uint64
		for {
			frameCtx, frameCancel := context.WithTimeout(frameContext, 5*time.Second)
			frame, failure := client.ExecuteViewer(
				frameCtx,
				browserprotocol.ViewerOperationFrame,
				nil,
			)
			frameCancel()
			if failure != nil || frame == nil {
				return
			}
			sequence++
			framePayload := browserextension.RuntimeViewerFrame{
				AttemptIdentity:  command.AttemptIdentity,
				BrowserSessionID: command.BrowserSessionID,
				SessionEpoch:     command.SessionEpoch,
				AttachmentID:     command.AttachmentID,
				ControlEpoch:     command.ControlEpoch,
				FrameSeq:         sequence,
				MIMEType:         frame.MIMEType,
				Data:             frame.Data,
				Width:            frame.Width,
				Height:           frame.Height,
			}
			raw, err := browserextension.EncodeRuntimeViewerFrame(framePayload)
			if err != nil {
				return
			}
			reply, err := control.extensions.Publish(
				frameContext,
				openlinker.RuntimeExtensionRequest{
					Type:    browserextension.RuntimeViewerFrameMessage,
					Payload: raw,
				},
			)
			if err != nil ||
				reply == nil ||
				reply.Type != browserextension.RuntimeViewerFrameAckMessage {
				return
			}
			ack, err := browserextension.DecodeRuntimeViewerFrameAck(
				reply.Payload,
			)
			if err != nil ||
				ack.AttemptIdentity != framePayload.AttemptIdentity ||
				ack.ControlEpoch != framePayload.ControlEpoch ||
				ack.FrameSeq != framePayload.FrameSeq {
				return
			}
			select {
			case <-frameContext.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func (control *browserHumanControl) stopFrames() {
	if control == nil {
		return
	}
	control.mu.Lock()
	cancel := control.frameCancel
	control.frameCancel = nil
	control.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func emitBrowserControlLifecycle(
	emit func(string, any) error,
	phase string,
	reason string,
	identity browserprotocol.Identity,
) {
	if emit == nil {
		return
	}
	_ = emit("run.browser.lifecycle", map[string]any{
		"phase":              phase,
		"reason":             reason,
		"execution_profile":  "browser",
		"runtime":            "isolated",
		"browser_session_id": identity.BrowserSessionID,
		"session_epoch":      identity.SessionEpoch,
		"attachment_id":      identity.AttachmentID,
		"control_epoch":      identity.ControlEpoch,
		"controller":         identity.Controller,
	})
}
