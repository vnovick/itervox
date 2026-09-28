package main

import (
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/server"
)

// CORE-007 — debug.SetCrashOutput to <logsDir>/crash.log, reported once in
// HEARTBEAT.md on the next boot (timestamp + path only, never panic text).

const (
	crashChildEnv      = "ITERVOX_CRASH_TEST_CHILD_DIR"
	crashChildModeEnv  = "ITERVOX_CRASH_TEST_CHILD_MODE"
	crashFixturePanic  = "crash fixture: do-not-copy-this-panic-text"
	failFastFixtureMsg = "failfast fixture panic"
)

// TestMain-free child hook: when re-executed with crashChildEnv set, the test
// binary arms crash output (or failFastOnPanic) and panics on a NON-main
// goroutine — the case main()'s deferred recover cannot see.
func init() {
	dir := os.Getenv(crashChildEnv)
	if dir == "" {
		return
	}
	switch os.Getenv(crashChildModeEnv) {
	case "failfast":
		setupFailFastChildLogging(dir)
		done := make(chan struct{})
		go func() {
			defer close(done)
			defer failFastOnPanic("failfast-fixture")
			panic(failFastFixtureMsg)
		}()
		<-done
	default:
		if _, err := armCrashOutput(dir); err != nil {
			_, _ = os.Stderr.WriteString("arm failed: " + err.Error() + "\n")
			os.Exit(3)
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			panic(crashFixturePanic)
		}()
		<-done
	}
	os.Exit(0) // unreachable when the panic crashes the process
}

func runCrashChild(t *testing.T, dir, mode string) (string, error) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), crashChildEnv+"="+dir, crashChildModeEnv+"="+mode)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestCrashOutputCapturesPanic(t *testing.T) {
	dir := t.TempDir()
	out, err := runCrashChild(t, dir, "crash")
	var exitErr *exec.ExitError
	require.True(t, errors.As(err, &exitErr), "child must crash, got err=%v out=%s", err, out)
	assert.NotEqual(t, 0, exitErr.ExitCode())

	crashPath := filepath.Join(dir, crashLogName)
	data, readErr := os.ReadFile(crashPath)
	require.NoError(t, readErr, "crash.log must exist after an unrecovered goroutine panic")
	assert.Contains(t, string(data), "panic: "+crashFixturePanic)
	assert.Contains(t, string(data), "goroutine ", "the runtime's full crash dump, including the stack")

	info, statErr := os.Stat(crashPath)
	require.NoError(t, statErr)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "crash.log is written 0600")
}

func TestHeartbeatReportsLastCrash(t *testing.T) {
	dir := t.TempDir()
	crashPath := filepath.Join(dir, crashLogName)

	// Boot 1: no crash.log yet — nothing to report; the boot records its marker.
	report, f, err := prepareCrashLog(dir)
	require.NoError(t, err)
	require.NoError(t, f.Close())
	assert.True(t, report.At.IsZero(), "first boot has no prior crash")

	// The runtime appends a crash dump while boot 1 is running.
	crashedAt := time.Date(2026, 9, 25, 12, 34, 56, 0, time.UTC)
	dump := "panic: " + crashFixturePanic + "\n\ngoroutine 7 [running]:\nmain.main()\n"
	appendFile(t, crashPath, dump)
	require.NoError(t, os.Chtimes(crashPath, crashedAt, crashedAt))

	// Boot 2: the growth since boot 1's marker is reported.
	report, f, err = prepareCrashLog(dir)
	require.NoError(t, err)
	require.NoError(t, f.Close())
	require.False(t, report.At.IsZero(), "boot after a crash must report it")
	assert.Equal(t, crashedAt, report.At.UTC())
	assert.Equal(t, crashPath, report.Path)

	content := renderHeartbeat(server.StateSnapshot{}, heartbeatOptions{LastCrash: report}, crashedAt.Add(time.Minute))
	var line string
	for l := range strings.SplitSeq(content, "\n") {
		if strings.HasPrefix(l, "- Last crash:") {
			line = l
		}
	}
	require.NotEmpty(t, line, "HEARTBEAT.md must carry a '- Last crash:' line; got:\n%s", content)
	assert.Contains(t, line, "2026-09-25T12:34:56Z", "RFC3339 crash timestamp")
	assert.Contains(t, line, crashPath)
	assert.NotContains(t, content, crashFixturePanic, "panic text must never reach HEARTBEAT.md")
	assert.NotContains(t, content, "goroutine 7")

	// Boot 3: the same crash is not re-reported.
	report, f, err = prepareCrashLog(dir)
	require.NoError(t, err)
	require.NoError(t, f.Close())
	assert.True(t, report.At.IsZero(), "a crash is reported once, on the first boot after it")
	assert.NotContains(t, renderHeartbeat(server.StateSnapshot{}, heartbeatOptions{LastCrash: report}, crashedAt), "Last crash")
}

func TestPrepareCrashLogRotatesOversizedLog(t *testing.T) {
	dir := t.TempDir()
	crashPath := filepath.Join(dir, crashLogName)
	appendFile(t, crashPath, strings.Repeat("x", crashLogRotateBytes+1))

	report, f, err := prepareCrashLog(dir)
	require.NoError(t, err)
	require.NoError(t, f.Close())
	assert.False(t, report.At.IsZero(), "an unseen oversized crash.log is still reported before rotation")

	rotated, err := os.Stat(crashPath + ".1")
	require.NoError(t, err, "oversized crash.log rotates to crash.log.1")
	assert.Equal(t, int64(crashLogRotateBytes+1), rotated.Size())
	fresh, err := os.Stat(crashPath)
	require.NoError(t, err)
	assert.Equal(t, int64(0), fresh.Size())
	assert.Equal(t, os.FileMode(0o600), fresh.Mode().Perm())
}

func TestFailFastOnPanicLogsThenCrashes(t *testing.T) {
	dir := t.TempDir()
	out, err := runCrashChild(t, dir, "failfast")
	var exitErr *exec.ExitError
	require.True(t, errors.As(err, &exitErr), "failFastOnPanic must re-panic, got err=%v out=%s", err, out)
	logged, readErr := os.ReadFile(filepath.Join(dir, "failfast.log"))
	require.NoError(t, readErr)
	assert.Contains(t, string(logged), "goroutine panic: failing fast")
	assert.Contains(t, string(logged), "failfast-fixture")
	assert.Contains(t, string(logged), failFastFixtureMsg)
	assert.Contains(t, out, "panic: "+failFastFixtureMsg, "the process still crashes with the original panic")
}

func appendFile(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	require.NoError(t, err)
	_, err = f.WriteString(s)
	require.NoError(t, err)
	require.NoError(t, f.Close())
}

// setupFailFastChildLogging points the child's default slog logger at
// <dir>/failfast.log so the parent can assert what failFastOnPanic logged
// before re-panicking.
func setupFailFastChildLogging(dir string) {
	f, err := os.OpenFile(filepath.Join(dir, "failfast.log"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		os.Exit(4)
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(f, nil)))
}

// TestPrepareCrashLogReportsRotatedDumpPath (G8, CORE-007): when the crash
// that is being reported also pushes crash.log over the rotation threshold,
// the dump is renamed to crash.log.1 before a fresh crash.log is opened. The
// reported path — the one HEARTBEAT.md tells the operator to open — must
// name the file that actually holds the dump, not the fresh empty one.
func TestPrepareCrashLogReportsRotatedDumpPath(t *testing.T) {
	dir := t.TempDir()
	crashPath := filepath.Join(dir, crashLogName)

	report, f, err := prepareCrashLog(dir) // boot 1 records its marker
	require.NoError(t, err)
	require.NoError(t, f.Close())
	require.True(t, report.At.IsZero())

	dump := "panic: " + crashFixturePanic + "\n\ngoroutine 7 [running]:\n"
	appendFile(t, crashPath, dump+strings.Repeat("x", crashLogRotateBytes))

	report, f, err = prepareCrashLog(dir) // boot 2 reports, then rotates
	require.NoError(t, err)
	require.NoError(t, f.Close())
	require.False(t, report.At.IsZero(), "the crash must still be reported")

	held, err := os.ReadFile(report.Path)
	require.NoError(t, err, "reported crash path must exist")
	assert.Contains(t, string(held), "panic: "+crashFixturePanic,
		"reported path %s must hold the dump", report.Path)
	assert.Equal(t, crashPath+".1", report.Path)

	content := renderHeartbeat(server.StateSnapshot{}, heartbeatOptions{LastCrash: report}, time.Now())
	assert.Contains(t, content, crashPath+".1", "HEARTBEAT.md must point at the rotated dump")
}
