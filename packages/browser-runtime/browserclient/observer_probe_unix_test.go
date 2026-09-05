//go:build unix

package browserclient

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserprotocol"
)

func TestProbeObserverBridgeAcceptsContentFreeProbeResponse(t *testing.T) {
	directory, err := os.MkdirTemp("/tmp", "ol-observer-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	socketPath := filepath.Join(directory, "observer.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	credential := strings.Repeat("a", 64)
	served := make(chan error, 1)
	go func() {
		connection, acceptErr := listener.AcceptUnix()
		if acceptErr != nil {
			served <- acceptErr
			return
		}
		defer connection.Close()

		var request browserprotocol.OpsObserverRequest
		if decodeErr := json.NewDecoder(connection).Decode(&request); decodeErr != nil {
			served <- decodeErr
			return
		}
		if request.ContractID != browserprotocol.OpsObserverContractID ||
			request.ChannelCredential != credential ||
			request.Operation != browserprotocol.OpsObserverProbeOperation ||
			request.ObserverLeaseID != "" || request.RunID != "" ||
			!request.LeaseExpiresAt.IsZero() {
			served <- errors.New("probe request shape is invalid")
			return
		}
		served <- json.NewEncoder(connection).Encode(
			browserprotocol.OpsObserverProbeResponse(request.RequestID),
		)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := ProbeObserverBridge(ctx, socketPath, credential, time.Second); err != nil {
		t.Fatal(err)
	}
	if err := <-served; err != nil {
		t.Fatal(err)
	}
}
