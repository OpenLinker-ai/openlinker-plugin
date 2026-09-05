//go:build !windows

package browserruntime

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserprotocol"
)

func (server *Server) handleConnection(parent context.Context, connection *net.UnixConn) {
	now := server.options.Now().UTC()
	_ = connection.SetDeadline(now.Add(server.options.IOTimeout))

	raw, failure := server.readRequest(connection)
	if failure != nil {
		server.writeResponse(connection, browserprotocol.ErrorResponse("", failure))
		return
	}
	var envelope struct {
		ContractID string `json:"contract_id"`
	}
	if err := json.Unmarshal(raw, &envelope); err == nil &&
		envelope.ContractID != "" {
		switch envelope.ContractID {
		case browserprotocol.ViewerContractID:
			server.handleViewerConnection(parent, connection, raw, now)
			return
		case browserprotocol.HealthContractID:
			server.handleHealthConnection(connection, raw)
			return
		}
	}
	request, failure := server.decodeRequest(bytes.NewReader(raw))
	if failure != nil {
		server.writeResponse(connection, browserprotocol.ErrorResponse(request.RequestID, failure))
		return
	}
	if subtle.ConstantTimeCompare(
		[]byte(request.ChannelCredential),
		[]byte(server.options.ChannelCredential),
	) != 1 {
		server.writeResponse(connection, browserprotocol.ErrorResponse(
			request.RequestID,
			browserprotocol.NewFailure(browserprotocol.ErrorUnauthorized, "browser channel credential is invalid", false),
		))
		return
	}
	if failure := request.Validate(now); failure != nil {
		server.writeResponse(connection, browserprotocol.ErrorResponse(request.RequestID, failure))
		return
	}
	var revoker LeaseRevoker
	alreadyRevoked := false
	if request.Action.Kind == browserprotocol.ActionClose {
		var ok bool
		revoker, ok = server.options.Lease.(LeaseRevoker)
		if !ok {
			server.writeResponse(connection, browserprotocol.ErrorResponse(
				request.RequestID,
				browserprotocol.NewFailure(
					browserprotocol.ErrorRuntimeUnavailable,
					"browser runtime lease does not support durable closure",
					false,
				),
			))
			return
		}
		alreadyRevoked, failure = revoker.IsRevoked(request.Identity)
		if failure != nil {
			server.writeResponse(connection, browserprotocol.ErrorResponse(request.RequestID, failure))
			return
		}
	}
	if !alreadyRevoked {
		if failure := server.options.Lease.Validate(request.Identity); failure != nil {
			server.writeResponse(connection, browserprotocol.ErrorResponse(request.RequestID, failure))
			return
		}
	}
	if failure := server.reserveRequest(
		request.Identity,
		request.RequestID,
		request.Action,
	); failure != nil {
		server.writeResponse(connection, browserprotocol.ErrorResponse(request.RequestID, failure))
		return
	}
	if revoker != nil && !alreadyRevoked {
		if failure := revoker.Revoke(request.Identity); failure != nil {
			server.writeResponse(connection, browserprotocol.ErrorResponse(request.RequestID, failure))
			return
		}
	}

	_ = connection.SetDeadline(request.Deadline)
	actionContext, cancel := context.WithDeadline(parent, request.Deadline)
	defer cancel()
	observation, failure := server.options.Engine.Execute(actionContext, request.Identity, request.Action)
	if failure == nil && actionContext.Err() != nil {
		failure = browserprotocol.NewFailure(
			browserprotocol.ErrorDeadlineExceeded,
			"browser action deadline elapsed",
			true,
		)
	}
	_ = connection.SetWriteDeadline(server.options.Now().UTC().Add(server.options.IOTimeout))
	if failure != nil {
		if validationFailure := browserprotocol.ValidateFailure(failure); validationFailure != nil {
			failure = validationFailure
		} else if request.Action.Kind == browserprotocol.ActionClick &&
			failure.Code == browserprotocol.ErrorHighImpactActionBlocked {
			failure = server.recordBlockedClick(request.Identity, failure)
		} else if failure.EngineProcessReset {
			server.resetClickNavigationBaseline(request.Identity)
		}
		if failure.Code == browserprotocol.ErrorChallengeRequired {
			if server.options.HumanControlAvailable {
				failure.HumanControlAvailable = true
			} else {
				if terminalFailure := server.closeRequiredChallenge(
					parent,
					request.Identity,
				); terminalFailure != nil {
					failure = terminalFailure
				}
			}
		}
		server.writeResponse(connection, browserprotocol.ErrorResponse(request.RequestID, failure))
		return
	}
	var observationFailure *browserprotocol.Failure
	if request.Action.Kind == browserprotocol.ActionClose {
		observationFailure = observation.ValidateClosed()
	} else {
		observationFailure = observation.ValidateEngine()
		if observationFailure == nil {
			observationFailure = server.recordEngineObservation(
				request.Identity,
				observation,
			)
		}
	}
	if observationFailure != nil {
		server.writeResponse(
			connection,
			browserprotocol.ErrorResponse(request.RequestID, observationFailure),
		)
		return
	}
	if request.Action.Kind == browserprotocol.ActionCheckpoint {
		server.resetClickNavigationBaseline(request.Identity)
	}
	server.writeResponse(connection, browserprotocol.SuccessResponse(request.RequestID, observation))
}

func (server *Server) handleHealthConnection(
	connection *net.UnixConn,
	raw []byte,
) {
	if len(raw) > browserprotocol.MaxHealthRequestBytes {
		_ = server.writeHealthResponse(connection, browserprotocol.HealthErrorResponse(
			"",
			browserprotocol.HealthReasonInvalidRequest,
		))
		return
	}
	request, err := browserprotocol.DecodeHealthRequest(raw)
	if err != nil || request.Validate() != nil {
		_ = server.writeHealthResponse(connection, browserprotocol.HealthErrorResponse(
			request.RequestID,
			browserprotocol.HealthReasonInvalidRequest,
		))
		return
	}
	if subtle.ConstantTimeCompare(
		[]byte(request.ChannelCredential),
		[]byte(server.options.ChannelCredential),
	) != 1 {
		_ = server.writeHealthResponse(connection, browserprotocol.HealthErrorResponse(
			request.RequestID,
			browserprotocol.HealthReasonUnauthorized,
		))
		return
	}
	_ = server.writeHealthResponse(
		connection,
		browserprotocol.HealthSuccessResponse(request.RequestID),
	)
}

func (server *Server) handleViewerConnection(
	parent context.Context,
	connection *net.UnixConn,
	raw []byte,
	now time.Time,
) {
	request, err := browserprotocol.DecodeViewerRequest(raw)
	if err != nil {
		server.writeViewerResponse(connection, browserprotocol.ViewerErrorResponse(
			"",
			browserprotocol.NewFailure(
				browserprotocol.ErrorProtocolInvalid,
				"browser viewer request is not valid",
				false,
			),
		))
		return
	}
	if subtle.ConstantTimeCompare(
		[]byte(request.ChannelCredential),
		[]byte(server.options.ChannelCredential),
	) != 1 {
		server.writeViewerResponse(connection, browserprotocol.ViewerErrorResponse(
			request.RequestID,
			browserprotocol.NewFailure(
				browserprotocol.ErrorUnauthorized,
				"browser viewer channel credential is invalid",
				false,
			),
		))
		return
	}
	if failure := request.Validate(now); failure != nil {
		server.writeViewerResponse(
			connection,
			browserprotocol.ViewerErrorResponse(request.RequestID, failure),
		)
		return
	}
	if failure := server.options.Lease.Validate(request.Identity); failure != nil {
		server.writeViewerResponse(
			connection,
			browserprotocol.ViewerErrorResponse(request.RequestID, failure),
		)
		return
	}
	if failure := server.reserveViewerRequest(
		request.Identity,
		request.RequestID,
	); failure != nil {
		server.writeViewerResponse(
			connection,
			browserprotocol.ViewerErrorResponse(request.RequestID, failure),
		)
		return
	}
	engine, ok := server.options.Engine.(ViewerEngine)
	if !ok {
		server.writeViewerResponse(connection, browserprotocol.ViewerErrorResponse(
			request.RequestID,
			browserprotocol.NewFailure(
				browserprotocol.ErrorViewerUnavailable,
				"browser runtime Viewer is unavailable",
				false,
			),
		))
		return
	}
	_ = connection.SetDeadline(request.Deadline)
	viewerContext, cancel := context.WithDeadline(parent, request.Deadline)
	defer cancel()
	frame, failure := engine.ExecuteViewer(
		viewerContext,
		request.Identity,
		request.Operation,
		request.Input,
	)
	if failure == nil && viewerContext.Err() != nil {
		failure = browserprotocol.NewFailure(
			browserprotocol.ErrorDeadlineExceeded,
			"browser viewer deadline elapsed",
			true,
		)
	}
	if failure != nil {
		if validationFailure := browserprotocol.ValidateFailure(failure); validationFailure != nil {
			failure = validationFailure
		}
		server.writeViewerResponse(
			connection,
			browserprotocol.ViewerErrorResponse(request.RequestID, failure),
		)
		return
	}
	if frame != nil {
		if validationFailure := frame.Validate(); validationFailure != nil {
			server.writeViewerResponse(
				connection,
				browserprotocol.ViewerErrorResponse(
					request.RequestID,
					validationFailure,
				),
			)
			return
		}
	}
	server.writeViewerResponse(
		connection,
		browserprotocol.ViewerSuccessResponse(request.RequestID, frame),
	)
}

func (server *Server) reserveViewerRequest(
	identity browserprotocol.Identity,
	requestID string,
) *browserprotocol.Failure {
	server.seenMu.Lock()
	defer server.seenMu.Unlock()
	scope := attachmentRequestScope(identity)
	if scope != server.seenScope {
		clear(server.seen)
		server.seenScope = scope
		server.actionCount = 0
		server.viewerCount = 0
		server.closeCount = 0
		server.terminal = false
	}
	if _, found := server.seen[requestID]; found {
		return browserprotocol.NewFailure(
			browserprotocol.ErrorRequestReplayed,
			"browser viewer request_id has already been used",
			false,
		)
	}
	if server.viewerCount >= maxViewerRequestsPerEpoch {
		return browserprotocol.NewFailure(
			browserprotocol.ErrorActionLimitExceeded,
			"browser Viewer reached its request limit",
			false,
		)
	}
	server.seen[requestID] = struct{}{}
	server.viewerCount++
	return nil
}

func (server *Server) closeRequiredChallenge(
	parent context.Context,
	identity browserprotocol.Identity,
) *browserprotocol.Failure {
	revoker, ok := server.options.Lease.(LeaseRevoker)
	if !ok {
		return browserprotocol.NewFailure(
			browserprotocol.ErrorRuntimeUnavailable,
			"browser runtime cannot durably fence a required challenge",
			false,
		)
	}
	revoked, failure := revoker.IsRevoked(identity)
	if failure != nil {
		return failure
	}
	if !revoked {
		if failure := revoker.Revoke(identity); failure != nil {
			return failure
		}
	}
	closeContext, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	_, _ = server.options.Engine.Execute(
		closeContext,
		identity,
		browserprotocol.Action{Kind: browserprotocol.ActionClose},
	)
	server.seenMu.Lock()
	server.terminal = true
	server.seenMu.Unlock()
	return nil
}

func (server *Server) reserveRequest(
	identity browserprotocol.Identity,
	requestID string,
	action browserprotocol.Action,
) *browserprotocol.Failure {
	server.seenMu.Lock()
	defer server.seenMu.Unlock()
	scope := attachmentRequestScope(identity)
	if scope != server.seenScope {
		clear(server.seen)
		server.seenScope = scope
		server.actionCount = 0
		server.viewerCount = 0
		server.closeCount = 0
		server.terminal = false
	}
	server.prepareClickScopeLocked(identity)
	if _, found := server.seen[requestID]; found {
		return browserprotocol.NewFailure(
			browserprotocol.ErrorRequestReplayed,
			"browser request_id has already been used",
			false,
		)
	}
	if action.Kind == browserprotocol.ActionClose {
		if server.closeCount >= maxCloseAttemptsPerAttachment {
			server.terminal = true
			return browserprotocol.NewFailure(
				browserprotocol.ErrorCloseRetryExhausted,
				"browser attachment close retry budget is exhausted",
				false,
			)
		}
		server.seen[requestID] = struct{}{}
		server.closeCount++
		if server.closeCount == maxCloseAttemptsPerAttachment {
			server.terminal = true
		}
		return nil
	}
	if server.terminal {
		return browserprotocol.NewFailure(
			browserprotocol.ErrorCloseRetryExhausted,
			"browser attachment is terminal after close retry exhaustion",
			false,
		)
	}
	cost := 1
	if action.Kind == browserprotocol.ActionBatch {
		cost = len(action.Actions)
	}
	if server.actionCount+cost > server.options.MaxRequests {
		return browserprotocol.NewFailure(
			browserprotocol.ErrorActionLimitExceeded,
			"browser attachment reached its action limit",
			false,
		)
	}
	server.seen[requestID] = struct{}{}
	server.actionCount += cost
	if action.Kind == browserprotocol.ActionClick &&
		(server.blockedClickNavigationCount >= maxBlockedClicksPerNavigation ||
			server.blockedClickRunCount >= maxBlockedClicksPerRun) {
		return server.clickRetryFailureLocked()
	}
	return nil
}

func (server *Server) recordEngineObservation(
	identity browserprotocol.Identity,
	observation browserprotocol.Observation,
) *browserprotocol.Failure {
	server.seenMu.Lock()
	defer server.seenMu.Unlock()
	server.prepareClickScopeLocked(identity)
	if failure := server.acceptEngineStateLocked(
		observation.EngineInstanceID,
		observation.NavigationGeneration,
	); failure != nil {
		return failure
	}
	server.clickPageStateID = observation.PageStateID
	return nil
}

func (server *Server) recordBlockedClick(
	identity browserprotocol.Identity,
	failure *browserprotocol.Failure,
) *browserprotocol.Failure {
	server.seenMu.Lock()
	defer server.seenMu.Unlock()
	server.prepareClickScopeLocked(identity)
	if stateFailure := server.acceptEngineStateLocked(
		failure.EngineInstanceID,
		failure.NavigationGeneration,
	); stateFailure != nil {
		return stateFailure
	}
	server.blockedClickNavigationCount++
	server.blockedClickRunCount++
	server.clickPageStateID = failure.PageStateID
	server.lastBlockedTargetCategory = failure.TargetCategory
	navigationRemaining := max(
		0,
		maxBlockedClicksPerNavigation-server.blockedClickNavigationCount,
	)
	runRemaining := max(0, maxBlockedClicksPerRun-server.blockedClickRunCount)
	failure.BlockedClickNavigationAttemptsRemaining = &navigationRemaining
	failure.BlockedClickRunAttemptsRemaining = &runRemaining
	return failure
}

func (server *Server) acceptEngineStateLocked(
	engineInstanceID uint64,
	navigationGeneration uint64,
) *browserprotocol.Failure {
	if navigationGeneration == 0 {
		return browserprotocol.NewFailure(
			browserprotocol.ErrorOutputInvalid,
			"browser navigation generation is invalid",
			false,
		)
	}
	if engineInstanceID == 0 {
		// EngineInstanceID is Go-only metadata. Test Engines that do not wrap a
		// process share one trusted synthetic instance.
		engineInstanceID = 1
	}
	if server.clickEngineInstanceID != engineInstanceID {
		server.clickEngineInstanceID = engineInstanceID
		server.clickNavigationGeneration = navigationGeneration
		server.blockedClickNavigationCount = 0
		return nil
	}
	if navigationGeneration < server.clickNavigationGeneration {
		return browserprotocol.NewFailure(
			browserprotocol.ErrorOutputInvalid,
			"browser navigation generation decreased",
			false,
		)
	}
	if navigationGeneration > server.clickNavigationGeneration {
		server.clickNavigationGeneration = navigationGeneration
		server.blockedClickNavigationCount = 0
	}
	return nil
}

func (server *Server) resetClickNavigationBaseline(
	identity browserprotocol.Identity,
) {
	server.seenMu.Lock()
	defer server.seenMu.Unlock()
	server.prepareClickScopeLocked(identity)
	server.clickEngineInstanceID = 0
	server.clickNavigationGeneration = 0
	server.blockedClickNavigationCount = 0
}

func (server *Server) prepareClickScopeLocked(identity browserprotocol.Identity) {
	if server.clickRunID != identity.RunID {
		server.clickRunID = identity.RunID
		server.blockedClickRunCount = 0
		server.clickPageStateID = ""
		server.lastBlockedTargetCategory = ""
		server.clickNavigationScope = ""
		server.clickEngineInstanceID = 0
		server.clickNavigationGeneration = 0
		server.blockedClickNavigationCount = 0
	}
	navigationScope := identity.BrowserSessionID + "|" +
		fmt.Sprintf("%d", identity.SessionEpoch)
	if server.clickNavigationScope != navigationScope {
		server.clickNavigationScope = navigationScope
		server.clickEngineInstanceID = 0
		server.clickNavigationGeneration = 0
		server.blockedClickNavigationCount = 0
	}
}

func (server *Server) clickRetryFailureLocked() *browserprotocol.Failure {
	failure := browserprotocol.NewFailure(
		browserprotocol.ErrorClickRetryExhausted,
		"browser blocked-click retry budget is exhausted",
		false,
	)
	failure.TargetCategory = server.lastBlockedTargetCategory
	failure.PageStateID = server.clickPageStateID
	failure.NavigationGeneration = server.clickNavigationGeneration
	navigationRemaining := max(
		0,
		maxBlockedClicksPerNavigation-server.blockedClickNavigationCount,
	)
	runRemaining := max(0, maxBlockedClicksPerRun-server.blockedClickRunCount)
	failure.BlockedClickNavigationAttemptsRemaining = &navigationRemaining
	failure.BlockedClickRunAttemptsRemaining = &runRemaining
	return failure
}

func attachmentRequestScope(identity browserprotocol.Identity) string {
	return identity.RunID + "|" + identity.AttachmentID + "|" +
		fmt.Sprintf("%d|%d", identity.SessionEpoch, identity.ControlEpoch)
}

func (server *Server) validateObservation(
	action browserprotocol.Action,
	observation browserprotocol.Observation,
) *browserprotocol.Failure {
	if action.Kind == browserprotocol.ActionClose {
		return observation.ValidateClosed()
	}
	return observation.ValidateEngine()
}

func (server *Server) decodeRequest(reader io.Reader) (browserprotocol.Request, *browserprotocol.Failure) {
	var request browserprotocol.Request
	limited := &io.LimitedReader{R: reader, N: server.options.MaxRequestBytes + 1}
	decoder := json.NewDecoder(limited)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		if limited.N == 0 {
			return request, browserprotocol.NewFailure(
				browserprotocol.ErrorRequestTooLarge,
				"browser request exceeds the input limit",
				false,
			)
		}
		return request, browserprotocol.NewFailure(
			browserprotocol.ErrorProtocolInvalid,
			"browser request is not valid",
			false,
		)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if limited.N == 0 {
			return request, browserprotocol.NewFailure(
				browserprotocol.ErrorRequestTooLarge,
				"browser request exceeds the input limit",
				false,
			)
		}
		return request, browserprotocol.NewFailure(
			browserprotocol.ErrorProtocolInvalid,
			"browser request contains trailing data",
			false,
		)
	}
	if limited.N == 0 {
		return request, browserprotocol.NewFailure(
			browserprotocol.ErrorRequestTooLarge,
			"browser request exceeds the input limit",
			false,
		)
	}
	return request, nil
}

func (server *Server) readRequest(reader io.Reader) ([]byte, *browserprotocol.Failure) {
	limited := &io.LimitedReader{
		R: reader,
		N: server.options.MaxRequestBytes + 1,
	}
	raw, err := io.ReadAll(limited)
	if err != nil {
		return nil, browserprotocol.NewFailure(
			browserprotocol.ErrorProtocolInvalid,
			"browser request is not valid",
			false,
		)
	}
	if int64(len(raw)) > server.options.MaxRequestBytes {
		return nil, browserprotocol.NewFailure(
			browserprotocol.ErrorRequestTooLarge,
			"browser request exceeds the input limit",
			false,
		)
	}
	return raw, nil
}

func (server *Server) writeResponse(writer io.Writer, response browserprotocol.Response) error {
	raw, err := json.Marshal(response)
	if err != nil || len(raw)+1 > server.options.MaxResponseBytes {
		raw, _ = json.Marshal(browserprotocol.ErrorResponse(
			response.RequestID,
			browserprotocol.NewFailure(
				browserprotocol.ErrorOutputTooLarge,
				"browser response exceeds the output limit",
				false,
			),
		))
	}
	raw = append(raw, '\n')
	return writeAllResponse(writer, raw)
}

func (server *Server) writeViewerResponse(
	writer io.Writer,
	response browserprotocol.ViewerResponse,
) error {
	raw, err := json.Marshal(response)
	if err != nil || len(raw)+1 > server.options.MaxResponseBytes {
		raw, _ = json.Marshal(browserprotocol.ViewerErrorResponse(
			response.RequestID,
			browserprotocol.NewFailure(
				browserprotocol.ErrorOutputTooLarge,
				"browser viewer response exceeds the output limit",
				false,
			),
		))
	}
	raw = append(raw, '\n')
	return writeAllResponse(writer, raw)
}

func (server *Server) writeHealthResponse(
	writer io.Writer,
	response browserprotocol.HealthResponse,
) error {
	raw, err := json.Marshal(response)
	if err != nil || len(raw)+1 > browserprotocol.MaxHealthResponseBytes {
		raw, _ = json.Marshal(browserprotocol.HealthErrorResponse(
			response.RequestID,
			browserprotocol.HealthReasonInvalidRequest,
		))
	}
	raw = append(raw, '\n')
	return writeAllResponse(writer, raw)
}

func writeAllResponse(writer io.Writer, value []byte) error {
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
