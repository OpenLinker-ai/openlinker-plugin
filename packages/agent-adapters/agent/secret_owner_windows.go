//go:build windows

package agent

import "os"

func secretFileOwnedByCurrentUser(os.FileInfo) bool {
	// Windows ACL evaluation is delegated to the OS. The regular-file,
	// non-symlink, size, and direct-vs-file checks still apply.
	return true
}
