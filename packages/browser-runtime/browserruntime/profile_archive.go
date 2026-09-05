//go:build !windows

package browserruntime

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const (
	maxProfileArchiveBytes = int64(384 << 20)
	maxProfileArchiveFiles = 100_000
)

func writeProfileArchive(root string, output io.Writer) error {
	writer := tar.NewWriter(output)
	var total int64
	files := 0
	walkErr := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil || relative == "." || relative == ".." ||
			strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return errors.New("browser profile archive path is invalid")
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
			return nil
		}
		files++
		if files > maxProfileArchiveFiles {
			return errors.New("browser profile archive has too many entries")
		}
		header := &tar.Header{
			Name:    filepath.ToSlash(relative),
			ModTime: info.ModTime().UTC(),
		}
		if info.IsDir() {
			header.Typeflag = tar.TypeDir
			header.Mode = 0o700
		} else {
			if info.Size() < 0 || total > maxProfileArchiveBytes-info.Size() {
				return errors.New("browser profile archive exceeds the size limit")
			}
			total += info.Size()
			header.Typeflag = tar.TypeReg
			header.Mode = 0o600
			header.Size = info.Size()
		}
		if err := writer.WriteHeader(header); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			file, err := os.Open(path) // #nosec G304 -- path is rooted below the private profile work directory.
			if err != nil {
				return err
			}
			_, copyErr := io.CopyN(writer, file, info.Size())
			closeErr := file.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		}
		return nil
	})
	if walkErr != nil {
		_ = writer.Close()
		return fmt.Errorf("archive browser profile: %w", walkErr)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("close browser profile archive: %w", err)
	}
	return nil
}

func extractProfileArchive(input io.Reader, root string) error {
	reader := tar.NewReader(io.LimitReader(input, maxProfileArchiveBytes+(64<<20)))
	var total int64
	files := 0
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read browser profile archive: %w", err)
		}
		files++
		if files > maxProfileArchiveFiles {
			return errors.New("browser profile archive has too many entries")
		}
		target, err := profileArchiveTarget(root, header.Name)
		if err != nil {
			return err
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := mkdirProfilePath(root, target); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if header.Size < 0 || total > maxProfileArchiveBytes-header.Size {
				return errors.New("browser profile archive exceeds the size limit")
			}
			total += header.Size
			if err := mkdirProfilePath(root, filepath.Dir(target)); err != nil {
				return err
			}
			file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- target passed strict root containment.
			if err != nil {
				return fmt.Errorf("create browser profile entry: %w", err)
			}
			_, copyErr := io.CopyN(file, reader, header.Size)
			syncErr := file.Sync()
			closeErr := file.Close()
			if copyErr != nil {
				return fmt.Errorf("extract browser profile entry: %w", copyErr)
			}
			if syncErr != nil {
				return fmt.Errorf("sync browser profile entry: %w", syncErr)
			}
			if closeErr != nil {
				return fmt.Errorf("close browser profile entry: %w", closeErr)
			}
		default:
			return errors.New("browser profile archive contains a forbidden entry")
		}
	}
}

func profileArchiveTarget(root, raw string) (string, error) {
	if raw == "" || strings.ContainsRune(raw, 0) || filepath.IsAbs(raw) {
		return "", errors.New("browser profile archive path is invalid")
	}
	clean := filepath.Clean(filepath.FromSlash(raw))
	if clean == "." || clean == ".." ||
		strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("browser profile archive path escapes its root")
	}
	target := filepath.Join(root, clean)
	relative, err := filepath.Rel(root, target)
	if err != nil || relative == ".." ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("browser profile archive path escapes its root")
	}
	return target, nil
}

func mkdirProfilePath(root, path string) error {
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == ".." ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("browser profile directory escapes its root")
	}
	current := root
	if relative == "." {
		return nil
	}
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, component)
		info, statErr := os.Lstat(current)
		if errors.Is(statErr, fs.ErrNotExist) {
			if err := os.Mkdir(current, 0o700); err != nil {
				return fmt.Errorf("create browser profile directory: %w", err)
			}
			continue
		}
		if statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("browser profile directory is invalid")
		}
		if info.Mode().Perm()&0o077 != 0 {
			if err := os.Chmod(current, 0o700); err != nil {
				return fmt.Errorf("protect browser profile directory: %w", err)
			}
		}
	}
	return nil
}
