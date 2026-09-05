//go:build !windows

package browserruntime

import (
	"bufio"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserprotocol"
)

const maxOpsObserverRequestsPerLease = 4096

type OpsObserverServerOptions struct {
	SocketPath        string
	SocketMode        os.FileMode
	ChannelCredential string
	Observer          OpsObserverEngine
	Now               func() time.Time
	// Leases lets several listeners share one Runtime-wide observation lease.
	// When nil the server owns a private manager, which keeps the single-socket
	// deployment behaving exactly as before.
	Leases *OpsObserverLeaseManager
}

type OpsObserverServer struct {
	options     OpsObserverServerOptions
	mu          sync.Mutex
	listener    *net.UnixListener
	socketInfo  os.FileInfo
	closed      bool
	leases      *OpsObserverLeaseManager
	connections map[*net.UnixConn]struct{}
	wg          sync.WaitGroup
}

type opsObserverLease struct {
	id        string
	runID     string
	expiresAt time.Time
	sequence  uint64
	seen      map[string]struct{}
}

func NewOpsObserverServer(options OpsObserverServerOptions) (*OpsObserverServer, error) {
	options.SocketPath = filepath.Clean(options.SocketPath)
	if !filepath.IsAbs(options.SocketPath) {
		return nil, errors.New("Ops Observer socket path must be absolute")
	}
	if len(options.ChannelCredential) < 32 || len(options.ChannelCredential) > 512 {
		return nil, errors.New("Ops Observer channel credential length is invalid")
	}
	if options.Observer == nil {
		return nil, errors.New("Ops Observer engine is required")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.SocketMode == 0 {
		options.SocketMode = 0o600
	}
	if options.SocketMode.Perm()&0o077 != 0 {
		return nil, errors.New("Ops Observer socket must be owner-only")
	}
	leases := options.Leases
	if leases == nil {
		leases = NewOpsObserverLeaseManager(options.Now)
	}
	return &OpsObserverServer{
		options:     options,
		leases:      leases,
		connections: make(map[*net.UnixConn]struct{}),
	}, nil
}

func (server *OpsObserverServer) Serve(ctx context.Context) error {
	if err := server.open(); err != nil {
		return err
	}
	defer server.Close()
	server.mu.Lock()
	listener := server.listener
	server.mu.Unlock()
	stop := context.AfterFunc(ctx, func() { _ = server.Close() })
	defer stop()
	for {
		connection, err := listener.AcceptUnix()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				server.wg.Wait()
				return nil
			}
			return fmt.Errorf("accept Ops Observer Unix connection: %w", err)
		}
		server.mu.Lock()
		if server.closed {
			server.mu.Unlock()
			_ = connection.Close()
			continue
		}
		server.connections[connection] = struct{}{}
		server.wg.Add(1)
		server.mu.Unlock()
		go func() {
			defer server.wg.Done()
			defer server.removeConnection(connection)
			server.handleConnection(ctx, connection)
		}()
	}
}

func (server *OpsObserverServer) Close() error {
	server.mu.Lock()
	if server.closed {
		server.mu.Unlock()
		return nil
	}
	server.closed = true
	listener := server.listener
	socketInfo := server.socketInfo
	server.listener = nil
	server.socketInfo = nil
	connections := make([]*net.UnixConn, 0, len(server.connections))
	for connection := range server.connections {
		connections = append(connections, connection)
	}
	server.mu.Unlock()
	var result error
	if listener != nil {
		if err := listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			result = errors.Join(result, err)
		}
	}
	for _, connection := range connections {
		result = errors.Join(result, connection.Close())
	}
	result = errors.Join(result, removeOwnedSocket(server.options.SocketPath, socketInfo))
	return result
}

func (server *OpsObserverServer) open() error {
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.closed {
		return errors.New("Ops Observer server is closed")
	}
	if server.listener != nil {
		return nil
	}
	if err := validateSocketParent(filepath.Dir(server.options.SocketPath)); err != nil {
		return err
	}
	if err := removeStaleSocket(server.options.SocketPath); err != nil {
		return err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{
		Name: server.options.SocketPath,
		Net:  "unix",
	})
	if err != nil {
		return fmt.Errorf("listen on Ops Observer Unix socket: %w", err)
	}
	if err := os.Chmod(server.options.SocketPath, server.options.SocketMode.Perm()); err != nil {
		_ = listener.Close()
		_ = os.Remove(server.options.SocketPath)
		return fmt.Errorf("protect Ops Observer Unix socket: %w", err)
	}
	info, err := os.Lstat(server.options.SocketPath)
	if err != nil || info.Mode()&os.ModeSocket == 0 {
		_ = listener.Close()
		_ = os.Remove(server.options.SocketPath)
		return errors.New("cannot verify Ops Observer Unix socket identity")
	}
	server.listener = listener
	server.socketInfo = info
	return nil
}

func (server *OpsObserverServer) removeConnection(connection *net.UnixConn) {
	_ = connection.Close()
	server.mu.Lock()
	delete(server.connections, connection)
	server.mu.Unlock()
}

func (server *OpsObserverServer) handleConnection(parent context.Context, connection *net.UnixConn) {
	reader := bufio.NewReaderSize(connection, browserprotocol.MaxOpsObserverRequestBytes)
	var admittedLeaseID string
	defer func() {
		if admittedLeaseID != "" {
			server.releaseLease(admittedLeaseID)
		}
	}()
	for {
		_ = connection.SetReadDeadline(server.options.Now().UTC().Add(browserprotocol.MaxOpsObserverDeadline))
		raw, err := readBoundedLine(reader, browserprotocol.MaxOpsObserverRequestBytes)
		if err != nil {
			return
		}
		now := server.options.Now().UTC()
		request, decodeErr := browserprotocol.DecodeOpsObserverRequest(raw)
		if decodeErr != nil {
			server.writeResponse(connection, browserprotocol.OpsObserverErrorResponse(
				"",
				browserprotocol.NewOpsObserverError(
					browserprotocol.OpsObserverProtocolError,
					"Ops Observer request is invalid",
				),
			))
			return
		}
		if subtle.ConstantTimeCompare(
			[]byte(request.ChannelCredential),
			[]byte(server.options.ChannelCredential),
		) != 1 {
			server.writeResponse(connection, browserprotocol.OpsObserverErrorResponse(
				request.RequestID,
				browserprotocol.NewOpsObserverError(
					browserprotocol.OpsObserverUnauthorized,
					"Ops Observer request is unauthorized",
				),
			))
			return
		}
		if observerErr := request.Validate(now); observerErr != nil {
			server.writeResponse(connection, browserprotocol.OpsObserverErrorResponse(request.RequestID, observerErr))
			return
		}
		if request.Operation == browserprotocol.OpsObserverProbeOperation {
			// Answered before lease admission on purpose: the probe proves the
			// listener is serving its protocol without competing for the single
			// Runtime lease, so a health check cannot evict a real observer.
			server.writeResponse(connection, browserprotocol.OpsObserverProbeResponse(request.RequestID))
			continue
		}
		if admittedLeaseID == "" {
			if request.LeaseExpiresAt.Before(now.Add(
				browserprotocol.MinOpsObserverTTL - browserprotocol.MaxOpsObserverDeadline,
			)) {
				server.writeResponse(connection, browserprotocol.OpsObserverErrorResponse(
					request.RequestID,
					browserprotocol.NewOpsObserverError(
						browserprotocol.OpsObserverProtocolError,
						"Ops Observer initial TTL is too short",
					),
				))
				return
			}
			if observerErr := server.admitLease(request); observerErr != nil {
				server.writeResponse(connection, browserprotocol.OpsObserverErrorResponse(request.RequestID, observerErr))
				return
			}
			admittedLeaseID = request.ObserverLeaseID
		} else if observerErr := server.validateLeaseRequest(request); observerErr != nil {
			server.writeResponse(connection, browserprotocol.OpsObserverErrorResponse(request.RequestID, observerErr))
			return
		}
		if observerErr := server.admitRequest(request.ObserverLeaseID, request.RequestID); observerErr != nil {
			server.writeResponse(connection, browserprotocol.OpsObserverErrorResponse(request.RequestID, observerErr))
			return
		}
		requestContext, cancel := context.WithDeadline(parent, request.Deadline)
		observation, busy, observerErr := server.options.Observer.ObserveOps(
			requestContext,
			request.RunID,
			request.Operation,
		)
		cancel()
		if observerErr != nil {
			server.writeResponse(connection, browserprotocol.OpsObserverErrorResponse(request.RequestID, observerErr))
			// Run-not-active is also the normal state before the provider's first
			// Browser action and between two actions. Keep the connection-bound
			// lease alive so an authenticated observer can wait on the same stream;
			// the caller closes it when its stronger Attempt identity says the Run
			// actually ended or rotated.
			continue
		}
		if busy {
			server.writeResponse(connection, browserprotocol.OpsObserverBusyResponse(request.RequestID))
			continue
		}
		sequence, sequenceErr := server.nextSequence(request.ObserverLeaseID)
		if sequenceErr != nil {
			server.writeResponse(connection, browserprotocol.OpsObserverErrorResponse(request.RequestID, sequenceErr))
			return
		}
		observation.FrameSequence = sequence
		observation.CapturedAt = server.options.Now().UTC()
		if validationErr := observation.Validate(request.Operation); validationErr != nil {
			server.writeResponse(connection, browserprotocol.OpsObserverErrorResponse(request.RequestID, validationErr))
			continue
		}
		server.writeResponse(connection, browserprotocol.OpsObserverSuccessResponse(request.RequestID, observation))
	}
}

func (server *OpsObserverServer) admitLease(request browserprotocol.OpsObserverRequest) *browserprotocol.OpsObserverError {
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.closed {
		return browserprotocol.NewOpsObserverError(browserprotocol.OpsObserverDisabled, "Ops Observer is unavailable")
	}
	leases := server.leases
	server.mu.Unlock()
	failure := leases.Admit(request)
	server.mu.Lock()
	return failure
}

func (server *OpsObserverServer) validateLeaseRequest(request browserprotocol.OpsObserverRequest) *browserprotocol.OpsObserverError {
	return server.leaseManager().Validate(request)
}

func (server *OpsObserverServer) admitRequest(leaseID, requestID string) *browserprotocol.OpsObserverError {
	return server.leaseManager().AdmitRequest(leaseID, requestID)
}

func (server *OpsObserverServer) nextSequence(leaseID string) (uint64, *browserprotocol.OpsObserverError) {
	return server.leaseManager().NextSequence(leaseID)
}

func (server *OpsObserverServer) releaseLease(leaseID string) {
	server.leaseManager().Release(leaseID)
}

// leaseManager reads the shared manager under the server lock so a listener that
// is concurrently closing cannot race the pointer read.
func (server *OpsObserverServer) leaseManager() *OpsObserverLeaseManager {
	server.mu.Lock()
	defer server.mu.Unlock()
	return server.leases
}

func (server *OpsObserverServer) writeResponse(connection *net.UnixConn, response browserprotocol.OpsObserverResponse) {
	_ = connection.SetWriteDeadline(server.options.Now().UTC().Add(browserprotocol.MaxOpsObserverDeadline))
	raw, err := json.Marshal(response)
	if err != nil {
		return
	}
	raw = append(raw, '\n')
	_, _ = connection.Write(raw)
}

// Leases exposes the shared observation lease so a second listener can be built
// against the same Runtime-wide lease rather than a private one.
func (server *OpsObserverServer) Leases() *OpsObserverLeaseManager {
	if server == nil {
		return nil
	}
	return server.leaseManager()
}
