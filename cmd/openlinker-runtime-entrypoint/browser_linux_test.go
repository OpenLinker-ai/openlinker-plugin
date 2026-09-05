//go:build linux

package main

import (
	"errors"
	"os"
	"syscall"
	"testing"
)

func TestPrepareBrowserMountsLeavesStandardProfileUnchanged(t *testing.T) {
	for _, profile := range []string{"", "standard", "STANDARD"} {
		t.Run(profile, func(t *testing.T) {
			t.Setenv("OPENLINKER_AGENT_EXECUTION_PROFILE", profile)
			if err := prepareBrowserMounts(); err != nil {
				t.Fatalf("prepare standard profile mounts: %v", err)
			}
		})
	}
}

func TestPrepareBrowserDirectorySkipsPrivilegedChangesWhenAlreadyCorrect(t *testing.T) {
	path := t.TempDir()
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}

	originalChown, originalChmod := browserChown, browserChmod
	t.Cleanup(func() {
		browserChown, browserChmod = originalChown, originalChmod
	})
	browserChown = func(string, int, int) error {
		return errors.New("unexpected chown")
	}
	browserChmod = func(string, os.FileMode) error {
		return errors.New("unexpected chmod")
	}

	if err := prepareBrowserDirectory(path, os.Geteuid(), os.Getegid(), 0o700); err != nil {
		t.Fatalf("prepare already-correct Browser directory: %v", err)
	}
}

func TestPrepareBrowserDirectoryCorrectsModeWithoutChangingOwnership(t *testing.T) {
	path := t.TempDir()
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}

	originalChown, originalChmod := browserChown, browserChmod
	t.Cleanup(func() {
		browserChown, browserChmod = originalChown, originalChmod
	})
	chownCalls := 0
	chmodCalls := 0
	browserChown = func(string, int, int) error {
		chownCalls++
		return syscall.EPERM
	}
	browserChmod = func(path string, mode os.FileMode) error {
		chmodCalls++
		return os.Chmod(path, mode)
	}

	if err := prepareBrowserDirectory(path, os.Geteuid(), os.Getegid(), 0o700); err != nil {
		t.Fatalf("prepare Browser directory mode: %v", err)
	}
	if chownCalls != 0 {
		t.Fatalf("ownership was already correct, chown calls = %d", chownCalls)
	}
	if chmodCalls != 1 {
		t.Fatalf("chmod calls = %d, want 1", chmodCalls)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("directory mode = %#o, want 0700", got)
	}
}
