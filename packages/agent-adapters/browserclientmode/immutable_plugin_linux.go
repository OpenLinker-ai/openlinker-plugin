//go:build linux

package browserclientmode

import (
	"errors"
	"io/fs"
	"syscall"
)

func validateImmutablePluginEntry(
	_ string,
	entry fs.DirEntry,
	required bool,
) error {
	if !required {
		return nil
	}
	info, err := entry.Info()
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 || stat.Gid != 0 {
		return errors.New("native Browser Plugin must be root-owned")
	}
	if info.Mode().Perm()&0o222 != 0 {
		return errors.New("native Browser Plugin must be read-only")
	}
	if !entry.IsDir() && !info.Mode().IsRegular() {
		return errors.New("native Browser Plugin contains a special file")
	}
	return nil
}
