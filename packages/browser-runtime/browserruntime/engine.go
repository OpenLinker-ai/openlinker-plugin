//go:build !windows

package browserruntime

import (
	"context"

	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserprotocol"
)

type Engine interface {
	Execute(
		context.Context,
		browserprotocol.Identity,
		browserprotocol.Action,
	) (browserprotocol.Observation, *browserprotocol.Failure)
}

type ViewerEngine interface {
	ExecuteViewer(
		context.Context,
		browserprotocol.Identity,
		browserprotocol.ViewerOperation,
		*browserprotocol.ViewerInput,
	) (*browserprotocol.ViewerFrame, *browserprotocol.Failure)
}

type OpsObserverEngine interface {
	ObserveOps(
		context.Context,
		string,
		browserprotocol.OpsObserverOperation,
	) (browserprotocol.OpsObserverObservation, bool, *browserprotocol.OpsObserverError)
}

type activeOpsObserverEngine interface {
	ObserveActiveOps(
		context.Context,
		browserprotocol.Identity,
		browserprotocol.OpsObserverOperation,
	) (opsPageObservation, bool, *browserprotocol.OpsObserverError)
}

// opsActionInFlightReporter is implemented by Engines that can report whether a
// provider-issued Browser action is executing. It stays optional so an Engine
// without the capability still serves observations, only without the evidence.
type opsActionInFlightReporter interface {
	ActionInFlightSample() (started uint64, inFlight bool)
}

type opsPageObservation struct {
	PageURL   string
	PageTitle string
	Frame     *browserprotocol.ViewerFrame
}
