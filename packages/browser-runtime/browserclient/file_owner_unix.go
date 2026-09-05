//go:build !windows

package browserclient

import (
	"os"
	"syscall"
)

func fileOwnedByCurrentUser(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == os.Geteuid()
}
