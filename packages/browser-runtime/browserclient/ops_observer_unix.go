//go:build !windows

package browserclient

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserprotocol"
)

type OpsObserverConfig struct {
	SocketPath     string
	CredentialFile string
	RunID          string
	TTL            time.Duration
	Now            func() time.Time
}

type OpsObserverStream struct {
	mu                sync.Mutex
	connection        *net.UnixConn
	reader            *bufio.Reader
	channelCredential string
	observerLeaseID   string
	runID             string
	expiresAt         time.Time
	now               func() time.Time
	closed            bool
}

func NewOpsObserverStream(ctx context.Context, config OpsObserverConfig) (*OpsObserverStream, error) {
	socketPath := filepath.Clean(strings.TrimSpace(config.SocketPath))
	if !filepath.IsAbs(socketPath) {
		return nil, errors.New("Ops Observer socket path must be absolute")
	}
	credentialRaw, err := readOwnerOnlyFile(
		config.CredentialFile,
		maxCredentialFileBytes,
		"Ops Observer channel credential",
	)
	if err != nil {
		return nil, err
	}
	credential := strings.TrimSpace(string(credentialRaw))
	if len(credential) < 32 || len(credential) > maxChannelCredentialSize ||
		strings.ContainsAny(credential, "\r\n\t ") {
		return nil, errors.New("Ops Observer channel credential value is invalid")
	}
	if config.TTL < browserprotocol.MinOpsObserverTTL ||
		config.TTL > browserprotocol.MaxOpsObserverTTL {
		return nil, errors.New("Ops Observer TTL is invalid")
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	validationNow := now().UTC()
	if failure := (browserprotocol.OpsObserverRequest{
		ContractID:        browserprotocol.OpsObserverContractID,
		ChannelCredential: credential,
		RequestID:         "11111111-1111-4111-8111-111111111111",
		ObserverLeaseID:   "22222222-2222-4222-8222-222222222222",
		RunID:             config.RunID,
		Operation:         browserprotocol.OpsObserverStatusOperation,
		Deadline:          validationNow.Add(time.Second),
		LeaseExpiresAt:    validationNow.Add(config.TTL),
	}).Validate(validationNow); failure != nil {
		return nil, errors.New("Ops Observer Run ID is invalid")
	}
	leaseID, err := newRequestID()
	if err != nil {
		return nil, errors.New("generate Ops Observer lease identifier")
	}
	dialer := net.Dialer{}
	rawConnection, err := dialer.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return nil, errors.New("connect to Ops Observer Runtime")
	}
	connection, ok := rawConnection.(*net.UnixConn)
	if !ok {
		_ = rawConnection.Close()
		return nil, errors.New("Ops Observer connection is not a Unix socket")
	}
	return &OpsObserverStream{
		connection:        connection,
		reader:            bufio.NewReaderSize(connection, 64<<10),
		channelCredential: credential,
		observerLeaseID:   leaseID,
		runID:             config.RunID,
		expiresAt:         now().UTC().Add(config.TTL),
		now:               now,
	}, nil
}

func (stream *OpsObserverStream) Observe(
	ctx context.Context,
	operation browserprotocol.OpsObserverOperation,
) (browserprotocol.OpsObserverResponse, *browserprotocol.OpsObserverError) {
	if stream == nil {
		return browserprotocol.OpsObserverResponse{}, browserprotocol.NewOpsObserverError(
			browserprotocol.OpsObserverInternalError,
			"Ops Observer stream is not configured",
		)
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if stream.closed || stream.connection == nil {
		return browserprotocol.OpsObserverResponse{}, browserprotocol.NewOpsObserverError(
			browserprotocol.OpsObserverInternalError,
			"Ops Observer stream is closed",
		)
	}
	now := stream.now().UTC()
	if !now.Before(stream.expiresAt) {
		return browserprotocol.OpsObserverResponse{}, browserprotocol.NewOpsObserverError(
			browserprotocol.OpsObserverProtocolError,
			"Ops Observer lease expired",
		)
	}
	requestID, err := newRequestID()
	if err != nil {
		return browserprotocol.OpsObserverResponse{}, browserprotocol.NewOpsObserverError(
			browserprotocol.OpsObserverInternalError,
			"generate Ops Observer request identifier",
		)
	}
	deadline := now.Add(browserprotocol.MaxOpsObserverDeadline)
	if stream.expiresAt.Before(deadline) {
		deadline = stream.expiresAt
	}
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline.UTC()
	}
	request := browserprotocol.OpsObserverRequest{
		ContractID:        browserprotocol.OpsObserverContractID,
		ChannelCredential: stream.channelCredential,
		RequestID:         requestID,
		ObserverLeaseID:   stream.observerLeaseID,
		RunID:             stream.runID,
		Operation:         operation,
		Deadline:          deadline,
		LeaseExpiresAt:    stream.expiresAt,
	}
	if observerErr := request.Validate(now); observerErr != nil {
		return browserprotocol.OpsObserverResponse{}, observerErr
	}
	if err := stream.connection.SetDeadline(deadline); err != nil {
		return browserprotocol.OpsObserverResponse{}, browserprotocol.NewOpsObserverError(
			browserprotocol.OpsObserverInternalError,
			"set Ops Observer deadline",
		)
	}
	stopCancellation := context.AfterFunc(ctx, func() {
		_ = stream.connection.SetDeadline(time.Now())
	})
	defer stopCancellation()
	if err := jsonEncodeLine(stream.connection, request); err != nil {
		return browserprotocol.OpsObserverResponse{}, observerTransportError(ctx, "send Ops Observer request")
	}
	raw, err := readOpsObserverLine(stream.reader, browserprotocol.MaxResponseBytes)
	if err != nil {
		return browserprotocol.OpsObserverResponse{}, observerTransportError(ctx, "read Ops Observer response")
	}
	response, err := browserprotocol.DecodeOpsObserverResponse(raw)
	if err != nil || response.RequestID != requestID {
		return browserprotocol.OpsObserverResponse{}, browserprotocol.NewOpsObserverError(
			browserprotocol.OpsObserverInternalError,
			"Ops Observer response is invalid",
		)
	}
	if response.Status == "ok" {
		if validationErr := response.Observation.Validate(operation); validationErr != nil {
			return browserprotocol.OpsObserverResponse{}, validationErr
		}
	}
	if response.Status == "error" {
		return response, response.Error
	}
	return response, nil
}

func (stream *OpsObserverStream) Close() error {
	if stream == nil {
		return nil
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if stream.closed {
		return nil
	}
	stream.closed = true
	connection := stream.connection
	stream.connection = nil
	if connection == nil {
		return nil
	}
	return connection.Close()
}

func jsonEncodeLine(writer io.Writer, value any) error {
	return json.NewEncoder(writer).Encode(value)
}

func readOpsObserverLine(reader *bufio.Reader, limit int) ([]byte, error) {
	var output []byte
	for {
		fragment, more, err := reader.ReadLine()
		if err != nil {
			return nil, err
		}
		if len(output)+len(fragment) > limit {
			return nil, errors.New("Ops Observer response exceeds the output limit")
		}
		output = append(output, fragment...)
		if !more {
			return output, nil
		}
	}
}

func observerTransportError(ctx context.Context, message string) *browserprotocol.OpsObserverError {
	if ctx.Err() != nil {
		return browserprotocol.NewOpsObserverError(browserprotocol.OpsObserverInternalError, message+" canceled")
	}
	return browserprotocol.NewOpsObserverError(browserprotocol.OpsObserverInternalError, message+" failed")
}
