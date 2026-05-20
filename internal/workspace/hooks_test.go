package workspace_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/workspace"
)

func TestRunHookSuccess(t *testing.T) {
	dir := t.TempDir()
	err := workspace.RunHook(context.Background(), "echo hello", dir, 5000)
	assert.NoError(t, err)
}

func TestRunHookEmptyScriptIsNoOp(t *testing.T) {
	dir := t.TempDir()
	err := workspace.RunHook(context.Background(), "", dir, 5000)
	assert.NoError(t, err)
}

func TestRunHookNonZeroExitFails(t *testing.T) {
	dir := t.TempDir()
	err := workspace.RunHook(context.Background(), "exit 1", dir, 5000)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "hook_failed")
}

func TestRunHookTimeoutKillsProcess(t *testing.T) {
	dir := t.TempDir()
	start := time.Now()
	err := workspace.RunHook(context.Background(), "sleep 60", dir, 200)
	elapsed := time.Since(start)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "hook_timeout")
	assert.Less(t, elapsed, 5*time.Second, "hook should be killed well before 5s")
}

func TestRunHookCancellationKillsBackgroundChild(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- workspace.RunHook(ctx, "sleep 60 & echo $! > child.pid; wait", dir, 60000)
	}()

	var data []byte
	require.Eventually(t, func() bool {
		var readErr error
		data, readErr = os.ReadFile(filepath.Join(dir, "child.pid"))
		return readErr == nil && strings.TrimSpace(string(data)) != ""
	}, 3*time.Second, 20*time.Millisecond, "background hook child pid should be written before cancellation")

	cancel()

	var err error
	select {
	case err = <-errCh:
	case <-time.After(3 * time.Second):
		t.Fatal("hook did not return after context cancellation")
	}
	require.Error(t, err)

	pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
	require.NoError(t, parseErr)

	require.Eventually(t, func() bool {
		return !processExists(pid)
	}, 3*time.Second, 50*time.Millisecond, "background hook child should be killed with the hook process group")
}

func TestRunHookCapsCapturedOutput(t *testing.T) {
	dir := t.TempDir()
	var logged []string
	err := workspace.RunHook(context.Background(), "yes 0123456789 | head -c 300000; exit 1", dir, 5000, func(line string) {
		logged = append(logged, line)
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "hook output truncated")
	assert.Less(t, len(err.Error()), 270000, "hook error output should be capped")
	assert.NotEmpty(t, logged, "truncated output should still be forwarded to the hook log")
}

func TestRunHookNonPositiveTimeoutFallsBackTo60s(t *testing.T) {
	dir := t.TempDir()
	// A quick command with timeout=0 should succeed (falls back to 60000ms).
	err := workspace.RunHook(context.Background(), "echo ok", dir, 0)
	assert.NoError(t, err)
}

func TestRunHookWritesFileInWorkspaceDir(t *testing.T) {
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "hook-ran")
	script := "touch hook-ran"
	err := workspace.RunHook(context.Background(), script, dir, 5000)
	require.NoError(t, err)
	_, statErr := os.Stat(sentinel)
	assert.NoError(t, statErr, "hook should have created file in workspace dir")
}

func processExists(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
