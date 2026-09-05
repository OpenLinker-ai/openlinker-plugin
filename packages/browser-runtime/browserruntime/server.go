//go:build !windows

package browserruntime

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserprotocol"
)

const (
	defaultIOTimeout              = 5 * time.Second
	maxRequestsPerLease           = 2048
	maxCloseAttemptsPerAttachment = 3
	maxBlockedClicksPerNavigation = 3
	maxBlockedClicksPerRun        = 12
	maxViewerRequestsPerEpoch     = 8192
)

type ServerOptions struct {
	SocketPath            string
	SocketMode            os.FileMode
	ChannelCredential     string
	Lease                 LeaseValidator
	Engine                Engine
	Now                   func() time.Time
	MaxRequestBytes       int64
	MaxResponseBytes      int
	MaxRequests           int
	IOTimeout             time.Duration
	HumanControlAvailable bool
}

type Server struct {
	options     ServerOptions
	mu          sync.Mutex
	listener    *net.UnixListener
	socketInfo  os.FileInfo
	closed      bool
	wg          sync.WaitGroup
	sem         chan struct{}
	seenMu      sync.Mutex
	seenScope   string
	seen        map[string]struct{}
	actionCount int
	viewerCount int
	closeCount  int
	terminal    bool

	clickRunID                  string
	blockedClickRunCount        int
	clickNavigationScope        string
	clickEngineInstanceID       uint64
	clickNavigationGeneration   uint64
	blockedClickNavigationCount int
	clickPageStateID            string
	lastBlockedTargetCategory   browserprotocol.TargetCategory
}

func NewServer(options ServerOptions) (*Server, error) {
	options.SocketPath = filepath.Clean(options.SocketPath)
	if !filepath.IsAbs(options.SocketPath) {
		return nil, errors.New("browser socket path must be absolute")
	}
	if len(options.ChannelCredential) < 32 || len(options.ChannelCredential) > 512 {
		return nil, errors.New("browser channel credential length is invalid")
	}
	if options.Lease == nil {
		return nil, errors.New("browser lease validator is required")
	}
	if options.Engine == nil {
		return nil, errors.New("browser engine is required")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.MaxRequestBytes <= 0 {
		options.MaxRequestBytes = browserprotocol.MaxRequestBytes
	}
	if options.MaxRequestBytes < 1024 {
		return nil, errors.New("browser request limit is too small for the contract")
	}
	if options.MaxRequestBytes > browserprotocol.MaxRequestBytes {
		return nil, errors.New("browser request limit exceeds the contract maximum")
	}
	if options.MaxResponseBytes <= 0 {
		options.MaxResponseBytes = browserprotocol.MaxResponseBytes
	}
	if options.MaxResponseBytes < 1024 {
		return nil, errors.New("browser response limit is too small for a stable error response")
	}
	if options.MaxResponseBytes > browserprotocol.MaxResponseBytes {
		return nil, errors.New("browser response limit exceeds the contract maximum")
	}
	if options.MaxRequests <= 0 {
		options.MaxRequests = maxRequestsPerLease
	}
	if options.MaxRequests > maxRequestsPerLease {
		return nil, errors.New("browser request count limit exceeds the contract maximum")
	}
	if options.IOTimeout <= 0 {
		options.IOTimeout = defaultIOTimeout
	}
	if options.SocketMode == 0 {
		options.SocketMode = 0o600
	}
	if options.SocketMode.Perm()&0o007 != 0 {
		return nil, errors.New("browser socket must not be accessible by other users")
	}
	return &Server{
		options: options,
		sem:     make(chan struct{}, 1),
		seen: make(
			map[string]struct{},
			options.MaxRequests+maxCloseAttemptsPerAttachment,
		),
	}, nil
}

func (server *Server) Serve(ctx context.Context) error {
	if err := server.Open(); err != nil {
		return err
	}
	defer server.Close()
	server.mu.Lock()
	listener := server.listener
	server.mu.Unlock()

	stop := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = server.Close()
		case <-stop:
		}
	}()
	defer close(stop)

	for {
		select {
		case server.sem <- struct{}{}:
		case <-ctx.Done():
			server.wg.Wait()
			return nil
		}
		connection, err := listener.AcceptUnix()
		if err != nil {
			<-server.sem
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				server.wg.Wait()
				return nil
			}
			server.wg.Wait()
			return fmt.Errorf("accept browser Unix connection: %w", err)
		}
		server.wg.Add(1)
		go func() {
			defer server.wg.Done()
			defer func() { <-server.sem }()
			defer connection.Close()
			server.handleConnection(ctx, connection)
		}()
	}
}

func (server *Server) Close() error {
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
	server.mu.Unlock()

	var closeErr error
	if listener != nil {
		closeErr = listener.Close()
	}
	removeErr := removeOwnedSocket(server.options.SocketPath, socketInfo)
	if closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
		return closeErr
	}
	return removeErr
}
