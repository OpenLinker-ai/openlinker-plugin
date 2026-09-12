//go:build unix || windows

package agent

import (
	"os"
	"path/filepath"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/appfiles"
)

type agentModeLock struct{ lock *appfiles.Lock }

func acquireAgentModeLock(stateDir string) (*agentModeLock, error) {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, err
	}
	// The product retains this path and the acquire/release lifecycle. The
	// image's .runtime-worker.lock remains a different ownership boundary.
	lock, err := appfiles.AcquireLock(filepath.Join(stateDir, ".agent-mode.lock"))
	if err != nil {
		return nil, err
	}
	return &agentModeLock{lock: lock}, nil
}

func (lock *agentModeLock) release() error {
	if lock == nil {
		return nil
	}
	return lock.lock.Release()
}
