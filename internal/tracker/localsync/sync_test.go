package localsync

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/outbox"
	"github.com/vnovick/itervox/internal/tracker"
	"github.com/vnovick/itervox/internal/tracker/local"
)

// env is one sync under test: a local issues directory, a fake remote in
// one dialect, and a Syncer between them with batching off.
type env struct {
	t        *testing.T
	kind     string
	dir      string
	ledger   string
	local    *local.Tracker
	remote   *fakeRemote
	tr       tracker.Tracker
	ob       *outbox.Outbox
	s        *Syncer
	changes  atomic.Int32
	timeSkew time.Duration
}

func newEnv(t *testing.T, kind string) *env {
	t.Helper()
	root := t.TempDir()
	e := &env{t: t, kind: kind, dir: filepath.Join(root, "issues"), ledger: filepath.Join(root, "sync_ledger.json")}
	e.local = local.New(local.Config{Dir: e.dir, Prefix: "ITX", ActiveStates: []string{"Todo", "In Progress"}, TerminalStates: []string{"Done"}})
	e.remote = newFakeRemote(kind, e.now)
	e.tr = e.remote
	if kind == "linear" {
		e.tr = fakeLinear{e.remote}
	}
	ob, err := outbox.New("")
	require.NoError(t, err)
	ob.SetPushInterval(0)
	e.ob = ob
	e.s = e.newSyncer()
	return e
}

func (e *env) newSyncer() *Syncer {
	s, err := New(Config{Kind: e.kind, Local: e.local, Remote: e.tr, Outbox: e.ob, LedgerPath: e.ledger,
		StateMap:    map[string]string{"Review": "In Review"},
		PullStates:  []string{"Backlog", "Todo", "In Progress", "In Review"},
		StateLabels: e.remote.stateLabels, Now: e.now,
		OnLocalChange: func() { e.changes.Add(1) }})
	require.NoError(e.t, err)
	return s
}

// now is the wall clock (file modification times are wall-clock times)
// plus a skew tests move forward.
func (e *env) now() time.Time { return time.Now().Add(e.timeSkew) }

// sync runs one cycle and pushes what it queued.
func (e *env) sync() {
	e.t.Helper()
	ctx := context.Background()
	require.NoError(e.t, e.s.Cycle(ctx))
	e.s.Push(ctx)
	require.Empty(e.t, e.ob.Snapshot(), "every queued remote write was delivered")
}

func (e *env) path(id string) string { return filepath.Join(e.dir, id+".md") }

// write writes an issue file by hand, at modification time at (zero: now).
func (e *env) write(id, content string, at time.Time) {
	e.t.Helper()
	require.NoError(e.t, os.MkdirAll(e.dir, 0o755))
	require.NoError(e.t, os.WriteFile(e.path(id), []byte(content), 0o644))
	if at.IsZero() {
		at = e.now()
	}
	require.NoError(e.t, os.Chtimes(e.path(id), at, at))
}

func (e *env) read(id string) string {
	e.t.Helper()
	b, err := os.ReadFile(e.path(id))
	require.NoError(e.t, err)
	return string(b)
}

// edit rewrites an issue file by hand with old replaced by new.
func (e *env) edit(id, old, new string, at time.Time) {
	e.t.Helper()
	content := e.read(id)
	require.Contains(e.t, content, old)
	e.write(id, strings.Replace(content, old, new, 1), at)
}

func (e *env) issue(id string) local.SyncIssue {
	e.t.Helper()
	all, err := e.local.SyncIssues()
	require.NoError(e.t, err)
	for _, si := range all {
		if si.Identifier == id {
			return si
		}
	}
	e.t.Fatalf("no local issue %s", id)
	return local.SyncIssue{}
}

// linked creates ITX-1 locally and syncs it to the remote; it returns the
// remote issue's id.
func (e *env) linked() string {
	e.t.Helper()
	e.write("ITX-1", "---\ntitle: Add a --json flag\nstate: Todo\n---\n\nPrint JSON.\n", e.now().Add(-time.Hour))
	e.sync()
	return e.remote.only().id
}

func forEachKind(t *testing.T, f func(t *testing.T, e *env)) {
	for _, kind := range []string{"linear", "github"} {
		t.Run(kind, func(t *testing.T) { f(t, newEnv(t, kind)) })
	}
}

// TestLocalSyncCreatesRemoteIssueAndRecordsRemoteID (#118): a new local
// issue is created on the remote, with its fields, and the file gets the
// remote identifier in its front matter, keeping its own layout and YAML
// comments. Its comments follow on the next cycle.
func TestLocalSyncCreatesRemoteIssueAndRecordsRemoteID(t *testing.T) {
	forEachKind(t, func(t *testing.T, e *env) {
		e.write("ITX-1", "---\n# Report work.\ntitle: Add a --json flag\nstate: Todo  # not started\npriority: 2\nlabels: [cli]\n---\n\n"+
			"Print the report as JSON.\n\n## Comments\n\n### 2026-10-09T12:05:00Z — alex\n\nSchema is in docs/report.md.\n", time.Time{})
		e.sync()

		r := e.remote.only()
		assert.Equal(t, "Add a --json flag", r.title)
		assert.Equal(t, "Print the report as JSON.", r.body)
		got := e.remote.domainLocked(&r)
		assert.Equal(t, "Todo", got.State)
		assert.Contains(t, got.Labels, "cli")
		require.NotNil(t, got.Priority)
		if e.kind == "linear" {
			assert.Equal(t, 2, *got.Priority)
			assert.Equal(t, "ENG-1", r.identifier)
		} else {
			assert.Equal(t, 1, *got.Priority, "local priority 2 is GitHub's p1")
			assert.Equal(t, "#1", r.identifier)
		}

		si := e.issue("ITX-1")
		assert.Equal(t, r.identifier, si.Remote, "the file records the remote identifier")
		content := e.read("ITX-1")
		assert.Contains(t, content, "# Report work.\ntitle: Add a --json flag\nstate: Todo # not started\n", "front matter comments and order are kept")
		assert.Contains(t, content, "remote: ")

		e.sync()
		r = e.remote.only()
		require.Len(t, r.comments, 1, "the file's comment is posted on the next cycle")
		assert.Contains(t, r.comments[0].Body, "**alex** (local issue file):\n\nSchema is in docs/report.md.")
	})
}

// TestLocalSyncTitleEditReachesTheOtherSide (#118): a title edited in the
// file reaches the remote on the next sync, and one edited on the remote
// reaches the file; neither write comes back as an edit.
func TestLocalSyncTitleEditReachesTheOtherSide(t *testing.T) {
	forEachKind(t, func(t *testing.T, e *env) {
		id := e.linked()

		e.edit("ITX-1", "title: Add a --json flag", "title: Add --json to report", time.Time{})
		e.sync()
		assert.Equal(t, "Add --json to report", e.remote.get(id).title)

		e.remote.edit(id, e.now().Add(time.Minute), func(is *fakeIssue) { is.title = "Report: add --json" })
		e.sync()
		assert.Equal(t, "Report: add --json", e.issue("ITX-1").Title)

		e.assertSettled()
	})
}

// TestLocalSyncStateEditReachesTheOtherSide (#118): state moves both ways,
// through the state map (local Review is remote In Review).
func TestLocalSyncStateEditReachesTheOtherSide(t *testing.T) {
	forEachKind(t, func(t *testing.T, e *env) {
		id := e.linked()
		state := func() string { r := e.remote.get(id); return e.remote.domainLocked(&r).State }

		e.edit("ITX-1", "state: Todo", "state: Review", time.Time{})
		e.sync()
		assert.Equal(t, "In Review", state(), "local Review maps to remote In Review")

		e.remote.edit(id, e.now().Add(time.Minute), func(is *fakeIssue) {
			if e.kind == "linear" {
				is.state = "Done"
			} else {
				is.labels = []string{"Done"}
			}
		})
		e.sync()
		assert.Equal(t, "Done", e.issue("ITX-1").State)

		e.remote.edit(id, e.now().Add(2*time.Minute), func(is *fakeIssue) {
			if e.kind == "linear" {
				is.state = "In Review"
			} else {
				is.labels = []string{"In Review"}
			}
		})
		e.sync()
		assert.Equal(t, "Review", e.issue("ITX-1").State, "remote In Review maps back to local Review")

		e.assertSettled()
	})
}

// TestLocalSyncCommentReachesTheOtherSide (#118): a comment added to the
// file is posted on the remote, one posted on the remote is added to the
// file, and neither is copied back.
func TestLocalSyncCommentReachesTheOtherSide(t *testing.T) {
	forEachKind(t, func(t *testing.T, e *env) {
		id := e.linked()

		content := e.read("ITX-1") + "\n## Comments\n\n### 2026-10-10T09:00:00Z — alex\n\nShould this cover CSV too?\n"
		e.write("ITX-1", content, time.Time{})
		e.sync()
		r := e.remote.get(id)
		require.Len(t, r.comments, 1)
		assert.Contains(t, r.comments[0].Body, "Should this cover CSV too?")

		e.remote.humanComment(id, "sam", "No, JSON only.", e.now().Add(time.Minute))
		e.sync()
		si := e.issue("ITX-1")
		require.Len(t, si.Comments, 2)
		assert.Equal(t, "sam", si.Comments[1].Author)
		assert.Equal(t, "No, JSON only.", si.Comments[1].Body)

		e.assertSettled()
		assert.Len(t, e.remote.get(id).comments, 2, "no comment is copied back")
		assert.Len(t, e.issue("ITX-1").Comments, 2)
	})
}

// TestLocalSyncConcurrentEditKeepsNewerAndRecordsConflict (#118): when the
// same field was edited on both sides since the last sync, the more recent
// edit wins on both sides and the conflict is recorded as a comment in the
// file (not posted to the remote).
func TestLocalSyncConcurrentEditKeepsNewerAndRecordsConflict(t *testing.T) {
	for _, remoteNewer := range []bool{true, false} {
		name := "local_newer"
		if remoteNewer {
			name = "remote_newer"
		}
		t.Run(name, func(t *testing.T) {
			forEachKind(t, func(t *testing.T, e *env) {
				id := e.linked()
				base := e.now()
				localAt, remoteAt := base.Add(20*time.Second), base.Add(10*time.Second)
				want := "Local title"
				if remoteNewer {
					localAt, remoteAt = base.Add(10*time.Second), base.Add(20*time.Second)
					want = "Remote title"
				}
				e.edit("ITX-1", "title: Add a --json flag", "title: Local title", localAt)
				e.remote.edit(id, remoteAt, func(is *fakeIssue) { is.title = "Remote title" })
				e.sync()

				assert.Equal(t, want, e.issue("ITX-1").Title)
				assert.Equal(t, want, e.remote.get(id).title)
				comments := e.issue("ITX-1").Comments
				require.Len(t, comments, 1, "the conflict is recorded in the file")
				assert.Equal(t, local.ItervoxAuthor, comments[0].Author)
				assert.Contains(t, comments[0].Body, "Sync conflict on `title`")
				assert.Contains(t, comments[0].Body, "Local title")
				assert.Contains(t, comments[0].Body, "Remote title")
				e.assertSettled()
				assert.Empty(t, e.remote.get(id).comments, "the conflict note stays in the file")
			})
		})
	}
}

// TestLocalSyncPullsNewRemoteIssueIntoFile (#118): a remote issue in a
// pulled state becomes a new local file with its remote id and comments.
func TestLocalSyncPullsNewRemoteIssueIntoFile(t *testing.T) {
	forEachKind(t, func(t *testing.T, e *env) {
		r := e.remote.add("Fix the parser", "It panics on empty input.", "Todo")
		e.remote.humanComment(r.id, "sam", "Repro attached.", e.now())
		e.remote.add("Old work", "", "Done") // not in a pulled state
		e.sync()

		all, err := e.local.SyncIssues()
		require.NoError(t, err)
		require.Len(t, all, 1)
		si := all[0]
		assert.Equal(t, "ITX-1", si.Identifier)
		assert.Equal(t, "Fix the parser", si.Title)
		assert.Equal(t, "Todo", si.State)
		assert.Equal(t, "It panics on empty input.", si.Description)
		assert.Equal(t, r.identifier, si.Remote)
		require.Len(t, si.Comments, 1)
		assert.Equal(t, "sam", si.Comments[0].Author)
		assert.Positive(t, e.changes.Load(), "the orchestrator is told about the new file")

		e.assertSettled()
	})
}

// TestLocalSyncLabelsPriorityAndBlockers (#118): labels, priority and
// blockers sync both ways; blockers map between local identifiers and the
// remote issues they are linked to.
func TestLocalSyncLabelsPriorityAndBlockers(t *testing.T) {
	forEachKind(t, func(t *testing.T, e *env) {
		e.write("ITX-1", "---\ntitle: Parser\nstate: Todo\n---\n", time.Time{})
		e.write("ITX-2", "---\ntitle: CLI\nstate: Todo\nlabels: [cli]\nblocked_by: [ITX-1]\n---\n", time.Time{})
		e.sync()
		e.sync() // ITX-1 is linked now, so ITX-2's blocker can be sent
		r1, r2 := e.remoteFor("ITX-1"), e.remoteFor("ITX-2")
		d2 := e.remote.domainLocked(&r2)
		require.Len(t, d2.BlockedBy, 1)
		assert.Equal(t, r1.id, *d2.BlockedBy[0].ID)
		assert.Contains(t, d2.Labels, "cli")

		e.remote.edit(r2.id, e.now().Add(time.Minute), func(is *fakeIssue) {
			if e.kind == "linear" {
				is.labels, is.priority, is.blockers = []string{"cli", "urgent"}, 1, nil
			} else {
				is.labels = []string{"Todo", "cli", "urgent", "p0"}
				is.body, _ = tracker.SplitSyncBlockersLine(is.body)
			}
		})
		e.sync()
		si := e.issue("ITX-2")
		assert.Equal(t, []string{"cli", "urgent"}, si.Labels)
		require.NotNil(t, si.Priority)
		assert.Equal(t, 1, *si.Priority)
		assert.Empty(t, si.BlockedBy)
		e.assertSettled()
	})
}

// TestLocalSyncSkipsMalformedFiles (#118): a file that does not parse is
// neither created on the remote nor written to.
func TestLocalSyncSkipsMalformedFiles(t *testing.T) {
	forEachKind(t, func(t *testing.T, e *env) {
		e.write("ITX-1", "---\ntitle: Good\nstate: Todo\n---\n", time.Time{})
		bad := "---\nstate: Todo\n---\n\nNo title.\n"
		e.write("ITX-2", bad, time.Time{})
		e.sync()
		e.sync()
		assert.Equal(t, "Good", e.remote.only().title, "only the good file is created remotely")
		assert.Equal(t, bad, e.read("ITX-2"), "the malformed file is not written")
	})
}

// TestLocalSyncAdoptsLinkedFileWithoutLedger (#118): a file that names its
// remote issue but has no ledger entry (a fresh clone, a lost ledger) is
// linked, not created again; the newer side of a differing field wins.
func TestLocalSyncAdoptsLinkedFileWithoutLedger(t *testing.T) {
	forEachKind(t, func(t *testing.T, e *env) {
		id := e.linked()
		require.NoError(t, os.Remove(e.ledger))
		e.s = e.newSyncer()
		e.remote.edit(id, e.now().Add(time.Hour), func(is *fakeIssue) { is.title = "Renamed remotely" })
		e.sync()
		e.remote.mu.Lock()
		n := len(e.remote.issues)
		e.remote.mu.Unlock()
		assert.Equal(t, 1, n, "no second remote issue")
		assert.Equal(t, "Renamed remotely", e.issue("ITX-1").Title)
		assert.Empty(t, e.issue("ITX-1").Comments, "adoption records no conflict")
		e.assertSettled()
	})
}

// TestLocalSyncLedgerRecordsFieldBases (#118): the ledger keeps each
// field's last-synced hash and time, and the link, under .itervox/.
func TestLocalSyncLedgerRecordsFieldBases(t *testing.T) {
	forEachKind(t, func(t *testing.T, e *env) {
		id := e.linked()
		data, err := os.ReadFile(e.ledger)
		require.NoError(t, err)
		var lg ledger
		require.NoError(t, json.Unmarshal(data, &lg))
		l := lg.Links["ITX-1"]
		require.NotNil(t, l)
		assert.Equal(t, id, l.RemoteID)
		for _, f := range syncedFields {
			assert.NotEmpty(t, l.Fields[f].Hash, f)
			assert.False(t, l.Fields[f].SyncedAt.IsZero(), f)
		}
	})
}

// remoteFor is the remote issue linked to a local file.
func (e *env) remoteFor(localID string) fakeIssue {
	e.t.Helper()
	si := e.issue(localID)
	require.NotEmpty(e.t, si.Remote)
	e.remote.mu.Lock()
	var id string
	for _, is := range e.remote.issues {
		if is.identifier == si.Remote {
			id = is.id
		}
	}
	e.remote.mu.Unlock()
	return e.remote.get(id)
}

// assertSettled runs more cycles and checks that nothing moves: the sync's
// own writes do not come back as edits on either side.
func (e *env) assertSettled() {
	e.t.Helper()
	writes := e.remote.writeCount()
	files := map[string]string{}
	entries, _ := os.ReadDir(e.dir)
	for _, de := range entries {
		files[de.Name()] = e.read(strings.TrimSuffix(de.Name(), ".md"))
	}
	e.timeSkew += 2 * time.Minute // past every edit time a test used
	for range 3 {
		e.sync()
	}
	assert.Equal(e.t, writes, e.remote.writeCount(), "no remote write after the sync settled")
	for name, content := range files {
		assert.Equal(e.t, content, e.read(strings.TrimSuffix(name, ".md")), "no local write after the sync settled: %s", name)
	}
}

// TestLocalSyncDeletedFileIsNotPulledBack (#118): deleting a linked file
// leaves the remote issue alone and does not pull it into a new file.
func TestLocalSyncDeletedFileIsNotPulledBack(t *testing.T) {
	forEachKind(t, func(t *testing.T, e *env) {
		id := e.linked()
		require.NoError(t, os.Remove(e.path("ITX-1")))
		e.remote.edit(id, e.now().Add(time.Minute), func(is *fakeIssue) { is.title = "Still here" })
		writes := e.remote.writeCount()
		e.sync()
		e.sync()
		all, err := e.local.SyncIssues()
		require.NoError(t, err)
		assert.Empty(t, all, "the remote issue is not pulled back")
		assert.Equal(t, writes, e.remote.writeCount(), "the remote issue is left alone")
	})
}

// TestLocalSyncQueuedWriteIsNotReadAsRemoteEdit (#118): while a local edit
// waits in the push batch, the remote still shows the old value; a cycle
// that reads the issue then (another change arrived) must not take that old
// value for a remote edit and undo the file's change.
func TestLocalSyncQueuedWriteIsNotReadAsRemoteEdit(t *testing.T) {
	forEachKind(t, func(t *testing.T, e *env) {
		id := e.linked()
		e.ob.SetPushInterval(time.Hour)
		e.edit("ITX-1", "title: Add a --json flag", "title: Queued title", time.Time{})
		require.NoError(t, e.s.Cycle(context.Background()))
		require.Len(t, e.ob.Snapshot(), 1, "the title edit waits in the batch")

		e.remote.humanComment(id, "sam", "Unrelated.", e.now().Add(time.Minute))
		require.NoError(t, e.s.Cycle(context.Background()))
		assert.Equal(t, "Queued title", e.issue("ITX-1").Title, "the old remote title is not an edit")
		assert.Equal(t, "Add a --json flag", e.remote.get(id).title)

		e.ob.SetPushInterval(0)
		for _, en := range e.ob.Snapshot() {
			e.ob.RetryNow(en.ID)
		}
		e.s.Push(context.Background())
		assert.Equal(t, "Queued title", e.remote.get(id).title)
		e.assertSettled()
	})
}
