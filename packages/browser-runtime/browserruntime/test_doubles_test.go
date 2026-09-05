//go:build !windows

package browserruntime

import (
	"context"

	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserprotocol"
)

type NotReadyEngine struct{}

func (NotReadyEngine) Execute(
	context.Context,
	browserprotocol.Identity,
	browserprotocol.Action,
) (browserprotocol.Observation, *browserprotocol.Failure) {
	return browserprotocol.Observation{}, browserprotocol.NewFailure(
		browserprotocol.ErrorRuntimeUnavailable,
		"browser engine is not configured",
		true,
	)
}

type NoActiveLease struct{}

func (NoActiveLease) Validate(browserprotocol.Identity) *browserprotocol.Failure {
	return browserprotocol.NewFailure(
		browserprotocol.ErrorRuntimeUnavailable,
		"browser runtime has no active attachment",
		true,
	)
}
