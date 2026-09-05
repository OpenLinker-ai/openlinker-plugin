//go:build windows

package browserclient

import (
	"context"
	"errors"
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

type OpsObserverStream struct{}

func NewOpsObserverStream(context.Context, OpsObserverConfig) (*OpsObserverStream, error) {
	return nil, errors.New("Ops Observer requires a Unix host")
}

func (stream *OpsObserverStream) Observe(
	context.Context,
	browserprotocol.OpsObserverOperation,
) (browserprotocol.OpsObserverResponse, *browserprotocol.OpsObserverError) {
	return browserprotocol.OpsObserverResponse{}, browserprotocol.NewOpsObserverError(
		browserprotocol.OpsObserverDisabled,
		"Ops Observer requires a Unix host",
	)
}

func (stream *OpsObserverStream) Close() error {
	return nil
}
