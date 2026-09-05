//go:build !windows

package browserclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"time"

	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserprotocol"
)

func exchange(
	ctx context.Context,
	socketPath string,
	request browserprotocol.Request,
	deadline time.Time,
) (browserprotocol.Response, *browserprotocol.Failure) {
	dialer := net.Dialer{}
	connection, err := dialer.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return browserprotocol.Response{}, contextFailure(ctx, "connect to Browser Runtime")
	}
	defer connection.Close()
	if err := connection.SetDeadline(deadline); err != nil {
		return browserprotocol.Response{}, browserprotocol.NewFailure(
			browserprotocol.ErrorRuntimeUnavailable,
			"set Browser Runtime deadline",
			true,
		)
	}
	stopCancellation := context.AfterFunc(ctx, func() {
		_ = connection.SetDeadline(time.Now())
	})
	defer stopCancellation()
	if err := json.NewEncoder(connection).Encode(request); err != nil {
		return browserprotocol.Response{}, contextFailure(ctx, "send Browser Runtime request")
	}
	if unixConnection, ok := connection.(*net.UnixConn); ok {
		if err := unixConnection.CloseWrite(); err != nil {
			return browserprotocol.Response{}, contextFailure(ctx, "finish Browser Runtime request")
		}
	}
	counted := &countingReader{reader: connection}
	limited := &io.LimitedReader{
		R: counted,
		N: int64(browserprotocol.MaxResponseBytes) + 1,
	}
	decoder := json.NewDecoder(limited)
	decoder.DisallowUnknownFields()
	var response browserprotocol.Response
	if err := decoder.Decode(&response); err != nil {
		if limited.N == 0 {
			return browserprotocol.Response{}, browserprotocol.NewFailure(
				browserprotocol.ErrorOutputTooLarge,
				"Browser Runtime response exceeds the output limit",
				false,
			)
		}
		return browserprotocol.Response{}, classifyRuntimeDecodeFailure(
			ctx,
			err,
			counted.count,
			"Browser Runtime",
		)
	}
	if limited.N == 0 {
		return browserprotocol.Response{}, browserprotocol.NewFailure(
			browserprotocol.ErrorOutputTooLarge,
			"Browser Runtime response exceeds the output limit",
			false,
		)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if limited.N == 0 {
			return browserprotocol.Response{}, browserprotocol.NewFailure(
				browserprotocol.ErrorOutputTooLarge,
				"Browser Runtime response exceeds the output limit",
				false,
			)
		}
		if err != nil {
			return browserprotocol.Response{}, classifyRuntimeDecodeFailure(
				ctx,
				err,
				counted.count,
				"Browser Runtime",
			)
		}
		return browserprotocol.Response{}, browserprotocol.NewFailure(
			browserprotocol.ErrorOutputInvalid,
			"Browser Runtime response contains trailing data",
			false,
		)
	}
	return response, nil
}

func exchangeViewer(
	ctx context.Context,
	socketPath string,
	request browserprotocol.ViewerRequest,
	deadline time.Time,
) (browserprotocol.ViewerResponse, *browserprotocol.Failure) {
	dialer := net.Dialer{}
	connection, err := dialer.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return browserprotocol.ViewerResponse{}, contextFailure(
			ctx,
			"connect to Browser Runtime Viewer",
		)
	}
	defer connection.Close()
	if err := connection.SetDeadline(deadline); err != nil {
		return browserprotocol.ViewerResponse{}, browserprotocol.NewFailure(
			browserprotocol.ErrorRuntimeUnavailable,
			"set Browser Runtime Viewer deadline",
			true,
		)
	}
	stopCancellation := context.AfterFunc(ctx, func() {
		_ = connection.SetDeadline(time.Now())
	})
	defer stopCancellation()
	if err := json.NewEncoder(connection).Encode(request); err != nil {
		return browserprotocol.ViewerResponse{}, contextFailure(
			ctx,
			"send Browser Runtime Viewer request",
		)
	}
	if unixConnection, ok := connection.(*net.UnixConn); ok {
		if err := unixConnection.CloseWrite(); err != nil {
			return browserprotocol.ViewerResponse{}, contextFailure(
				ctx,
				"finish Browser Runtime Viewer request",
			)
		}
	}
	counted := &countingReader{reader: connection}
	limited := &io.LimitedReader{
		R: counted,
		N: int64(browserprotocol.MaxResponseBytes) + 1,
	}
	decoder := json.NewDecoder(limited)
	decoder.DisallowUnknownFields()
	var response browserprotocol.ViewerResponse
	if err := decoder.Decode(&response); err != nil {
		if limited.N == 0 {
			return browserprotocol.ViewerResponse{}, browserprotocol.NewFailure(
				browserprotocol.ErrorOutputTooLarge,
				"Browser Runtime Viewer response exceeds the output limit",
				false,
			)
		}
		return browserprotocol.ViewerResponse{}, classifyRuntimeDecodeFailure(
			ctx,
			err,
			counted.count,
			"Browser Runtime Viewer",
		)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if limited.N == 0 {
			return browserprotocol.ViewerResponse{}, browserprotocol.NewFailure(
				browserprotocol.ErrorOutputTooLarge,
				"Browser Runtime Viewer response exceeds the output limit",
				false,
			)
		}
		if err != nil {
			return browserprotocol.ViewerResponse{}, classifyRuntimeDecodeFailure(
				ctx,
				err,
				counted.count,
				"Browser Runtime Viewer",
			)
		}
		return browserprotocol.ViewerResponse{}, browserprotocol.NewFailure(
			browserprotocol.ErrorOutputInvalid,
			"Browser Runtime Viewer response contains trailing data",
			false,
		)
	}
	return response, nil
}

type countingReader struct {
	reader io.Reader
	count  int64
}

func (reader *countingReader) Read(value []byte) (int, error) {
	count, err := reader.reader.Read(value)
	reader.count += int64(count)
	return count, err
}

func classifyRuntimeDecodeFailure(
	ctx context.Context,
	err error,
	bytesRead int64,
	label string,
) *browserprotocol.Failure {
	if ctx.Err() != nil {
		return contextFailure(ctx, "read "+label+" response")
	}
	var timeoutError net.Error
	if errors.As(err, &timeoutError) && timeoutError.Timeout() ||
		errors.Is(err, os.ErrDeadlineExceeded) {
		return browserprotocol.NewFailure(
			browserprotocol.ErrorRuntimeUnavailable,
			label+" response timed out",
			true,
		)
	}
	if bytesRead == 0 {
		return browserprotocol.NewFailure(
			browserprotocol.ErrorRuntimeUnavailable,
			label+" closed without a response",
			true,
		)
	}
	return browserprotocol.NewFailure(
		browserprotocol.ErrorOutputInvalid,
		label+" response is invalid",
		false,
	)
}

func contextFailure(
	ctx context.Context,
	operation string,
) *browserprotocol.Failure {
	switch ctx.Err() {
	case context.Canceled:
		return browserprotocol.NewFailure(
			browserprotocol.ErrorCanceled,
			"browser request was canceled",
			false,
		)
	case context.DeadlineExceeded:
		return browserprotocol.NewFailure(
			browserprotocol.ErrorDeadlineExceeded,
			"browser request exceeded its deadline",
			true,
		)
	default:
		return browserprotocol.NewFailure(
			browserprotocol.ErrorRuntimeUnavailable,
			operation+" failed",
			true,
		)
	}
}
