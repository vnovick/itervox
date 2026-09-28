//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly

package workflow_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/atomicfs"
	"github.com/vnovick/itervox/internal/workflow"
)

// CORE-149: WORKFLOW.md read-modify-write must be serialized ACROSS
// processes, not just within one. These tests re-exec the test binary as a
// helper process (the os/exec TestHelperProcess pattern) so the concurrent
// writer really is a second process with its own editMu — exactly the
// position `itervox init --update` and `itervox models refresh` are in
// relative to a running daemon.

const (
	helperEnvMode  = "ITERVOX_WORKFLOW_HELPER_MODE"
	helperEnvPath  = "ITERVOX_WORKFLOW_HELPER_PATH"
	helperEnvCount = "ITERVOX_WORKFLOW_HELPER_COUNT"
)

var counterLineRE = regexp.MustCompile(`(?m)^  counter: (\d+)$`)

const counterFixture = `---
tracker:
  kind: linear
agent:
  max_concurrent_agents: 3
  counter: 0
---
prompt body
`

// incrementCounter is a read-modify-write mutator with a deliberately wide
// window between the read (the file bytes ApplyAndWriteFrontMatter handed
// in) and the write, so an unserialized concurrent writer reliably loses
// updates.
func incrementCounter(front []string) ([]string, error) {
	for i, line := range front {
		m := counterLineRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		n, _ := strconv.Atoi(m[1])
		time.Sleep(2 * time.Millisecond)
		front[i] = "  counter: " + strconv.Itoa(n+1)
		return front, nil
	}
	return nil, fmt.Errorf("counter line not found")
}

// rawIncrementLocked mirrors the `itervox init --update` writers
// (migrateWorkflowToSchema2 / rewriteServerPort): a whole-file re-encode
// under workflow.WithEditLock rather than a front-matter Mutator.
func rawIncrementLocked(path string) error {
	return workflow.WithEditLock(path, func() error {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		m := counterLineRE.FindSubmatch(data)
		if m == nil {
			return fmt.Errorf("counter line not found")
		}
		n, _ := strconv.Atoi(string(m[1]))
		time.Sleep(2 * time.Millisecond)
		out := counterLineRE.ReplaceAll(data, []byte("  counter: "+strconv.Itoa(n+1)))
		return atomicfs.WriteFile(path, out, 0o644)
	})
}

// TestHelperProcessWorkflowWriter is not a test: it is the body of the
// helper process spawned by the cross-process tests. It does nothing unless
// the helper env var is set.
func TestHelperProcessWorkflowWriter(t *testing.T) {
	mode := os.Getenv(helperEnvMode)
	if mode == "" {
		t.Skip("helper process body; only runs when re-exec'd by a cross-process test")
	}
	path := os.Getenv(helperEnvPath)
	count, _ := strconv.Atoi(os.Getenv(helperEnvCount))
	for range count {
		var err error
		switch mode {
		case "raw-increment":
			err = rawIncrementLocked(path)
		case "patch-increment":
			err = workflow.ApplyAndWriteFrontMatter(path, incrementCounter)
		case "patch-int":
			err = workflow.PatchIntField(path, "max_concurrent_agents", 7)
		default:
			err = fmt.Errorf("unknown helper mode %q", mode)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "helper:", err)
			os.Exit(3)
		}
	}
	os.Exit(0)
}

func helperCmd(t *testing.T, mode, path string, count int) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcessWorkflowWriter$", "-test.count=1")
	cmd.Env = append(os.Environ(),
		helperEnvMode+"="+mode,
		helperEnvPath+"="+path,
		helperEnvCount+"="+strconv.Itoa(count),
	)
	return cmd
}

func readCounter(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	m := counterLineRE.FindSubmatch(data)
	require.NotNil(t, m, "counter line vanished:\n%s", data)
	n, err := strconv.Atoi(string(m[1]))
	require.NoError(t, err)
	return n
}

// TestCrossProcessWorkflowEditsDoNotLoseUpdates runs the daemon's locked
// patcher in this process concurrently with two helper processes — one
// using the same patcher (models refresh shape), one using WithEditLock
// around a raw re-encode (init --update shape). Every increment must land.
func TestCrossProcessWorkflowEditsDoNotLoseUpdates(t *testing.T) {
	const perWriter = 15
	path := filepath.Join(t.TempDir(), "WORKFLOW.md")
	require.NoError(t, os.WriteFile(path, []byte(counterFixture), 0o644))

	var stderrA, stderrB strings.Builder
	a := helperCmd(t, "patch-increment", path, perWriter)
	a.Stderr = &stderrA
	b := helperCmd(t, "raw-increment", path, perWriter)
	b.Stderr = &stderrB
	require.NoError(t, a.Start())
	require.NoError(t, b.Start())

	var wg sync.WaitGroup
	wg.Add(1)
	var localErr error
	go func() {
		defer wg.Done()
		for range perWriter {
			if err := workflow.ApplyAndWriteFrontMatter(path, incrementCounter); err != nil {
				localErr = err
				return
			}
		}
	}()
	wg.Wait()
	require.NoError(t, a.Wait(), "helper A: %s", stderrA.String())
	require.NoError(t, b.Wait(), "helper B: %s", stderrB.String())
	require.NoError(t, localErr)

	require.Equal(t, 3*perWriter, readCounter(t, path),
		"cross-process WORKFLOW.md edits lost updates — the edit lock is process-local")
}

// CORE-116 × CORE-149: a patcher write made by ANOTHER process lands in that
// process's self-write registry, not this one's, so this process's watcher
// must treat it as foreign and reload.
func TestWatchFiresOnCrossProcessPatcherWrite(t *testing.T) {
	path := newSelfWriteFixture(t)
	reloads := startCountingWatch(t, path)

	var stderr strings.Builder
	cmd := helperCmd(t, "patch-int", path, 1)
	cmd.Stderr = &stderr
	require.NoError(t, cmd.Run(), "helper: %s", stderr.String())

	require.Eventually(t, func() bool { return reloads.Load() == 1 },
		3*time.Second, 10*time.Millisecond,
		"a WORKFLOW.md write by another itervox process was suppressed as a self-write")
}
