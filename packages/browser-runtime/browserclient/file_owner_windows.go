//go:build windows

package browserclient

import "os"

func fileOwnedByCurrentUser(os.FileInfo) bool {
	return true
}
