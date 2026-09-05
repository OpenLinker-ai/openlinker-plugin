//go:build !windows

package agentexec

import "os"

func replaceFileAtomic(source, destination string) error { return os.Rename(source, destination) }
