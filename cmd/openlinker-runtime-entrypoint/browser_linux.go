//go:build linux

package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

const (
	officialBrowserControlRoot = "/browser-control"
	officialBrowserBrokerRoot  = "/browser-tool"
)

var (
	browserChown = os.Chown
	browserChmod = os.Chmod
)

func prepareBrowserMounts() error {
	profile := strings.ToLower(strings.TrimSpace(os.Getenv("OPENLINKER_AGENT_EXECUTION_PROFILE")))
	if profile == "" || profile == "standard" {
		return nil
	}
	if profile != "browser" {
		return errors.New("OPENLINKER_AGENT_EXECUTION_PROFILE must be standard or browser")
	}
	if os.Geteuid() != runtimeUID || os.Getegid() != runtimeGID {
		return errors.New("Browser mount initialization requires the fixed Runtime UID/GID")
	}
	for _, path := range []string{
		officialBrowserControlRoot,
		filepath.Join(officialBrowserControlRoot, "leases"),
	} {
		if err := prepareBrowserDirectory(path, runtimeUID, runtimeGID, 0o700); err != nil {
			return err
		}
	}
	if err := prepareBrowserDirectory(
		officialBrowserBrokerRoot,
		runtimeUID,
		providerGID,
		os.ModeSetgid|0o710,
	); err != nil {
		return err
	}
	credentialPath := filepath.Join(officialBrowserControlRoot, "channel-credential")
	if err := ensureBrowserChannelCredential(credentialPath); err != nil {
		return err
	}
	return nil
}

func prepareBrowserDirectory(path string, uid, gid int, mode os.FileMode) error {
	if err := os.MkdirAll(path, mode.Perm()); err != nil {
		return fmt.Errorf("create Browser directory %s: %w", path, err)
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("Browser path %s must be a real directory", path)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("inspect Browser directory ownership: %s", path)
	}
	if int(stat.Uid) != uid || int(stat.Gid) != gid {
		if err := browserChown(path, uid, gid); err != nil {
			return fmt.Errorf("set Browser directory ownership: %w", err)
		}
	}
	modeMask := os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky
	if info.Mode()&modeMask != mode&modeMask {
		if err := browserChmod(path, mode); err != nil {
			return fmt.Errorf("protect Browser directory: %w", err)
		}
	}
	return nil
}

func ensureBrowserChannelCredential(path string) error {
	info, err := os.Lstat(path)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 ||
			!info.Mode().IsRegular() ||
			info.Mode().Perm() != 0o600 ||
			info.Size() < 32 ||
			info.Size() > 512 {
			return errors.New("existing Browser channel credential is invalid")
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || int(stat.Uid) != runtimeUID || int(stat.Gid) != runtimeGID {
			return errors.New("existing Browser channel credential ownership is invalid")
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return errors.New("generate Browser channel credential")
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if errors.Is(err, os.ErrExist) {
		return ensureBrowserChannelCredential(path)
	}
	if err != nil {
		return errors.New("create Browser channel credential")
	}
	value := hex.EncodeToString(secret[:]) + "\n"
	if _, err := file.WriteString(value); err != nil {
		_ = file.Close()
		return errors.New("write Browser channel credential")
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return errors.New("sync Browser channel credential")
	}
	if err := file.Close(); err != nil {
		return errors.New("close Browser channel credential")
	}
	if os.Geteuid() != runtimeUID || os.Getegid() != runtimeGID {
		if err := os.Chown(path, runtimeUID, runtimeGID); err != nil {
			return errors.New("set Browser channel credential ownership")
		}
	}
	return nil
}
