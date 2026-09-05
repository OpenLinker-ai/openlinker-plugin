//go:build !windows

package browserclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strings"
	"time"

	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserprotocol"
)

func CheckHealth(
	ctx context.Context,
	socketPath string,
	channelCredential string,
	timeout time.Duration,
) error {
	socketPath = filepath.Clean(strings.TrimSpace(socketPath))
	if !filepath.IsAbs(socketPath) {
		return errors.New("Browser health socket path must be absolute")
	}
	if len(channelCredential) < 32 ||
		len(channelCredential) > maxChannelCredentialSize ||
		strings.ContainsAny(channelCredential, "\r\n\t ") {
		return errors.New("Browser health credential is invalid")
	}
	if timeout <= 0 || timeout > 10*time.Second {
		return errors.New("Browser health timeout is invalid")
	}
	requestID, err := newRequestID()
	if err != nil {
		return errors.New("generate Browser health request identifier")
	}
	deadline := time.Now().Add(timeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	dialer := net.Dialer{}
	connection, err := dialer.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return errors.New("connect to Browser Runtime health endpoint")
	}
	defer connection.Close()
	if err := connection.SetDeadline(deadline); err != nil {
		return errors.New("set Browser Runtime health deadline")
	}
	stopCancellation := context.AfterFunc(ctx, func() {
		_ = connection.SetDeadline(time.Now())
	})
	defer stopCancellation()
	request := browserprotocol.HealthRequest{
		ContractID:        browserprotocol.HealthContractID,
		ChannelCredential: channelCredential,
		RequestID:         requestID,
	}
	if err := json.NewEncoder(connection).Encode(request); err != nil {
		return errors.New("send Browser Runtime health request")
	}
	if unixConnection, ok := connection.(*net.UnixConn); ok {
		if err := unixConnection.CloseWrite(); err != nil {
			return errors.New("finish Browser Runtime health request")
		}
	}
	limited := io.LimitReader(
		connection,
		int64(browserprotocol.MaxHealthResponseBytes)+1,
	)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return errors.New("read Browser Runtime health response")
	}
	if len(raw) == 0 || len(raw) > browserprotocol.MaxHealthResponseBytes {
		return errors.New("Browser Runtime health response size is invalid")
	}
	response, err := browserprotocol.DecodeHealthResponse(raw)
	if err != nil {
		return errors.New("Browser Runtime health response is invalid")
	}
	if err := response.Validate(requestID); err != nil {
		return err
	}
	if response.Status != "ok" {
		return fmt.Errorf("Browser Runtime health check failed: %s", response.Reason)
	}
	return nil
}
