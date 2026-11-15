//go:build windows

package procgroup

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// helperEnv selects the role the test binary plays when re-executed by the
// tests below: "parent" starts a "child" and prints its pid, then waits;
// "child" just waits. Both are killed (or exit) long before the timeout.
const helperEnv = "ITERVOX_PROCGROUP_HELPER"

// TestHelperProcess is not a real test: it is the process tree the tests
// below start, by re-executing the test binary with helperEnv set.
func TestHelperProcess(t *testing.T) {
	switch os.Getenv(helperEnv) {
	case "parent":
		child := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$")
		child.Env = append(os.Environ(), helperEnv+"=child")
		if err := child.Start(); err != nil {
			fmt.Println("start child:", err)
			os.Exit(2)
		}
		fmt.Println(child.Process.Pid)
		if os.Getenv("ITERVOX_PROCGROUP_PARENT_EXITS") == "1" {
			os.Exit(0) // leave the child running, as a hook that backgrounds a job does
		}
		time.Sleep(60 * time.Second)
		os.Exit(0)
	case "child":
		time.Sleep(60 * time.Second)
		os.Exit(0)
	}
}

// startTree starts the helper parent through Configure + Start and returns
// it with a handle on the grandchild it spawned.
func startTree(t *testing.T, ctx context.Context, extraEnv ...string) (*exec.Cmd, windows.Handle) {
	t.Helper()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestHelperProcess$")
	cmd.Env = append(append(os.Environ(), helperEnv+"=parent"), extraEnv...)
	Configure(cmd, 5*time.Second)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := Start(cmd); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatalf("read grandchild pid: %v (%q)", err, line)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatalf("grandchild pid %q: %v", line, err)
	}
	h, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		t.Fatalf("open grandchild %d: %v", pid, err)
	}
	t.Cleanup(func() {
		_ = windows.TerminateProcess(h, 1)
		_ = windows.CloseHandle(h)
	})
	return cmd, h
}

// exited waits up to d for the process behind h to exit.
func exited(h windows.Handle, d time.Duration) bool {
	ev, err := windows.WaitForSingleObject(h, uint32(d/time.Millisecond))
	return err == nil && ev == windows.WAIT_OBJECT_0
}

// TestProcgroupKillsWholeTreeWindows: cancelling a command started with
// Configure + Start kills its whole tree — the grandchild too, which a
// plain TerminateProcess of the direct child would leave running.
func TestProcgroupKillsWholeTreeWindows(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd, grandchild := startTree(t, ctx)

	cancel()
	_ = cmd.Wait()

	if !exited(grandchild, 5*time.Second) {
		t.Fatal("grandchild still running 5s after cancel+Wait: the job did not take the whole tree")
	}
}

// TestProcgroupLeaderExitKeepsDescendantsWindows: when the direct child
// exits on its own, its job is released without killing what it left
// running — the Unix behaviour, where a process group outlives its leader.
func TestProcgroupLeaderExitKeepsDescendantsWindows(t *testing.T) {
	cmd, grandchild := startTree(t, context.Background(), "ITERVOX_PROCGROUP_PARENT_EXITS=1")
	if err := cmd.Wait(); err != nil {
		t.Fatalf("parent: %v", err)
	}
	// The job is released by a watcher goroutine after the parent exits.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, ok := jobs.Load(cmd.Process.Pid); !ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("job still registered 5s after the parent exited")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if exited(grandchild, 500*time.Millisecond) {
		t.Fatal("grandchild died with its parent: releasing the job must clear kill-on-close")
	}
}

// TestConfigureCancelBeforeStartIsNoopWindows mirrors the Unix contract.
func TestConfigureCancelBeforeStartIsNoopWindows(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	Configure(cmd, 3*time.Second)
	if cmd.SysProcAttr == nil || cmd.SysProcAttr.CreationFlags&windows.CREATE_NEW_PROCESS_GROUP == 0 {
		t.Fatal("Configure must set CREATE_NEW_PROCESS_GROUP")
	}
	if cmd.WaitDelay != 3*time.Second {
		t.Fatalf("WaitDelay = %s, want 3s", cmd.WaitDelay)
	}
	if err := cmd.Cancel(); err != nil {
		t.Fatalf("Cancel before Start must be a no-op, got %v", err)
	}
}
