//go:build unix

package main

import (
	"os"
	"os/exec"
	"syscall"
)

// Process and file primitives whose Unix and Windows forms differ
// (platform_windows.go). The Unix bodies are the code these call sites
// carried inline before the Windows port.

// detachDaemon makes a daemon started by `itervox quickstart` outlive this
// command and its terminal: its own session.
func detachDaemon(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

// fileOwner returns info's owning uid and gid.
func fileOwner(info os.FileInfo) (uid, gid int, ok bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, false
	}
	return int(st.Uid), int(st.Gid), true
}

// probeProcess reports whether proc is alive and the calling user can
// signal it. Signal 0 performs no delivery but returns an error if the
// target is gone or not owned by the caller — the canonical POSIX probe.
func probeProcess(proc *os.Process) bool {
	return proc.Signal(syscall.Signal(0)) == nil
}

// sendSignal sends sig to proc and returns the signal actually sent.
func sendSignal(proc *os.Process, sig syscall.Signal) (syscall.Signal, error) {
	return sig, proc.Signal(sig)
}

// isAddrInUseErrno reports whether a listen's syscall error means the
// address is taken.
func isAddrInUseErrno(err error) bool {
	return err == syscall.EADDRINUSE
}
