//go:build windows

package agent

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

type agentModeLock struct{ file *os.File }

func acquireAgentModeLock(stateDir string) (*agentModeLock, error) {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(stateDir, ".agent-mode.lock")
	pathUTF16, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(pathUTF16, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_ALWAYS, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, errors.New("Agent state is already serving another Runtime Worker")
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil || info.FileAttributes&(windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_REPARSE_POINT) != 0 {
		_ = windows.CloseHandle(handle)
		return nil, errors.New("Agent mode ownership lock must be a regular non-reparse file")
	}
	return &agentModeLock{file: os.NewFile(uintptr(handle), path)}, nil
}

func (lock *agentModeLock) release() error {
	if lock == nil || lock.file == nil {
		return nil
	}
	err := lock.file.Close()
	lock.file = nil
	return err
}
