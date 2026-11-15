//go:build linux

package sysmon

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/procgroup"
)

// The helper process: the test binary re-executed with SYSMON_HELPER set.
// "agent" prints "ready", waits for "go" on stdin, then opens a file for
// writing under SYSMON_WS, connects to SYSMON_ADDR (when set), starts a
// "child" helper, prints "done" and waits for stdin to close; "child" just
// waits. The go handshake lets a test track the group before the work.
func TestMain(m *testing.M) {
	switch os.Getenv("SYSMON_HELPER") {
	case "agent":
		in := bufio.NewReader(os.Stdin)
		fmt.Println("ready")
		if _, err := in.ReadString('\n'); err != nil {
			os.Exit(1)
		}
		f, err := os.Create(filepath.Join(os.Getenv("SYSMON_WS"), "out.txt"))
		if err != nil {
			os.Exit(2)
		}
		var conn net.Conn
		if addr := os.Getenv("SYSMON_ADDR"); addr != "" {
			if conn, err = net.Dial("tcp", addr); err != nil {
				os.Exit(3)
			}
		}
		child := exec.Command(os.Args[0], "-test.run=^$", "--child-arg", "--api-token=hide-me")
		child.Env = append(os.Environ(), "SYSMON_HELPER=child")
		childIn, err := child.StdinPipe()
		if err != nil || child.Start() != nil {
			os.Exit(4)
		}
		fmt.Println("done")
		_, _ = io.Copy(io.Discard, in)
		_ = childIn.Close()
		_ = child.Wait()
		_ = f.Close()
		if conn != nil {
			_ = conn.Close()
		}
		os.Exit(0)
	case "child":
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// startHelper starts the agent helper as its own process group (like an
// agent turn), reports it to m, then lets it work. Closing the returned
// writer ends it.
func startHelper(t *testing.T, m *Monitor, label, ws, addr string) (*exec.Cmd, io.WriteCloser) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), "SYSMON_HELPER=agent", "SYSMON_WS="+ws, "SYSMON_ADDR="+addr)
	procgroup.Configure(cmd, time.Second)
	stdin, err := cmd.StdinPipe()
	require.NoError(t, err)
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, procgroup.Start(cmd))
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	out := bufio.NewReader(stdout)
	line, err := out.ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, "ready\n", line)
	m.GroupStarted(cmd.Process.Pid, cmd.Process.Pid, label)
	_, err = io.WriteString(stdin, "go\n")
	require.NoError(t, err)
	line, err = out.ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, "done\n", line)
	return cmd, stdin
}

// localListener accepts TCP connections on loopback until the test ends,
// keeping them open.
func localListener(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	var conns []net.Conn
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			conns = append(conns, c)
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		<-done
		for _, c := range conns {
			_ = c.Close()
		}
	})
	return ln
}

// pollUntil ticks the sampler until cond holds or 10s pass.
func pollUntil(t *testing.T, m *Monitor, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		m.sampleRuns()
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("condition not reached before the deadline")
}

// TestMonitorSamplesARealProcessTree: a real two-process tree started as
// its own group is sampled (CPU, RSS, processes, child commands with
// redacted args), its sampled activity lists the started command, the
// file it writes in the workspace and its TCP connection, and EndRun
// returns the summary with the peaks.
func TestMonitorSamplesARealProcessTree(t *testing.T) {
	ln := localListener(t)
	ws := t.TempDir()
	m := New(Options{Resources: true, Mode: ModeSampled, Dir: t.TempDir()})
	m.AttachRun("ENG-1", "run-a", ws, false)
	cmd, stdin := startHelper(t, m, "ENG-1", ws, ln.Addr().String())

	pollUntil(t, m, func() bool {
		u, _ := m.Usage("ENG-1")
		r, _ := m.Activity("ENG-1", "")
		return u.Processes >= 2 && len(r.Connections) > 0 && slices.ContainsFunc(r.Files, func(f FileRecord) bool { return f.Write })
	})
	u, ok := m.Usage("ENG-1")
	require.True(t, ok)
	assert.True(t, u.Live)
	assert.Equal(t, "run-a", u.RunID)
	assert.GreaterOrEqual(t, u.Processes, 2, "the helper and its child")
	assert.Positive(t, u.RSSBytes)
	assert.True(t, u.DiskIOKnown)
	assert.Equal(t, 1, u.NetConnections)
	var childArgs string
	for _, c := range u.TopCommands {
		if strings.Contains(c.Args, "--child-arg") {
			childArgs = c.Args
		}
	}
	assert.Contains(t, childArgs, "--api-token=[redacted]", "the child command's credential flag is scrubbed")
	assert.NotContains(t, childArgs, "hide-me")

	r, err := m.Activity("ENG-1", "")
	require.NoError(t, err)
	assert.Equal(t, ModeSampled, r.Mode)
	assert.True(t, r.Live)
	assert.GreaterOrEqual(t, len(r.Commands), 2)
	written := slices.IndexFunc(r.Files, func(f FileRecord) bool { return f.Write && strings.HasSuffix(f.Path, "out.txt") })
	require.GreaterOrEqual(t, written, 0, "the file written in the workspace")
	assert.False(t, r.Files[written].OutsideWorkspace)
	assert.Equal(t, ln.Addr().String(), r.Connections[0].Remote)
	assert.Equal(t, "ESTABLISHED", r.Connections[0].State)

	sys := m.System()
	assert.Equal(t, 1, sys.Runs.Runs)
	assert.Equal(t, u.Processes, sys.Runs.Processes)

	// The run ends: the group exits and the loop closes it.
	require.NoError(t, stdin.Close())
	require.NoError(t, cmd.Wait())
	m.GroupExited(cmd.Process.Pid)
	sum, ok := m.EndRun("ENG-1")
	require.True(t, ok)
	assert.GreaterOrEqual(t, sum.PeakProcesses, 2)
	assert.Positive(t, sum.PeakRSSBytes)
	assert.GreaterOrEqual(t, sum.FilesWritten, 1)
	assert.Equal(t, 1, sum.Connections)
	assert.Equal(t, ModeSampled, sum.ActivityMode)

	after, ok := m.Usage("ENG-1")
	require.True(t, ok, "the finished run's usage is kept")
	assert.False(t, after.Live)
	assert.False(t, after.FinishedAt.IsZero())

	// The sampler saves the finished run; it is then readable after the
	// in-memory copy is gone.
	m.persist(false)
	m.mu.Lock()
	m.finished = nil
	m.mu.Unlock()
	stored, err := m.Activity("ENG-1", "run-a")
	require.NoError(t, err)
	assert.False(t, stored.Live)
	assert.NotEmpty(t, stored.Commands)
	assert.Equal(t, "run-a", stored.Runs[0].RunID)
}

// TestMonitorResourcesOffKeepsOnlyIdentity: agent.resource_sampling off
// keeps no figures even while sampled activity still reads the process
// tree; activity off records nothing and saves nothing.
func TestMonitorResourcesOffKeepsOnlyIdentity(t *testing.T) {
	m := New(Options{Resources: false, Mode: ModeSampled})
	m.AttachRun("ENG-2", "run-b", t.TempDir(), false)
	startHelper(t, m, "ENG-2", t.TempDir(), "")
	pollUntil(t, m, func() bool {
		r, _ := m.Activity("ENG-2", "")
		return len(r.Commands) >= 2
	})
	u, ok := m.Usage("ENG-2")
	require.True(t, ok)
	assert.Zero(t, u.RSSBytes, "the tree was read, but no figure is kept")
	assert.Zero(t, u.Processes)
	assert.Zero(t, u.Samples)
	assert.Empty(t, u.TopCommands)

	dir := t.TempDir()
	off := New(Options{Resources: true, Mode: ModeOff, Dir: dir})
	off.AttachRun("ENG-3", "run-c", t.TempDir(), false)
	cmd, stdin := startHelper(t, off, "ENG-3", t.TempDir(), "")
	off.sampleRuns()
	_ = stdin.Close()
	_ = cmd.Wait()
	off.GroupExited(cmd.Process.Pid)
	sum, ok := off.EndRun("ENG-3")
	require.True(t, ok)
	assert.Zero(t, sum.Commands)
	off.persist(true)
	_, err := os.Stat(filepath.Join(dir, "run-c.json"))
	assert.True(t, os.IsNotExist(err), "activity off saves nothing")
}

// TestConnectionsCountedWithoutActivity: the connection count is a
// resource figure, read in every activity mode (precise mode and off do not
// scan open files, but still count sockets); no file is recorded.
func TestConnectionsCountedWithoutActivity(t *testing.T) {
	ln := localListener(t)
	m := New(Options{Resources: true, Mode: ModeOff})
	m.AttachRun("ENG-4", "run-d", t.TempDir(), false)
	startHelper(t, m, "ENG-4", t.TempDir(), ln.Addr().String())
	pollUntil(t, m, func() bool {
		u, _ := m.Usage("ENG-4")
		return u.NetConnections == 1
	})
	r, err := m.Activity("ENG-4", "")
	require.NoError(t, err)
	assert.Empty(t, r.Files)
	assert.Empty(t, r.Commands)
}

// TestPreciseModeFallsBackWithReason: when the tracer cannot be opened the
// monitor runs sampled and says why.
func TestPreciseModeFallsBackWithReason(t *testing.T) {
	prev := openTracer
	t.Cleanup(func() { openTracer = prev })
	openTracer = func(func(preciseEvent)) (activityTracer, error) {
		return nil, errors.New("operation not permitted (CAP_BPF)")
	}
	m := New(Options{Mode: ModePrecise})
	mode, _ := m.Mode()
	assert.Equal(t, ModePrecise, mode, "asked for, not yet started")
	m.startPrecise()
	mode, reason := m.Mode()
	assert.Equal(t, ModeSampled, mode)
	assert.Contains(t, reason, "CAP_BPF")
	assert.Equal(t, ModeSampled, m.System().ActivityMode)
}

// TestPreciseLoaderFallsBackWithoutTracefs runs the REAL loader with no
// tracefs to find (as on a machine where it is not mounted, or without
// privileges): it fails with a reason and the monitor samples instead.
func TestPreciseLoaderFallsBackWithoutTracefs(t *testing.T) {
	prev := tracefsRoots
	t.Cleanup(func() { tracefsRoots = prev })
	tracefsRoots = []string{filepath.Join(t.TempDir(), "no-tracefs")}
	m := New(Options{Mode: ModePrecise})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); m.Run(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if mode, reason := m.Mode(); mode == ModeSampled {
			assert.Contains(t, reason, "tracefs is not mounted")
			break
		}
		require.True(t, time.Now().Before(deadline), "monitor never fell back")
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
}

// TestPreciseTracerRecordsExecOpenConnect needs CAP_BPF + CAP_PERFMON (or
// root) and tracefs; it skips, saying why, where those are missing (CI).
func TestPreciseTracerRecordsExecOpenConnect(t *testing.T) {
	probe, err := openPreciseTracer(func(preciseEvent) {})
	if err != nil {
		t.Skipf("precise mode unavailable here: %v", err)
	}
	require.NoError(t, probe.close())

	ln := localListener(t)
	ws := t.TempDir()
	m := New(Options{Mode: ModePrecise, Interval: 20 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); m.Run(ctx) }()
	defer func() { cancel(); <-done }()
	deadline := time.Now().Add(5 * time.Second)
	for mode, _ := m.Mode(); mode != ModePrecise; mode, _ = m.Mode() {
		require.True(t, time.Now().Before(deadline))
		time.Sleep(10 * time.Millisecond)
	}
	m.AttachRun("ENG-3", "run-c", ws, false)
	startHelper(t, m, "ENG-3", ws, ln.Addr().String())
	got := func() ActivityReport {
		r, _ := m.Activity("ENG-3", "")
		return r
	}
	deadline = time.Now().Add(5 * time.Second)
	for {
		r := got()
		wrote := slices.ContainsFunc(r.Files, func(f FileRecord) bool { return f.Write && strings.HasSuffix(f.Path, "out.txt") })
		connected := slices.ContainsFunc(r.Connections, func(c ConnRecord) bool { return c.Remote == ln.Addr().String() })
		execd := slices.ContainsFunc(r.Commands, func(c CommandRecord) bool { return strings.Contains(c.Args, "--child-arg") })
		if wrote && connected && execd {
			break
		}
		require.True(t, time.Now().Before(deadline), "precise events missing: %+v", r)
		time.Sleep(20 * time.Millisecond)
	}
	r := got()
	assert.Equal(t, ModePrecise, r.Mode)
	assert.True(t, r.Supports.Files)
	i := slices.IndexFunc(r.Commands, func(c CommandRecord) bool { return strings.Contains(c.Args, "--child-arg") })
	assert.Contains(t, r.Commands[i].Args, "--api-token=[redacted]")
}
