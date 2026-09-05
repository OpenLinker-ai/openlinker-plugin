//go:build !windows

package browserruntime

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"
)

func (server *Server) Open() error {
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.closed {
		return errors.New("browser server is closed")
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
	address := &net.UnixAddr{Name: server.options.SocketPath, Net: "unix"}
	listener, err := net.ListenUnix("unix", address)
	if err != nil {
		return fmt.Errorf("listen on browser Unix socket: %w", err)
	}
	if err := os.Chmod(server.options.SocketPath, server.options.SocketMode.Perm()); err != nil {
		_ = listener.Close()
		_ = os.Remove(server.options.SocketPath)
		return fmt.Errorf("protect browser Unix socket: %w", err)
	}
	socketInfo, err := os.Lstat(server.options.SocketPath)
	if err != nil || socketInfo.Mode()&os.ModeSocket == 0 {
		_ = listener.Close()
		_ = os.Remove(server.options.SocketPath)
		return errors.New("cannot verify browser Unix socket identity")
	}
	server.listener = listener
	server.socketInfo = socketInfo
	return nil
}

func validateSocketParent(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("browser socket parent must be a real directory")
	}
	if info.Mode().Perm()&0o007 != 0 {
		return errors.New("browser socket parent must not be accessible by other users")
	}
	return nil
}

func removeStaleSocket(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || info.Mode()&os.ModeSocket == 0 {
		return errors.New("browser socket path exists and is not a Unix socket")
	}
	connection, dialErr := net.DialTimeout("unix", path, 100*time.Millisecond)
	if dialErr == nil {
		_ = connection.Close()
		return errors.New("browser Unix socket is already active")
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove stale browser Unix socket: %w", err)
	}
	return nil
}

func removeOwnedSocket(path string, expected os.FileInfo) error {
	if expected == nil {
		return nil
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || info.Mode()&os.ModeSocket == 0 {
		return nil
	}
	if !os.SameFile(info, expected) {
		return nil
	}
	return os.Remove(path)
}
