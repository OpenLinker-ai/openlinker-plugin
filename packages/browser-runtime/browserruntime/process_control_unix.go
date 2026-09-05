//go:build !windows

package browserruntime

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

const engineProcessWaitDelay = 2 * time.Second

func configureEngineProcess(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.WaitDelay = engineProcessWaitDelay
}

func killEngineProcessGroup(command *exec.Cmd) error {
	if command == nil || command.Process == nil {
		return os.ErrProcessDone
	}
	err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	if err == nil {
		return nil
	}
	if killErr := command.Process.Kill(); killErr != nil &&
		!errors.Is(killErr, os.ErrProcessDone) {
		return errors.Join(err, killErr)
	}
	return err
}
