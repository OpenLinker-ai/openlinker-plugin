//go:build unix

package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

type agentModeLock struct{ file *os.File }

func acquireAgentModeLock(stateDir string) (*agentModeLock, error) {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, err
	}
	// The image entrypoint separately owns .runtime-worker.lock for the
	// persistent mount. This lock belongs to the CLI Agent service itself so
	// local Codex and Claude hosts cannot start duplicate Workers.
	path := filepath.Join(stateDir, ".agent-mode.lock")
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open Agent mode ownership lock: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	closeOnError := func(err error) (*agentModeLock, error) {
		_ = file.Close()
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || !secretFileOwnedByCurrentUser(info) {
		return closeOnError(errors.New("Agent mode ownership lock must be an owner-only regular file"))
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return closeOnError(errors.New("Agent state is already serving another Runtime Worker"))
	}
	return &agentModeLock{file: file}, nil
}

func (lock *agentModeLock) release() error {
	if lock == nil || lock.file == nil {
		return nil
	}
	err := unix.Flock(int(lock.file.Fd()), unix.LOCK_UN)
	closeErr := lock.file.Close()
	lock.file = nil
	if err != nil {
		return err
	}
	return closeErr
}
