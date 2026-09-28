package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/atomicfs"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

// countingWriter records every ledger write per path and forwards it to the
// real atomic writer so the on-disk content stays observable.
type countingWriter struct {
	mu     sync.Mutex
	counts map[string]int
}

func newCountingWriter() *countingWriter { return &countingWriter{counts: map[string]int{}} }

func (w *countingWriter) write(path string, data []byte, perm fs.FileMode) error {
	w.mu.Lock()
	w.counts[filepath.Base(path)]++
	w.mu.Unlock()
	return atomicfs.WriteFile(path, data, perm)
}

func (w *countingWriter) total() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := 0
	for _, c := range w.counts {
		n += c
	}
	return n
}

func (w *countingWriter) snapshot() map[string]int {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make(map[string]int, len(w.counts))
	for k, v := range w.counts {
		out[k] = v
	}
	return out
}

// CORE-038: storeSnap used to rewrite (temp + fsync + rename) the paused,
// input-required and automation-queue ledgers after EVERY event, including
// each EventWorkerUpdate token batch, whether or not any ledger changed.
// N worker updates must now cost zero ledger writes, and a real ledger change
// must still cost exactly one write of that ledger.
func TestStoreSnapSkipsUnchangedLedgers(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{}
	o := New(cfg, nil, nil, nil)
	cw := newCountingWriter()
	o.persistWriteFile = cw.write
	o.SetPausedFile(filepath.Join(dir, "paused.json"))
	o.SetInputRequiredFile(filepath.Join(dir, "input_required.json"))
	o.SetAutomationQueueFile(filepath.Join(dir, "automation_queue.json"))

	state := NewState(cfg)
	state.Running["u1"] = &RunEntry{Issue: domain.Issue{ID: "u1", Identifier: "ENG-1", State: "In Progress"}}
	o.storeSnap(state) // first persist of each ledger is allowed
	o.flushPersistence()
	baseline := cw.total()

	const updates = 20
	ctx := context.Background()
	for i := 1; i <= updates; i++ {
		state = o.handleEvent(ctx, state, OrchestratorEvent{
			Type:     EventWorkerUpdate,
			IssueID:  "u1",
			RunEntry: &RunEntry{TurnCount: 1, TotalTokens: i * 100, InputTokens: i * 60, OutputTokens: i * 40, LastMessage: "tick"},
		})
		o.storeSnap(state)
	}
	o.flushPersistence()
	require.Equal(t, updates*100, state.Running["u1"].TotalTokens, "the updates must have been applied")
	assert.Equal(t, 0, cw.total()-baseline,
		"%d EventWorkerUpdate events must produce zero ledger writes; per-file writes: %v", updates, cw.snapshot())

	// A real ledger change still lands, exactly once.
	before := cw.snapshot()["paused.json"]
	state.PausedIdentifiers["ENG-9"] = "u9"
	o.storeSnap(state)
	o.storeSnap(state)
	o.flushPersistence()
	assert.Equal(t, before+1, cw.snapshot()["paused.json"], "one change -> one paused.json write")
	data, err := os.ReadFile(filepath.Join(dir, "paused.json"))
	require.NoError(t, err)
	assert.JSONEq(t, `{"ENG-9":"u9"}`, string(data))
}

// CORE-151: ClearHistory ran on an HTTP goroutine while addCompletedRun (event
// loop) could already hold a pre-clear copy of the history on its way to disk.
// The clear removed the file, the stale write then recreated it, and the
// "cleared" runs came back on the next restart. The fake writer parks the
// in-flight history write so the interleaving is deterministic.
func TestClearHistoryNotUndoneByInFlightHistoryWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.json")
	o := New(&config.Config{}, nil, nil, nil)
	o.SetHistoryFile(path)

	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	o.persistWriteFile = func(p string, data []byte, perm fs.FileMode) error {
		if p == path {
			blocked := false
			once.Do(func() { blocked = true })
			if blocked {
				close(entered)
				<-release
			}
		}
		return atomicfs.WriteFile(p, data, perm)
	}

	addDone := make(chan struct{})
	go func() { // stands in for the event loop
		defer close(addDone)
		o.addCompletedRun(CompletedRun{Identifier: "ENG-1", Status: "succeeded"})
	}()
	<-entered // the pre-clear history is now mid-write

	o.ClearHistory()
	close(release)
	<-addDone
	o.flushPersistence()

	assert.Empty(t, o.RunHistory(), "in-memory history is cleared")
	_, err := os.Stat(path)
	assert.True(t, os.IsNotExist(err), "a clear must not be undone by a write that started before it (stat err=%v)", err)

	restarted := New(&config.Config{}, nil, nil, nil)
	restarted.SetHistoryFile(path)
	restarted.loadHistoryFromDisk()
	assert.Empty(t, restarted.RunHistory(), "cleared history must not reappear on restart")
}

// CORE-038 regression guard for the auto-switch reordering bec7456 removed:
// the older version (seq=1) is parked inside the writer until the newer one
// (seq=2) has been submitted; after release the disk must hold seq=2 and the
// seq=1 write must never land after it.
func TestAutoSwitchPersistOrdered(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auto_switched.json")
	o := New(&config.Config{}, nil, nil, nil)
	o.SetAutoSwitchedFile(path)

	entered := make(chan struct{})
	release := make(chan struct{})
	var mu sync.Mutex
	var landed []string // profile of each version, in landing order
	var once sync.Once
	o.persistWriteFile = func(p string, data []byte, perm fs.FileMode) error {
		first := false
		once.Do(func() { first = true })
		if first {
			close(entered)
			<-release
		}
		if err := atomicfs.WriteFile(p, data, perm); err != nil {
			return err
		}
		mu.Lock()
		landed = append(landed, string(data))
		mu.Unlock()
		return nil
	}

	o.startPersistence()
	ids := map[string]struct{}{"ENG-1": {}}
	o.saveAutoSwitchedToDisk(&State{AutoSwitchedIdentifiers: ids, IssueProfiles: map[string]string{"ENG-1": "older"}}) // seq=1
	<-entered                                                                                                          // seq=1 is mid-write
	o.saveAutoSwitchedToDisk(&State{AutoSwitchedIdentifiers: ids, IssueProfiles: map[string]string{"ENG-1": "newer"}}) // seq=2 submitted
	close(release)
	o.stopPersistence()

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.JSONEq(t, `{"version":2,"overrides":{"ENG-1":{"profile":"newer"}},"switch_history":{},"cooldowns":{},"cap_comment_until":{}}`, string(data), "disk must hold the seq=2 version")
	mu.Lock()
	defer mu.Unlock()
	require.NotEmpty(t, landed)
	assert.Contains(t, landed[len(landed)-1], `"newer"`, "the last write to land must be seq=2; landing order: %v", landed)
	w := o.ledger(ledgerAutoSwitched)
	w.mu.Lock()
	assert.Equal(t, uint64(2), w.writtenSeq)
	w.mu.Unlock()
}

// CORE-038: a queue mutation made immediately before Run returns must be on
// disk when Run returns. The first queue write is parked so the mutation's
// version is still pending (not yet written by the ledger worker) at cancel
// time; only Run's shutdown flush can land it.
func TestPersistFlushesOnShutdown(t *testing.T) {
	cfg := &config.Config{}
	cfg.Tracker.ActiveStates = []string{"Todo", "In Progress"}
	cfg.Tracker.TerminalStates = []string{"Done"}
	cfg.Agent.MaxConcurrentAgents = 0 // every automation queues with no_slots
	cfg.Agent.Profiles = map[string]config.AgentProfile{"pm": {Command: "claude"}}
	cfg.Polling.IntervalMs = 60_000
	mt := tracker.NewMemoryTracker(nil, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	o := New(cfg, mt, nil, nil)
	path := filepath.Join(t.TempDir(), "automation_queue.json")
	o.SetAutomationQueueFile(path)

	firstQueueWrite := make(chan struct{})
	releaseFirst := make(chan struct{})
	var once sync.Once
	o.persistWriteFile = func(p string, data []byte, perm fs.FileMode) error {
		if p == path {
			first := false
			once.Do(func() { first = true })
			if first {
				close(firstQueueWrite)
				<-releaseFirst
			}
		}
		return atomicfs.WriteFile(p, data, perm)
	}

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- o.Run(ctx) }()
	<-firstQueueWrite // the initial (empty) queue version is parked in the writer

	issue := domain.Issue{ID: "u1", Identifier: "ENG-1", Title: "T", State: "Todo"}
	require.True(t, o.DispatchAutomation(ctx, issue, AutomationDispatch{
		AutomationID: "cron-1",
		ProfileName:  "pm",
		Trigger:      AutomationTriggerContext{Type: config.AutomationTriggerCron, AutomationID: "cron-1"},
	}))
	require.Eventually(t, func() bool { return len(o.Snapshot().AutomationQueue) == 1 },
		5*time.Second, 5*time.Millisecond, "the event loop must enqueue the automation")

	cancel()
	close(releaseFirst)
	require.ErrorIs(t, <-runDone, context.Canceled)

	reader := New(cfg, nil, nil, nil)
	reader.SetAutomationQueueFile(path)
	loaded := reader.loadAutomationQueueFromDisk(NewState(cfg))
	assert.Len(t, loaded.AutomationQueue, 1, "the queue mutation made just before shutdown must be on disk when Run returns")
}

// CORE-038: write errors are counted and surfaced in the snapshot.
func TestPersistWriteErrorCounted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "paused.json")
	o := New(&config.Config{}, nil, nil, nil)
	o.SetPausedFile(path)
	o.persistWriteFile = func(string, []byte, fs.FileMode) error { return errors.New("disk full") }

	state := NewState(&config.Config{})
	state.PausedIdentifiers["ENG-1"] = "u1"
	o.storeSnap(state)

	assert.GreaterOrEqual(t, o.PersistWriteErrors(), int64(1))
	assert.Equal(t, o.PersistWriteErrors(), o.Snapshot().PersistWriteErrors, "the counter is surfaced in Snapshot()")
	_, err := os.Stat(path)
	assert.True(t, os.IsNotExist(err))
}

// CORE-038: a write that fails once, with no later state change to trigger
// another submission, is retried by the ledger worker's timer and lands.
func TestPersistRetriesFailedWriteWithoutNewEvent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "paused.json")
	o := New(&config.Config{}, nil, nil, nil)
	o.SetPausedFile(path)
	o.persistRetryInterval = 10 * time.Millisecond
	var calls atomic.Int32
	o.persistWriteFile = func(p string, data []byte, perm fs.FileMode) error {
		if calls.Add(1) == 1 {
			return errors.New("transient EIO")
		}
		return atomicfs.WriteFile(p, data, perm)
	}
	o.startPersistence()
	defer o.stopPersistence()

	state := NewState(&config.Config{})
	state.PausedIdentifiers["ENG-1"] = "u1"
	o.storeSnap(state) // the only submission; its first write fails

	require.Eventually(t, func() bool {
		data, err := os.ReadFile(path)
		return err == nil && string(data) == `{"ENG-1":"u1"}`
	}, 5*time.Second, 5*time.Millisecond, "the timer pass must retry the failed write without a new event")
	assert.Equal(t, int64(1), o.PersistWriteErrors())
	assert.GreaterOrEqual(t, calls.Load(), int32(2))
}

// CORE-038: while the ledger worker is live, a stream of submissions racing
// synchronous flushes always ends with the newest version on disk, and the
// versions that do land do so in strictly increasing order.
func TestLedgerWriterLastVersionLandsInOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.json")
	var mu sync.Mutex
	var landed []int
	w := newLedgerWriter("test", func(p string, data []byte, perm fs.FileMode) error {
		var v int
		if err := json.Unmarshal(data, &v); err != nil {
			return err
		}
		if err := atomicfs.WriteFile(p, data, perm); err != nil {
			return err
		}
		mu.Lock()
		landed = append(landed, v)
		mu.Unlock()
		return nil
	}, nil)
	w.start(time.Hour)

	const n = 500
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // a concurrent flusher (e.g. ClearHistory / tests)
		defer wg.Done()
		for i := 0; i < 50; i++ {
			_ = w.flush()
		}
	}()
	for i := 1; i <= n; i++ {
		data, _ := json.Marshal(i)
		if w.submit(path, data, 0o644) {
			w.settle()
		}
	}
	wg.Wait()
	require.NoError(t, w.stopAndFlush())

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "500", string(data))
	mu.Lock()
	defer mu.Unlock()
	for i := 1; i < len(landed); i++ {
		require.Greater(t, landed[i], landed[i-1], "versions must land in submission order: %v", landed)
	}
}

// BH6: outside Run (inline mode) no worker timer retries a failed write, so
// the only retry trigger is the next submission. A byte-identical
// resubmission used to be dropped by the dirty check, stranding the failed
// version pending forever although the caller asked for it to land again.
func TestPersistInlineFailedWriteRetriedOnIdenticalResubmit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "paused.json")
	o := New(&config.Config{}, nil, nil, nil)
	o.SetPausedFile(path)
	var calls atomic.Int32
	o.persistWriteFile = func(p string, data []byte, perm fs.FileMode) error {
		if calls.Add(1) == 1 {
			return errors.New("transient EIO")
		}
		return atomicfs.WriteFile(p, data, perm)
	}

	state := NewState(&config.Config{})
	state.PausedIdentifiers["ENG-1"] = "u1"
	o.storeSnap(state) // inline drain; the write fails
	_, err := os.Stat(path)
	require.True(t, os.IsNotExist(err), "precondition: the first write failed")

	o.storeSnap(state) // identical bytes: must retry, not be deduplicated away
	data, err := os.ReadFile(path)
	require.NoError(t, err, "an identical resubmission after a failed inline write must retry it")
	assert.Equal(t, `{"ENG-1":"u1"}`, string(data))
	assert.EqualValues(t, 2, calls.Load())

	o.storeSnap(state) // clean now: the dirty check applies again
	assert.EqualValues(t, 2, calls.Load(), "once written, an identical submission is free again")
}
