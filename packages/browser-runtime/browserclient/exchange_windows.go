//go:build windows

package browserclient

import (
	"context"
	"time"

	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserprotocol"
)

func exchange(
	context.Context,
	string,
	browserprotocol.Request,
	time.Time,
) (browserprotocol.Response, *browserprotocol.Failure) {
	return browserprotocol.Response{}, browserprotocol.NewFailure(
		browserprotocol.ErrorRuntimeUnavailable,
		"Browser Runtime is unavailable on Windows",
		false,
	)
}

func exchangeViewer(
	ctx context.Context,
	socketPath string,
	request browserprotocol.ViewerRequest,
	deadline time.Time,
) (browserprotocol.ViewerResponse, *browserprotocol.Failure) {
	return browserprotocol.ViewerResponse{}, browserprotocol.NewFailure(
		browserprotocol.ErrorViewerUnavailable,
		"Browser Runtime Viewer is unavailable on Windows",
		false,
	)
}
