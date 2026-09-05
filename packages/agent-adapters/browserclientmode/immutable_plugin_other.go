//go:build !linux

package browserclientmode

import (
	"errors"
	"io/fs"
)

func validateImmutablePluginEntry(
	_ string,
	_ fs.DirEntry,
	required bool,
) error {
	if !required {
		return nil
	}
	return errors.New(
		"immutable native Browser Plugin verification requires Linux",
	)
}
