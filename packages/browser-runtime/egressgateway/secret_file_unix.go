//go:build unix

package egressgateway

import (
	"errors"
	"io/fs"
	"os"
	"syscall"
)

func validateSecretFileOwner(info fs.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() {
		return errors.New("must be owned by the current process user")
	}
	return nil
}
