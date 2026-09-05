//go:build !windows

package agentexec

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserplugin"
)

type browserToolBroker struct {
	socketPath        string
	listener          *net.UnixListener
	cancel            context.CancelFunc
	done              chan struct{}
	info              os.FileInfo
	closeOnce         sync.Once
	agentProgressOnce sync.Once
}

func startBrowserToolBroker(
	parent context.Context,
	host string,
	root string,
	lease *browserRunLease,
	clientFactory func() (browserplugin.Executor, error),
	journal *browserMutationJournal,
	onFirstAgentExecutor func(),
) (*browserToolBroker, error) {
	host = strings.ToLower(strings.TrimSpace(host))
	if host != "codex" && host != "claude" {
		return nil, errors.New("Browser tool broker host is invalid")
	}
	if lease == nil {
		return nil, errors.New("Browser tool broker lease is missing")
	}
	if clientFactory == nil {
		clientFactory = lease.browserClient
	}
	root = filepath.Clean(strings.TrimSpace(root))
	if !filepath.IsAbs(root) {
		return nil, errors.New("Browser tool broker root must be absolute")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	rootInfo, err := os.Lstat(root)
	if err != nil ||
		rootInfo.Mode()&os.ModeSymlink != 0 ||
		!rootInfo.IsDir() ||
		rootInfo.Mode().Perm()&0o007 != 0 ||
		rootInfo.Mode().Perm()&0o020 != 0 ||
		!sessionFileOwnedByCurrentUser(rootInfo) {
		return nil, errors.New("Browser tool broker root must be owner-controlled and not writable by group or other users")
	}
	socketPath := filepath.Join(root, "browser-"+lease.identity.RunID+".sock")
	if info, err := os.Lstat(socketPath); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || info.Mode()&os.ModeSocket == 0 {
			return nil, errors.New("Browser tool broker path is not a Unix socket")
		}
		if connection, dialErr := net.DialUnix(
			"unix",
			nil,
			&net.UnixAddr{Name: socketPath, Net: "unix"},
		); dialErr == nil {
			_ = connection.Close()
			return nil, errors.New("Browser tool broker is already active")
		}
		if err := os.Remove(socketPath); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	listener, err := net.ListenUnix(
		"unix",
		&net.UnixAddr{Name: socketPath, Net: "unix"},
	)
	if err != nil {
		return nil, fmt.Errorf("listen on Browser tool broker socket: %w", err)
	}
	if err := os.Chmod(socketPath, 0o660); err != nil {
		_ = listener.Close()
		_ = os.Remove(socketPath)
		return nil, err
	}
	info, err := os.Lstat(socketPath)
	if err != nil || info.Mode()&os.ModeSocket == 0 {
		_ = listener.Close()
		_ = os.Remove(socketPath)
		return nil, errors.New("cannot verify Browser tool broker socket")
	}
	ctx, cancel := context.WithCancel(parent)
	broker := &browserToolBroker{
		socketPath: socketPath,
		listener:   listener,
		cancel:     cancel,
		done:       make(chan struct{}),
		info:       info,
	}
	go broker.serve(ctx, host, lease, clientFactory, journal, onFirstAgentExecutor)
	return broker, nil
}

func (broker *browserToolBroker) serve(
	ctx context.Context,
	host string,
	lease *browserRunLease,
	clientFactory func() (browserplugin.Executor, error),
	journal *browserMutationJournal,
	onFirstAgentExecutor func(),
) {
	defer close(broker.done)
	for {
		connection, err := broker.listener.AcceptUnix()
		if err != nil {
			return
		}
		server := &browserplugin.Server{
			Host:    host,
			Version: lease.config.Version,
			IO: browserplugin.IO{
				Getenv: func(string) string { return "" },
			},
			ClientFactory: func() (browserplugin.Executor, error) {
				executor, err := clientFactory()
				if err == nil && onFirstAgentExecutor != nil {
					broker.agentProgressOnce.Do(onFirstAgentExecutor)
				}
				if err != nil || journal == nil {
					return executor, err
				}
				return journal.wrap(executor), nil
			},
			EvidenceSupplier: lease.browserEvidenceSnapshot,
		}
		_ = server.Serve(ctx, connection, connection)
		_ = connection.Close()
		if ctx.Err() != nil {
			return
		}
	}
}

func (broker *browserToolBroker) SocketPath() string {
	if broker == nil {
		return ""
	}
	return broker.socketPath
}

func (broker *browserToolBroker) Close() error {
	if broker == nil {
		return nil
	}
	var closeErr error
	broker.closeOnce.Do(func() {
		broker.cancel()
		closeErr = broker.listener.Close()
		<-broker.done
		info, err := os.Lstat(broker.socketPath)
		if errors.Is(err, os.ErrNotExist) {
			return
		}
		if err == nil && info.Mode()&os.ModeSocket != 0 && os.SameFile(info, broker.info) {
			err = os.Remove(broker.socketPath)
		}
		if closeErr == nil || errors.Is(closeErr, net.ErrClosed) {
			closeErr = err
		}
	})
	if errors.Is(closeErr, net.ErrClosed) {
		return nil
	}
	return closeErr
}
