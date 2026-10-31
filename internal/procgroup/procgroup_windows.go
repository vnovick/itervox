//go:build windows

package procgroup

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Configure is the Windows counterpart of the Unix Setpgid wiring. The
// child gets its own console process group (CREATE_NEW_PROCESS_GROUP), so a
// Ctrl+C typed at the daemon's console does not reach it — the role Setpgid
// plays for the terminal's foreground group on Unix — and context
// cancellation kills the whole tree via Kill. Start must be used instead of
// cmd.Start for the tree kill to apply: it is Start that puts the child in a
// Job Object. A command started with plain cmd.Start still has its direct
// child killed on cancellation.
//
// waitDelay has the Unix meaning: it bounds how long cmd.Wait keeps waiting
// on stdout/stderr pipes after Cancel fires or the child exits.
//
// Configure overwrites cmd.SysProcAttr; call it before Start.
func Configure(cmd *exec.Cmd, waitDelay time.Duration) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		if killJob(cmd.Process.Pid) {
			return nil
		}
		// No job (plain cmd.Start, or job creation failed): kill the direct
		// child through exec's own handle, which cannot name a reused pid.
		return cmd.Process.Kill()
	}
	cmd.WaitDelay = waitDelay
}

// Start starts cmd suspended, places it in a new Job Object with
// JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE, then lets it run. Every process the
// child creates afterwards inherits the job, so Kill (and cmd.Cancel from
// Configure) ends the whole tree with one TerminateJobObject, and the OS
// ends it as well if the daemon dies without cancelling: the job handle is
// closed with the daemon's process, which is what the Unix orphan ledger
// (CORE-042) has to do by hand after a restart.
//
// When the child exits on its own, the kill-on-close limit is cleared before
// the job handle is closed, so — as on Unix, where a group outlives its
// leader — descendants it deliberately left running are not killed.
//
// If the job cannot be created or assigned, the child still runs (logged)
// and cancellation falls back to killing the direct child. If the child
// cannot be resumed it is killed and reaped, and the error is returned.
func Start(cmd *exec.Cmd) error {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_SUSPENDED
	if err := cmd.Start(); err != nil {
		return err
	}
	pid := cmd.Process.Pid
	j, jobErr := newJob(pid)
	if jobErr != nil {
		slog.Warn("procgroup: child runs without a job object; cancellation kills only the direct child",
			"pid", pid, "error", jobErr)
	}
	if err := resumeProcess(uint32(pid)); err != nil {
		if j != nil {
			j.release(true)
		}
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return fmt.Errorf("procgroup: resume pid %d: %w", pid, err)
	}
	return nil
}

// Kill terminates the job of the child started by Start with pid, ending
// every process in it. For a pid without a job it kills that one process
// and returns that error unchanged.
func Kill(pid int) error {
	if killJob(pid) {
		return nil
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}

// job is one child's Job Object. release runs once: on Kill (terminate
// every member) or when the child exits by itself (clear kill-on-close,
// leaving any descendants running).
type job struct {
	pid    int
	handle windows.Handle
	once   sync.Once
}

// jobs maps a child's pid to its *job while the child is alive. newJob's
// watcher holds a handle to the child until after the entry is removed, so
// the pid cannot be recycled while it names a job here.
var jobs sync.Map

func newJob(pid int) (*job, error) {
	proc, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return nil, fmt.Errorf("open process: %w", err)
	}
	h, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		_ = windows.CloseHandle(proc)
		return nil, fmt.Errorf("create job: %w", err)
	}
	if err := setKillOnClose(h, true); err != nil {
		_ = windows.CloseHandle(h)
		_ = windows.CloseHandle(proc)
		return nil, fmt.Errorf("set kill-on-close: %w", err)
	}
	if err := windows.AssignProcessToJobObject(h, proc); err != nil {
		_ = windows.CloseHandle(h)
		_ = windows.CloseHandle(proc)
		return nil, fmt.Errorf("assign process: %w", err)
	}
	j := &job{pid: pid, handle: h}
	jobs.Store(pid, j)
	go func() {
		_, _ = windows.WaitForSingleObject(proc, windows.INFINITE)
		j.release(false)
		_ = windows.CloseHandle(proc)
	}()
	return j, nil
}

// release ends the job's registration. kill terminates every member first;
// otherwise kill-on-close is cleared so closing the handle leaves them be.
func (j *job) release(kill bool) {
	j.once.Do(func() {
		jobs.CompareAndDelete(j.pid, j)
		if kill {
			_ = windows.TerminateJobObject(j.handle, 1)
		} else {
			_ = setKillOnClose(j.handle, false)
		}
		_ = windows.CloseHandle(j.handle)
	})
}

// killJob terminates pid's job and reports whether pid had one.
func killJob(pid int) bool {
	v, ok := jobs.Load(pid)
	if !ok {
		return false
	}
	v.(*job).release(true)
	return true
}

func setKillOnClose(h windows.Handle, on bool) error {
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	if on {
		info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	}
	_, err := windows.SetInformationJobObject(h, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)))
	return err
}

// resumeProcess resumes every thread of pid. A process created with
// CREATE_SUSPENDED has exactly one, its primary thread; exec does not hand
// out the thread handle, so it is found through a thread snapshot.
func resumeProcess(pid uint32) error {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return fmt.Errorf("thread snapshot: %w", err)
	}
	defer func() { _ = windows.CloseHandle(snap) }()
	var te windows.ThreadEntry32
	te.Size = uint32(unsafe.Sizeof(te))
	resumed := 0
	for err = windows.Thread32First(snap, &te); err == nil; err = windows.Thread32Next(snap, &te) {
		if te.OwnerProcessID != pid {
			continue
		}
		th, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, te.ThreadID)
		if err != nil {
			return fmt.Errorf("open thread %d: %w", te.ThreadID, err)
		}
		_, rerr := windows.ResumeThread(th)
		_ = windows.CloseHandle(th)
		if rerr != nil {
			return fmt.Errorf("resume thread %d: %w", te.ThreadID, rerr)
		}
		resumed++
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return fmt.Errorf("walk threads: %w", err)
	}
	if resumed == 0 {
		return errors.New("no thread found")
	}
	return nil
}

// ConfigureDetached is Configure on Windows: there is no controlling
// terminal to leave, and CREATE_NEW_PROCESS_GROUP already keeps the
// console's Ctrl+C away from the child.
func ConfigureDetached(cmd *exec.Cmd, waitDelay time.Duration) {
	Configure(cmd, waitDelay)
}
