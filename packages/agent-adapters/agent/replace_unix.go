//go:build !windows

package agent

import "os"

func replaceFileAtomic(source, destination string) error { return os.Rename(source, destination) }
