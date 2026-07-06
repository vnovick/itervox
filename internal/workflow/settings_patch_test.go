package workflow

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const minimalWorkflow = `---
tracker:
  kind: linear
agent:
  max_concurrent_agents: 3
---
prompt body
`

func writeTempWorkflow(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "WORKFLOW.md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return path
}

// TestApplyAndWriteFrontMatter_RunsMutatorsInOrderAndWritesOnce verifies that
// ApplyAndWriteFrontMatter threads the output of one mutator into the next
// and writes the file exactly once with the final composed result.
func TestApplyAndWriteFrontMatter_RunsMutatorsInOrderAndWritesOnce(t *testing.T) {
	path := writeTempWorkflow(t, minimalWorkflow)

	addLine := func(s string) Mutator {
		return func(front []string) ([]string, error) {
			return append(front, s), nil
		}
	}
	if err := ApplyAndWriteFrontMatter(path, addLine("first: 1"), addLine("second: 2")); err != nil {
		t.Fatalf("apply: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	body := string(got)
	if !strings.Contains(body, "first: 1") || !strings.Contains(body, "second: 2") {
		t.Fatalf("expected both mutators applied, got %q", body)
	}
	// And only one closing --- — sanity check the reassembly didn't double-write.
	if strings.Count(body, "\n---\n") != 1 {
		t.Fatalf("expected exactly one front-matter terminator, got %d in %q",
			strings.Count(body, "\n---\n"), body)
	}
}

// TestApplyAndWriteFrontMatter_ErrorLeavesFileUntouched verifies that a
// mutator returning an error does NOT write a partial result. This is the
// transactional contract callers depend on for cascade safety.
func TestApplyAndWriteFrontMatter_ErrorLeavesFileUntouched(t *testing.T) {
	path := writeTempWorkflow(t, minimalWorkflow)
	originalBytes, _ := os.ReadFile(path)

	failing := func(front []string) ([]string, error) {
		return nil, fmt.Errorf("intentional failure")
	}
	err := ApplyAndWriteFrontMatter(path,
		// First mutator succeeds and would have written if we wrote per-mutator.
		func(f []string) ([]string, error) { return append(f, "added: yes"), nil },
		failing,
	)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	gotBytes, _ := os.ReadFile(path)
	if string(gotBytes) != string(originalBytes) {
		t.Fatalf("file was modified despite mutator failure:\nbefore: %q\nafter:  %q",
			originalBytes, gotBytes)
	}
}

// TestApplyAndWriteFrontMatter_SerializesConcurrentEdits pins the editMu
// contract: two goroutines editing the same path must run sequentially, so
// neither write loses the other's mutations.
func TestApplyAndWriteFrontMatter_SerializesConcurrentEdits(t *testing.T) {
	path := writeTempWorkflow(t, minimalWorkflow)

	// A "slow" mutator that holds the editMu for ~50ms so the second
	// goroutine has to wait. If editMu is missing, the second goroutine
	// would read the same starting bytes and overwrite the first's change.
	var inFlight atomic.Int32
	var observedConcurrent atomic.Bool
	slowMutator := func(label string) Mutator {
		return func(front []string) ([]string, error) {
			n := inFlight.Add(1)
			if n > 1 {
				observedConcurrent.Store(true)
			}
			defer inFlight.Add(-1)
			time.Sleep(50 * time.Millisecond)
			return append(front, "added_by: "+label), nil
		}
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_ = ApplyAndWriteFrontMatter(path, slowMutator("first"))
	}()
	go func() {
		defer wg.Done()
		_ = ApplyAndWriteFrontMatter(path, slowMutator("second"))
	}()
	wg.Wait()

	if observedConcurrent.Load() {
		t.Fatalf("editMu did not serialize: two mutators ran concurrently")
	}
	// Both labels should be present in the final file (the second to acquire
	// the lock reads the first's output and adds to it).
	got, _ := os.ReadFile(path)
	body := string(got)
	if !strings.Contains(body, "added_by: first") || !strings.Contains(body, "added_by: second") {
		t.Fatalf("expected both labels in final file, got %q", body)
	}
}
