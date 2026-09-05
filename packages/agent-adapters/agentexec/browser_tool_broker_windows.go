package agentexec

import (
	"context"
	"errors"

	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserplugin"
)

type browserToolBroker struct{}

func startBrowserToolBroker(
	context.Context,
	string,
	string,
	*browserRunLease,
	func() (browserplugin.Executor, error),
	*browserMutationJournal,
	func(),
) (*browserToolBroker, error) {
	return nil, errors.New("Browser execution profile requires a Linux container or Unix host")
}

func (*browserToolBroker) SocketPath() string { return "" }
func (*browserToolBroker) Close() error       { return nil }
