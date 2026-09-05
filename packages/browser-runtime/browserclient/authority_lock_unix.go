//go:build !windows

package browserclient

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

const AuthorityLockFile = ".browser-lease-authority.lock"

func LockAuthority(root string) (func() error, error) {
	root = filepath.Clean(root)
	if !filepath.IsAbs(root) {
		return nil, errors.New("Browser lease authority root must be absolute")
	}
	info, err := os.Lstat(root)
	if err != nil ||
		info.Mode()&os.ModeSymlink != 0 ||
		!info.IsDir() ||
		info.Mode().Perm()&0o077 != 0 ||
		!ownedByCurrentUser(info) {
		return nil, errors.New("Browser lease authority root is invalid")
	}
	lockPath := filepath.Join(root, AuthorityLockFile)
	descriptor, err := syscall.Open(
		lockPath,
		syscall.O_RDWR|syscall.O_CREAT|syscall.O_CLOEXEC|syscall.O_NOFOLLOW,
		0o600,
	)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(descriptor), lockPath)
	if file == nil {
		_ = syscall.Close(descriptor)
		return nil, errors.New("Browser lease authority lock is unavailable")
	}
	lockInfo, err := file.Stat()
	if err != nil ||
		!lockInfo.Mode().IsRegular() ||
		lockInfo.Mode().Perm()&0o077 != 0 ||
		!ownedByCurrentUser(lockInfo) {
		_ = file.Close()
		return nil, errors.New("Browser lease authority lock is invalid")
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		_ = file.Close()
		return nil, err
	}
	return func() error {
		unlockErr := syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		closeErr := file.Close()
		return errors.Join(unlockErr, closeErr)
	}, nil
}

func ownedByCurrentUser(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == os.Geteuid()
}
