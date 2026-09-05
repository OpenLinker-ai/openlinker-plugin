//go:build linux

package main

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

const (
	runtimeUID  = 10001
	runtimeGID  = 10001
	providerUID = 10002
	providerGID = 10002
)

var fixedProvider string

func main() {
	if os.Getenv(codexAuthProxyStageEnv) == "1" {
		if err := runCodexAuthProxyStage(); err != nil {
			fatal(err)
		}
		return
	}
	if os.Getenv(providerExecStageEnv) == "1" {
		runProviderExecStage()
		return
	}
	target, err := fixedProviderBinary(fixedProvider)
	if err != nil {
		fatal(err)
	}
	if os.Geteuid() != runtimeUID || os.Getegid() != runtimeGID {
		fatal(errors.New("launcher must be invoked by the fixed Runtime UID/GID"))
	}
	argv := append([]string{target}, os.Args[1:]...)
	environment := os.Environ()
	if strings.EqualFold(strings.TrimSpace(fixedProvider), "codex") &&
		codexCommandNeedsAuthProxy(os.Args[1:]) {
		var launch *codexProxyLaunch
		launch, err = prepareCodexAuthProxy(argv, environment)
		if err != nil {
			fatal(err)
		}
		if launch != nil {
			os.Exit(runCodexWithAuthProxy(launch))
		}
	}
	runtime.LockOSThread()
	if err := dropProviderPrivileges(); err != nil {
		fatal(err)
	}
	if err := syscall.Exec(target, argv, environment); err != nil {
		fatal(errors.New("start fixed Provider binary"))
	}
}

func codexCommandNeedsAuthProxy(args []string) bool {
	if len(args) == 0 {
		return true
	}
	switch strings.TrimSpace(args[0]) {
	case "plugin", "mcp", "doctor", "features", "--version", "-V":
		return false
	default:
		return true
	}
}

func runProviderExecStage() {
	target, err := fixedProviderBinary(fixedProvider)
	if err != nil {
		fatal(err)
	}
	if os.Geteuid() != runtimeUID || os.Getegid() != runtimeGID {
		fatal(errors.New("Provider exec stage must start as the fixed Runtime UID/GID"))
	}
	environment := removeEnvironment(os.Environ(), providerExecStageEnv)
	runtime.LockOSThread()
	if err := dropProviderPrivileges(); err != nil {
		fatal(err)
	}
	argv := append([]string{target}, os.Args[1:]...)
	if err := syscall.Exec(target, argv, environment); err != nil {
		fatal(errors.New("start fixed Provider binary"))
	}
}

func fixedProviderBinary(provider string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "codex":
		return "/usr/local/bin/codex", nil
	case "claude":
		return "/usr/local/bin/claude", nil
	default:
		return "", errors.New("launcher Provider is not fixed at image build time")
	}
}

func dropProviderPrivileges() error {
	if err := unix.Setgroups([]int{}); err != nil {
		return errors.New("clear Provider supplementary groups")
	}
	if err := unix.Setresgid(providerGID, providerGID, providerGID); err != nil {
		return errors.New("drop Provider group identity")
	}
	if err := unix.Setresuid(providerUID, providerUID, providerUID); err != nil {
		return errors.New("drop Provider user identity")
	}
	if err := unix.Prctl(unix.PR_CAP_AMBIENT, unix.PR_CAP_AMBIENT_CLEAR_ALL, 0, 0, 0); err != nil {
		return errors.New("clear Provider ambient capabilities")
	}
	header := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	data := [2]unix.CapUserData{}
	if err := unix.Capset(&header, &data[0]); err != nil {
		return errors.New("clear Provider capabilities")
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return errors.New("lock Provider privilege boundary")
	}
	if os.Geteuid() != providerUID || os.Getegid() != providerGID {
		return errors.New("Provider identity drop did not take effect")
	}
	return nil
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "openlinker Provider launcher:", err)
	os.Exit(1)
}
