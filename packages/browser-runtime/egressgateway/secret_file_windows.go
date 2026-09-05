//go:build windows

package egressgateway

import (
	"errors"
	"io/fs"
)

func validateSecretFileOwner(fs.FileInfo) error {
	return errors.New(
		"is unsupported on Windows; use the direct environment variable",
	)
}
