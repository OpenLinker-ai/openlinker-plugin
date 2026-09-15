//go:build linux

package browserprofile

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// All source and verification opens are dirfd-relative and never create,
// chmod, truncate, quarantine or prune anything.
func migrationDirectory(path string, private bool) (*os.File, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, ErrInvalidConfiguration
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for index, part := range parts {
		if part == "" {
			continue
		}
		next, err := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		_ = unix.Close(fd)
		if err != nil {
			return nil, err
		}
		fd = next
		var stat unix.Stat_t
		if err := unix.Fstat(fd, &stat); err != nil {
			_ = unix.Close(fd)
			return nil, err
		}
		owner := stat.Uid == uint32(os.Geteuid())
		trusted := owner || stat.Uid == 0
		// Sticky ancestors owned by root or this uid protect this uid's entries
		// from other users. The private leaf is still strictly owner-only.
		writable := stat.Mode&0o022 != 0 && !(trusted && stat.Mode&unix.S_ISVTX != 0)
		if !trusted || writable || (private && index == len(parts)-1 && (!owner || stat.Mode&0o077 != 0)) {
			_ = unix.Close(fd)
			return nil, ErrInvalidConfiguration
		}
	}
	return os.NewFile(uintptr(fd), path), nil
}

func migrationChildDirectory(parent *os.File, name string) (*os.File, error) {
	if name == "" || name == "." || name == ".." || strings.Contains(name, "/") {
		return nil, ErrInvalidConfiguration
	}
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	if stat.Uid != uint32(os.Geteuid()) || stat.Mode&0o077 != 0 {
		_ = unix.Close(fd)
		return nil, ErrInvalidConfiguration
	}
	return os.NewFile(uintptr(fd), filepath.Join(parent.Name(), name)), nil
}

func migrationFile(parent *os.File, name string, limit int64) (*os.File, error) {
	if name == "" || name == "." || name == ".." || strings.Contains(name, "/") {
		return nil, ErrInvalidConfiguration
	}
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Uid != uint32(os.Geteuid()) ||
		stat.Mode&0o077 != 0 || stat.Nlink != 1 || stat.Size < 0 || stat.Size > limit {
		_ = unix.Close(fd)
		return nil, ErrInvalidConfiguration
	}
	return os.NewFile(uintptr(fd), filepath.Join(parent.Name(), name)), nil
}

func migrationReadFile(parent *os.File, name string, limit int64) ([]byte, error) {
	file, err := migrationFile(parent, name, limit)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if int64(len(raw)) > limit {
		return nil, ErrInvalidConfiguration
	}
	return raw, err
}

func migrationAbsoluteFile(path string, limit int64) ([]byte, error) {
	parent, err := migrationDirectory(filepath.Dir(path), true)
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	return migrationReadFile(parent, filepath.Base(path), limit)
}

func migrationLock(dir *os.File) (*os.File, error) {
	file, err := migrationFile(dir, ".manager.lock", 0)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK) {
			return nil, ErrProfileStoreLocked
		}
		return nil, err
	}
	return file, nil
}

func migrationSameFile(left, right *os.File) bool {
	l, le := left.Stat()
	r, re := right.Stat()
	return le == nil && re == nil && os.SameFile(l, r)
}

type migrationStat struct {
	Device uint64
	Inode  uint64
	Size   int64
	Mode   uint32
	UID    uint32
	GID    uint32
	Links  uint64
	MTime  unix.Timespec
	CTime  unix.Timespec
}

func migrationFileStat(file *os.File) (migrationStat, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return migrationStat{}, err
	}
	return migrationStat{uint64(stat.Dev), stat.Ino, stat.Size, stat.Mode, stat.Uid, stat.Gid, uint64(stat.Nlink), stat.Mtim, stat.Ctim}, nil
}

type migrationSnapshotReader struct {
	root        *os.File
	files       []*os.File
	stats       []migrationStat
	metadata    Metadata
	cipher      *payloadCipher
	payload     *os.File
	checkpoint  string
	fingerprint MigrationSnapshot
}

func (snapshot *migrationSnapshotReader) close() {
	if snapshot == nil {
		return
	}
	if snapshot.cipher != nil {
		snapshot.cipher.close()
	}
	for _, file := range snapshot.files {
		_ = file.Close()
	}
}

func openMigrationSnapshot(ctx context.Context, rootDir *os.File, identity Identity, rootKey *RootKey, contract storageContract) (_ *migrationSnapshotReader, resultErr error) {
	snapshot := &migrationSnapshotReader{root: rootDir}
	defer func() {
		if resultErr != nil {
			snapshot.close()
		}
	}()
	dir := rootDir
	digest := profileDigestForContract(identity, contract)
	for _, name := range []string{"profiles", digest} {
		next, err := migrationChildDirectory(dir, name)
		if err != nil {
			return nil, err
		}
		snapshot.files = append(snapshot.files, next)
		dir = next
	}
	pointer, err := migrationFile(dir, currentFileName, maxCurrentBytes)
	if err != nil {
		return nil, err
	}
	snapshot.files = append(snapshot.files, pointer)
	raw, err := io.ReadAll(io.LimitReader(pointer, maxCurrentBytes+1))
	if err != nil {
		return nil, err
	}
	var current currentRecord
	if err := strictMigrationJSON(raw, &current); err != nil || current.Version != currentVersion || !validCheckpointID(current.Checkpoint) {
		return nil, ErrProfileCorrupt
	}
	snapshot.checkpoint = current.Checkpoint
	info, err := pointer.Stat()
	if err != nil {
		return nil, err
	}
	snapshot.fingerprint = MigrationSnapshot{ProfileDirectorySHA256: digest, CheckpointIDSHA256: migrationSHA([]byte(current.Checkpoint)), CurrentPointerMTime: info.ModTime().UTC().Format(time.RFC3339Nano)}
	for _, name := range []string{"checkpoints", current.Checkpoint} {
		next, err := migrationChildDirectory(dir, name)
		if err != nil {
			return nil, err
		}
		snapshot.files = append(snapshot.files, next)
		dir = next
	}
	metadata, err := migrationFile(dir, metadataFileName, maxMetadataBytes)
	if err != nil {
		return nil, err
	}
	snapshot.files = append(snapshot.files, metadata)
	raw, err = io.ReadAll(io.LimitReader(metadata, maxMetadataBytes+1))
	if err != nil {
		return nil, err
	}
	// Strict duplicate detection supplements the existing closed metadata codec.
	if err := strictMigrationJSON(raw, &snapshot.metadata); err != nil {
		return nil, ErrProfileCorrupt
	}
	snapshot.metadata, err = parseMetadataForContract(raw, contract)
	if err != nil {
		return nil, err
	}
	snapshot.fingerprint.MetadataSHA256 = migrationSHA(raw)
	dek, err := unwrapForContract(snapshot.metadata, identity, rootKey, contract)
	if err != nil {
		return nil, err
	}
	snapshot.cipher = &payloadCipher{identity: identity, random: rand.Reader, contract: contract}
	copy(snapshot.cipher.key[:], dek)
	clear(dek)
	snapshot.payload, err = migrationFile(dir, payloadFileName, migrationMaxCiphertext)
	if err != nil {
		return nil, err
	}
	snapshot.files = append(snapshot.files, snapshot.payload)
	hash := sha256.New()
	if n, err := io.Copy(hash, io.LimitReader(&migrationContextReader{ctx: ctx, reader: snapshot.payload}, migrationMaxCiphertext+1)); err != nil || n > migrationMaxCiphertext {
		if err == nil {
			err = ErrProfileCorrupt
		}
		return nil, err
	}
	snapshot.fingerprint.PayloadSHA256 = hex.EncodeToString(hash.Sum(nil))
	for _, file := range snapshot.files {
		stat, err := migrationFileStat(file)
		if err != nil {
			return nil, err
		}
		snapshot.stats = append(snapshot.stats, stat)
	}
	return snapshot, nil
}

func (snapshot *migrationSnapshotReader) decrypt(ctx context.Context, writer io.Writer) error {
	if _, err := snapshot.payload.Seek(0, io.SeekStart); err != nil {
		return err
	}
	bounded := &migrationLimitWriter{ctx: ctx, writer: writer, remaining: migrationMaxPlaintext}
	limited := &io.LimitedReader{R: &migrationContextReader{ctx: ctx, reader: snapshot.payload}, N: migrationMaxCiphertext + 1}
	err := snapshot.cipher.decrypt(bounded, limited)
	if limited.N == 0 {
		return ErrProfileCorrupt
	}
	return err
}

// Translate bind-mount aliases using Linux's own mount namespace metadata.
// Device/inode equality alone misses a target parent bound from a descendant
// of the source. No mount paths are included in reports or errors.
func migrationMountAlias(source, parent string) (bool, error) {
	file, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return false, err
	}
	defer file.Close()
	type location struct {
		device, path string
		length       int
	}
	locations := [2]location{}
	paths := [2]string{source, parent}
	reader := &io.LimitedReader{R: file, N: 1 << 20}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 64<<10)
	unescape := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 7 {
			return false, ErrInvalidConfiguration
		}
		root, mount := unescape.Replace(fields[3]), unescape.Replace(fields[4])
		if !filepath.IsAbs(root) || !filepath.IsAbs(mount) {
			return false, ErrInvalidConfiguration
		}
		for index, path := range paths {
			if path != mount && !strings.HasPrefix(path, strings.TrimSuffix(mount, "/")+"/") {
				continue
			}
			if len(mount) < locations[index].length {
				continue
			}
			relative, err := filepath.Rel(mount, path)
			if err != nil {
				return false, err
			}
			locations[index] = location{device: fields[2], path: filepath.Join(root, relative), length: len(mount)}
		}
	}
	if scanner.Err() != nil || reader.N == 0 || locations[0].length == 0 || locations[1].length == 0 {
		return false, ErrInvalidConfiguration
	}
	return locations[0].device == locations[1].device && (locations[0].path == locations[1].path || strings.HasPrefix(locations[1].path, locations[0].path+"/")), nil
}

func (snapshot *migrationSnapshotReader) verify(ctx context.Context, identity Identity, key *RootKey, contract storageContract) error {
	// Re-open from the absolute trusted chain, rather than only rechecking old
	// descriptors that might now refer to an unlinked snapshot.
	dir, err := migrationDirectory(snapshot.root.Name(), true)
	if err != nil {
		return err
	}
	defer dir.Close()
	if !migrationSameFile(snapshot.root, dir) {
		return ErrProfileCorrupt
	}
	other, err := openMigrationSnapshot(ctx, dir, identity, key, contract)
	if err != nil {
		return err
	}
	defer other.close()
	if snapshot.fingerprint != other.fingerprint || len(snapshot.stats) != len(other.stats) {
		return ErrProfileCorrupt
	}
	for index := range snapshot.stats {
		if snapshot.stats[index] != other.stats[index] {
			return ErrProfileCorrupt
		}
	}
	return nil
}

type migrationLimitWriter struct {
	ctx       context.Context
	writer    io.Writer
	remaining int64
	count     int64
}

func (writer *migrationLimitWriter) Write(buffer []byte) (int, error) {
	if err := writer.ctx.Err(); err != nil {
		return 0, err
	}
	if int64(len(buffer)) > writer.remaining {
		return 0, ErrProfileCorrupt
	}
	n, err := writer.writer.Write(buffer)
	writer.remaining -= int64(n)
	writer.count += int64(n)
	return n, err
}

func migrationWriteAt(dir *os.File, name string, raw []byte) error {
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), name)
	writeErr := writeAll(file, raw)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	return errors.Join(writeErr, file.Close())
}
