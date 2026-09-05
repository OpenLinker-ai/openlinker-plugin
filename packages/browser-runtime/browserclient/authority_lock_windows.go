//go:build windows

package browserclient

import "errors"

const AuthorityLockFile = ".browser-lease-authority.lock"

func LockAuthority(string) (func() error, error) {
	return nil, errors.New("Browser lease authority is unavailable on Windows")
}
