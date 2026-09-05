//go:build linux

package main

import (
	"errors"
	"os"
	"runtime"
	"syscall"

	"golang.org/x/sys/unix"
)

const liveStageEnvironment = "OPENLINKER_BROWSER_PROVIDER_LIVE_STAGE"

func prepareRuntimeIdentity() error {
	if os.Getenv(liveStageEnvironment) == "1" {
		if os.Geteuid() != 10001 || os.Getegid() != 10001 {
			return errors.New("live acceptance Runtime stage has the wrong identity")
		}
		return nil
	}
	if os.Geteuid() != 0 || os.Getegid() != 0 {
		return errors.New("live acceptance image must start as root")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := unix.Prctl(unix.PR_SET_KEEPCAPS, 1, 0, 0, 0); err != nil {
		return errors.New("preserve Runtime capabilities")
	}
	if err := unix.Setgroups([]int{}); err != nil {
		return errors.New("clear Runtime supplementary groups")
	}
	if err := unix.Setresgid(10001, 10001, 10001); err != nil {
		return errors.New("drop Runtime group identity")
	}
	if err := unix.Setresuid(10001, 10001, 10001); err != nil {
		return errors.New("drop Runtime user identity")
	}
	const required = uint32(1<<unix.CAP_SETGID | 1<<unix.CAP_SETUID)
	header := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	data := [2]unix.CapUserData{{
		Effective:   required,
		Permitted:   required,
		Inheritable: required,
	}}
	if err := unix.Capset(&header, &data[0]); err != nil {
		return errors.New("retain Runtime identity-switch capabilities")
	}
	for _, capability := range []uintptr{unix.CAP_SETGID, unix.CAP_SETUID} {
		if err := unix.Prctl(unix.PR_CAP_AMBIENT, unix.PR_CAP_AMBIENT_RAISE, capability, 0, 0); err != nil {
			return errors.New("raise Runtime ambient capabilities")
		}
	}
	if err := unix.Prctl(unix.PR_SET_KEEPCAPS, 0, 0, 0, 0); err != nil {
		return errors.New("lock Runtime capability retention")
	}
	environment := setEnvironment(os.Environ(), map[string]string{liveStageEnvironment: "1"})
	return syscall.Exec(os.Args[0], os.Args, environment)
}
