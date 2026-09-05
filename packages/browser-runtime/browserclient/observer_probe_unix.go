//go:build unix

package browserclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"path/filepath"
	"strings"
	"time"

	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserprotocol"
)

// ProbeObserverBridge proves the observation listener is serving its protocol.
//
// It performs a bounded, authenticated round trip and validates the response
// shape. It deliberately does not claim the observation lease and never asks for
// a frame: the lease is Runtime-wide and singular, so a probe that took it would
// evict the real observer on every health interval. Checking only that the socket
// and credential files exist would repeat the empty-probe failure this repository
// already fixed once, where the files remained while the Runtime was deadlocked.
func ProbeObserverBridge(
	ctx context.Context,
	socketPath string,
	channelCredential string,
	timeout time.Duration,
) error {
	socketPath = filepath.Clean(strings.TrimSpace(socketPath))
	if !filepath.IsAbs(socketPath) {
		return errors.New("Browser observation socket path must be absolute")
	}
	if len(channelCredential) < 32 ||
		len(channelCredential) > maxChannelCredentialSize ||
		strings.ContainsAny(channelCredential, "\r\n\t ") {
		return errors.New("Browser observation credential is invalid")
	}
	if timeout <= 0 || timeout > 10*time.Second {
		return errors.New("Browser observation probe timeout is invalid")
	}
	requestID, err := newRequestID()
	if err != nil {
		return errors.New("generate Browser observation probe identifier")
	}
	deadline := time.Now().Add(timeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	dialer := net.Dialer{}
	connection, err := dialer.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return errors.New("connect to Browser observation endpoint")
	}
	defer connection.Close()
	if err := connection.SetDeadline(deadline); err != nil {
		return errors.New("set Browser observation probe deadline")
	}
	stopCancellation := context.AfterFunc(ctx, func() {
		_ = connection.SetDeadline(time.Now())
	})
	defer stopCancellation()

	request := browserprotocol.OpsObserverRequest{
		ContractID:        browserprotocol.OpsObserverContractID,
		ChannelCredential: channelCredential,
		RequestID:         requestID,
		Operation:         browserprotocol.OpsObserverProbeOperation,
		Deadline:          time.Now().UTC().Add(timeout),
	}
	if err := json.NewEncoder(connection).Encode(request); err != nil {
		return errors.New("send Browser observation probe")
	}
	if unixConnection, ok := connection.(*net.UnixConn); ok {
		if err := unixConnection.CloseWrite(); err != nil {
			return errors.New("finish Browser observation probe")
		}
	}
	limited := io.LimitReader(
		connection,
		int64(browserprotocol.MaxOpsObserverProbeResponseBytes)+1,
	)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return errors.New("read Browser observation probe response")
	}
	if len(raw) == 0 || len(raw) > browserprotocol.MaxOpsObserverProbeResponseBytes {
		return errors.New("Browser observation probe response size is invalid")
	}
	response, err := browserprotocol.DecodeOpsObserverProbeResponse(raw)
	if err != nil {
		return errors.New("Browser observation probe response is invalid")
	}
	if response.ContractID != browserprotocol.OpsObserverContractID ||
		response.RequestID != requestID || response.Status != "ok" {
		return errors.New("Browser observation probe was refused")
	}
	// A probe must never carry an observation; page content in this response
	// would mean the listener answered with a frame it was not asked for.
	if response.Observation != nil {
		return errors.New("Browser observation probe returned page content")
	}
	return nil
}
