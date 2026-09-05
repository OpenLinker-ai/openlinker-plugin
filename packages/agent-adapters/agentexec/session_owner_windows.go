//go:build windows

package agentexec

import "os"

func sessionFileOwnedByCurrentUser(os.FileInfo) bool { return true }
