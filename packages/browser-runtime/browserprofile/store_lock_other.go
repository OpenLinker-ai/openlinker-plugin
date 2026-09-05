//go:build !unix

package browserprofile

import (
	"os"
)

func acquireStoreLock(string) (*os.File, error) {
	return nil, ErrInvalidConfiguration
}

func releaseStoreLock(file *os.File) error {
	if file == nil {
		return nil
	}
	return file.Close()
}
