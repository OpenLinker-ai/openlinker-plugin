//go:build linux

package main

import (
	"errors"
	"os"
	"runtime"
	"syscall"

	"golang.org/x/sys/unix"
)

var (
	runtimeStageDropIdentity = dropRuntimeStageIdentity
	runtimeStageExec         = syscall.Exec
	runtimeStageEffectiveUID = os.Geteuid
	runtimeStageEffectiveGID = os.Getegid
)

func runAgentStage(binary string) error {
	if runtimeStageEffectiveUID() != 0 || runtimeStageEffectiveGID() != 0 {
		return errors.New("official Provider image launcher must start as root before dropping to the Runtime UID")
	}
	environment, err := agentStageEnvironment(fixedProvider)
	if err != nil {
		return err
	}
	// Docker sends its stop signal to PID 1. Keep that PID while dropping to
	// the Runtime identity, then exec tini as the same UID as every process it
	// supervises. A root tini without CAP_KILL cannot signal a Runtime-UID
	// child after cap_drop: ALL; spawning the child here would therefore turn a
	// normal docker stop into SIGKILL after the grace period.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err = runtimeStageDropIdentity(); err != nil {
		return err
	}
	return runtimeStageExec(
		"/usr/bin/tini",
		[]string{"/usr/bin/tini", "--", binary},
		environment,
	)
}

func dropRuntimeStageIdentity() error {
	if err := unix.Prctl(unix.PR_SET_KEEPCAPS, 1, 0, 0, 0); err != nil {
		return errors.New("preserve Runtime capabilities while dropping identity")
	}
	if err := unix.Setgroups([]int{}); err != nil {
		return errors.New("clear Runtime supplementary groups")
	}
	if err := unix.Setresgid(runtimeGID, runtimeGID, runtimeGID); err != nil {
		return errors.New("drop Runtime group identity")
	}
	if err := unix.Setresuid(runtimeUID, runtimeUID, runtimeUID); err != nil {
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
		return errors.New("retain only Runtime identity-switch capabilities")
	}
	for _, capability := range []uintptr{unix.CAP_SETGID, unix.CAP_SETUID} {
		if err := unix.Prctl(
			unix.PR_CAP_AMBIENT,
			unix.PR_CAP_AMBIENT_RAISE,
			capability,
			0,
			0,
		); err != nil {
			return errors.New("raise Runtime ambient capabilities")
		}
	}
	if err := unix.Prctl(unix.PR_SET_KEEPCAPS, 0, 0, 0, 0); err != nil {
		return errors.New("lock Runtime capability retention")
	}
	if runtimeStageEffectiveUID() != runtimeUID || runtimeStageEffectiveGID() != runtimeGID {
		return errors.New("Runtime identity drop did not take effect")
	}
	return nil
}
