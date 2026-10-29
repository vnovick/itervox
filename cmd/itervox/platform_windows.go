//go:build windows

package main

import (
	"errors"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// Windows forms of the process and file primitives in platform_unix.go.

// detachDaemon makes a daemon started by `itervox quickstart` outlive this
// command and its console: no console of its own, and a new process group
// so a Ctrl+C typed here does not reach it.
func detachDaemon(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS,
	}
}

// fileOwner has no uid/gid to report on Windows.
func fileOwner(os.FileInfo) (uid, gid int, ok bool) { return 0, 0, false }

// stillActive is STILL_ACTIVE, the exit code of a process that has not exited.
const stillActive = 259

// probeProcess reports whether proc is still running. os.FindProcess
// opened a handle to it, so it exists (or existed); its exit code tells
// whether it is still alive. Signal(0) is not supported on Windows.
func probeProcess(proc *os.Process) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(proc.Pid))
	if err != nil {
		return false
	}
	defer func() { _ = windows.CloseHandle(h) }()
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == stillActive
}

// sendSignal kills proc: Windows can deliver no SIGTERM to another
// process, so `itervox stop` terminates the daemon without a graceful
// drain there (Ctrl+C in the daemon's own console drains). Its Job Objects
// close with it, which ends its agents and hooks.
func sendSignal(proc *os.Process, sig syscall.Signal) (syscall.Signal, error) {
	return syscall.SIGKILL, proc.Kill()
}

// isAddrInUseErrno reports whether a listen's syscall error means the
// address is taken (WSAEADDRINUSE on Windows).
func isAddrInUseErrno(err error) bool {
	return errors.Is(err, windows.WSAEADDRINUSE) || errors.Is(err, syscall.EADDRINUSE)
}
