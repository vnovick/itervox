package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/tracker"
	"github.com/vnovick/itervox/internal/tracker/github"
)

// TestParseItervoxCommand (#84): only a first line starting with /itervox
// is a command.
func TestParseItervoxCommand(t *testing.T) {
	for _, tc := range []struct {
		body string
		want itervoxCommand
		ok   bool
	}{
		{"/itervox run", itervoxCommand{Verb: "run"}, true},
		{"  /itervox run implementer\nplease", itervoxCommand{Verb: "run", Profile: "implementer"}, true},
		{"\n\n/itervox STOP", itervoxCommand{Verb: "stop"}, true},
		{"/itervox review", itervoxCommand{Verb: "review"}, true},
		{"/itervox", itervoxCommand{Verb: "help"}, true},
		{"/itervox deploy", itervoxCommand{Verb: "deploy"}, true},
		{"Thanks! /itervox run", itervoxCommand{}, false},
		{"looks good\n/itervox run", itervoxCommand{}, false},
		{"/itervoxrun", itervoxCommand{}, false},
		{"", itervoxCommand{}, false},
	} {
		got, ok := parseItervoxCommand(tc.body)
		assert.Equal(t, tc.ok, ok, tc.body)
		assert.Equal(t, tc.want, got, tc.body)
	}
}

// fakeCommentTracker is the memory tracker plus the GitHub comment-command
// surface: scripted repository comments, permissions and reactions.
type fakeCommentTracker struct {
	*tracker.MemoryTracker
	mu         sync.Mutex
	comments   []github.RepoComment
	perms      map[string]string
	tokenLogin string
	reactions  []string // "<commentID>:<content>"
	permCalls  atomic.Int32
	edited     map[string]bool // listed whatever `since` says, as GitHub lists edited comments
}

func (f *fakeCommentTracker) ListRepoCommentsSince(_ context.Context, since time.Time) ([]github.RepoComment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []github.RepoComment
	for _, c := range f.comments {
		if !c.CreatedAt.Before(since) || f.edited[c.ID] {
			out = append(out, c)
		}
	}
	return out, nil
}

func (f *fakeCommentTracker) CollaboratorPermission(_ context.Context, login string) (string, error) {
	f.permCalls.Add(1)
	if p, ok := f.perms[login]; ok {
		return p, nil
	}
	return "none", nil
}

func (f *fakeCommentTracker) AuthenticatedLogin(context.Context) (string, error) {
	return f.tokenLogin, nil
}

func (f *fakeCommentTracker) AddCommentReaction(_ context.Context, id, content string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reactions = append(f.reactions, id+":"+content)
	return nil
}

func (f *fakeCommentTracker) addComment(id, login, body string, at time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.comments = append(f.comments, github.RepoComment{ID: id, IssueNumber: "42", Body: body, Login: login, UserType: "User", CreatedAt: at})
}

func (f *fakeCommentTracker) reactionsSnapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.reactions...)
}

func issueState(t *testing.T, f *fakeCommentTracker) string {
	t.Helper()
	issues, err := f.FetchIssueStatesByIDs(context.Background(), []string{"42"})
	require.NoError(t, err)
	require.Len(t, issues, 1)
	return issues[0].State
}

func repliesOn42(t *testing.T, f *fakeCommentTracker) []string {
	t.Helper()
	d, err := f.FetchIssueDetail(context.Background(), "42")
	require.NoError(t, err)
	var out []string
	for _, c := range d.Comments {
		out = append(out, c.Body)
	}
	return out
}

// commandFixture is a real orchestrator over a memory tracker holding #42
// in Backlog, with the comment-command handler wired to it.
func commandFixture(t *testing.T, cc config.CommentCommandsConfig) (*fakeCommentTracker, *commentCommandHandler, *atomic.Int32, string) {
	t.Helper()
	cfg := &config.Config{}
	cfg.Tracker.ActiveStates = []string{"Todo", "In Progress"}
	cfg.Tracker.TerminalStates = []string{"Done"}
	cfg.Tracker.CompletionState = "Done"
	cfg.Polling.IntervalMs = 20
	cfg.Agent.MaxConcurrentAgents = 2
	cfg.Agent.MaxTurns = 1
	cfg.Agent.Profiles = map[string]config.AgentProfile{"implementer": {Command: "claude"}}
	cfg.Agent.ReviewerProfile = ""
	mem := tracker.NewMemoryTracker([]domain.Issue{{ID: "42", Identifier: "#42", Title: "T", State: "Backlog"}},
		cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	f := &fakeCommentTracker{MemoryTracker: mem, perms: map[string]string{"alice": "write", "bob": "read", "itervox-bot": "admin"}, tokenLogin: "itervox-bot"}
	runs := &atomic.Int32{}
	runner := &countingRunner{calls: runs}
	orch := orchestrator.New(cfg, f, runner, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	done := make(chan struct{})
	go func() { _ = orch.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	ledger := filepath.Join(t.TempDir(), "comment_commands.json")
	h := newCommentCommandHandler(cc, f, orch, ledger)
	h.ledger.Cursor = time.Now().Add(-time.Hour)
	return f, h, runs, ledger
}

type countingRunner struct{ calls *atomic.Int32 }

func (r *countingRunner) RunTurn(context.Context, agent.Logger, func(agent.TurnResult), *string, string, string, string, string, string, int, int, agent.PermissionMode) (agent.TurnResult, error) {
	r.calls.Add(1)
	return agent.TurnResult{SessionID: "s", InputTokens: 1, OutputTokens: 1, ResultText: "done"}, nil
}

// TestCommentCommandMaintainerRunDispatches (#84): a maintainer's
// `/itervox run` dispatches the issue — it moves to an active state and an
// agent runs — and is acknowledged with a reaction; a non-maintainer's
// comment does nothing at all.
func TestCommentCommandMaintainerRunDispatches(t *testing.T) {
	f, h, runs, _ := commandFixture(t, config.CommentCommandsConfig{Enabled: true})
	ctx := context.Background()

	f.addComment("1", "bob", "/itervox run", time.Now())
	h.poll(ctx)
	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, "Backlog", issueState(t, f), "a non-maintainer's command changes nothing")
	assert.Equal(t, int32(0), runs.Load())
	assert.Empty(t, f.reactionsSnapshot())
	assert.Empty(t, repliesOn42(t, f), "no reply by default")

	f.addComment("2", "alice", "/itervox run", time.Now())
	h.poll(ctx)
	require.Eventually(t, func() bool { return runs.Load() > 0 }, 5*time.Second, 20*time.Millisecond,
		"the maintainer's command dispatches the issue")
	assert.Equal(t, []string{"2:+1"}, f.reactionsSnapshot())
}

// TestCommentCommandIsActedOnOnce (#84): the same comment is never acted on
// twice — not on the next poll, and not after a restart that reloads the
// ledger.
func TestCommentCommandIsActedOnOnce(t *testing.T) {
	f, h, _, ledger := commandFixture(t, config.CommentCommandsConfig{Enabled: true})
	ctx := context.Background()
	f.addComment("7", "alice", "/itervox stop", time.Now())

	h.poll(ctx)
	h.poll(ctx)
	require.Len(t, f.reactionsSnapshot(), 1, "acted on once across polls")

	restarted := newCommentCommandHandler(h.cfg, f, h.orch, ledger)
	restarted.poll(ctx)
	assert.Len(t, f.reactionsSnapshot(), 1, "not acted on again after a restart")
	_, recorded := restarted.ledger.Handled["7"]
	assert.True(t, recorded)

	// A command posted while the daemon was down is still picked up.
	f.addComment("8", "alice", "/itervox stop", time.Now())
	restarted.poll(ctx)
	assert.Len(t, f.reactionsSnapshot(), 2)
}

// TestCommentCommandPermissions (#84): the permission check and its
// exceptions.
func TestCommentCommandPermissions(t *testing.T) {
	for _, tc := range []struct {
		name      string
		cc        config.CommentCommandsConfig
		login     string
		userType  string
		body      string
		wantActed bool
	}{
		{"write access", config.CommentCommandsConfig{}, "alice", "User", "/itervox stop", true},
		{"read access", config.CommentCommandsConfig{}, "bob", "User", "/itervox stop", false},
		{"not a collaborator", config.CommentCommandsConfig{}, "mallory", "User", "/itervox stop", false},
		{"allow list", config.CommentCommandsConfig{Allow: []string{"Mallory"}}, "mallory", "User", "/itervox stop", true},
		{"bot", config.CommentCommandsConfig{}, "alice", "Bot", "/itervox stop", false},
		{"token user (agents post as it), even as admin", config.CommentCommandsConfig{}, "itervox-bot", "User", "/itervox stop", false},
		{"token user allowed", config.CommentCommandsConfig{AllowTokenUser: true}, "itervox-bot", "User", "/itervox stop", true},
		{"Itervox's own comment", config.CommentCommandsConfig{}, "alice", "User", tracker.MarkManagedComment("/itervox stop"), false},
		{"on a pull request", config.CommentCommandsConfig{}, "alice", "PR", "/itervox stop", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.cc.Enabled = true
			f, h, _, _ := commandFixture(t, tc.cc)
			f.mu.Lock()
			userType, onPR := tc.userType, false
			if userType == "PR" {
				userType, onPR = "User", true
			}
			f.comments = append(f.comments, github.RepoComment{ID: "9", IssueNumber: "42", Body: tc.body, Login: tc.login, UserType: userType, CreatedAt: time.Now(), OnPullRequest: onPR})
			f.mu.Unlock()
			h.poll(context.Background())
			assert.Equal(t, tc.wantActed, len(f.reactionsSnapshot()) > 0, "reactions: %v", f.reactionsSnapshot())
		})
	}
}

// TestCommentCommandRepliesAndErrors (#84): errors are answered on the
// issue (managed, so never re-read as commands); unauthorized users get a
// reply only with reply_to_unauthorized.
func TestCommentCommandRepliesAndErrors(t *testing.T) {
	f, h, runs, _ := commandFixture(t, config.CommentCommandsConfig{Enabled: true, ReplyToUnauthorized: true})
	ctx := context.Background()
	f.addComment("1", "alice", "/itervox run no-such-profile", time.Now())
	f.addComment("2", "alice", "/itervox review", time.Now())
	f.addComment("3", "bob", "/itervox run", time.Now())
	h.poll(ctx)
	h.poll(ctx) // replies are managed comments: never treated as commands

	replies := strings.Join(repliesOn42(t, f), "\n---\n")
	assert.Contains(t, replies, "@alice `/itervox run`: no enabled profile \"no-such-profile\"")
	assert.Contains(t, replies, "@alice `/itervox review`:")
	assert.Contains(t, replies, "@bob only maintainers with write access can run `/itervox` commands here.")
	assert.Equal(t, 3, strings.Count(replies, "@"), "one reply per command, none repeated")
	assert.ElementsMatch(t, []string{"1:confused", "2:confused"}, f.reactionsSnapshot())
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, int32(0), runs.Load())
}

// TestCommentCommandRunWithProfile (#84): `/itervox run <profile>` sets the
// issue's profile before dispatch.
func TestCommentCommandRunWithProfile(t *testing.T) {
	f, h, runs, _ := commandFixture(t, config.CommentCommandsConfig{Enabled: true})
	f.addComment("1", "alice", "/itervox run implementer", time.Now())
	h.poll(context.Background())
	require.Eventually(t, func() bool { return runs.Load() > 0 }, 5*time.Second, 20*time.Millisecond)
	assert.Equal(t, "implementer", h.orch.(*orchestrator.Orchestrator).Snapshot().IssueProfiles["#42"])
}

// TestCommentCommandNotRecordedWhenPermissionCheckFails: a failed check is
// retried on the next poll rather than silently dropping the command.
func TestCommentCommandNotRecordedWhenPermissionCheckFails(t *testing.T) {
	f, h, _, _ := commandFixture(t, config.CommentCommandsConfig{Enabled: true})
	failing := &failingPermTracker{fakeCommentTracker: f}
	h.tr = failing
	f.addComment("1", "carol", "/itervox stop", time.Now())
	at := time.Now().Add(-10 * time.Minute) // older than the poll overlap
	f.mu.Lock()
	f.comments[len(f.comments)-1].CreatedAt = at
	f.comments = append(f.comments, github.RepoComment{ID: "2", IssueNumber: "42", Body: "later", Login: "x", CreatedAt: time.Now()})
	f.mu.Unlock()
	h.poll(context.Background())
	_, recorded := h.ledger.Handled["1"]
	assert.False(t, recorded)
	assert.False(t, h.ledger.Cursor.After(at), "the cursor stays at the unhandled command, so the next poll sees it again")

	h.tr = f // the check works again
	h.poll(context.Background())
	assert.True(t, h.ledger.Cursor.After(at), "retried once the check succeeds, then the cursor moves on")
	assert.Empty(t, h.failures)
}

// TestCommentCommandGivesUpOnAPermanentlyFailingCheck (#84): a check that
// never succeeds stops holding the cursor after a bounded number of polls.
func TestCommentCommandGivesUpOnAPermanentlyFailingCheck(t *testing.T) {
	f, h, _, _ := commandFixture(t, config.CommentCommandsConfig{Enabled: true})
	h.tr = &failingPermTracker{fakeCommentTracker: f}
	f.addComment("1", "carol", "/itervox stop", time.Now().Add(-5*time.Minute))
	for i := 0; i < commentCommandMaxCheckFailures; i++ {
		h.poll(context.Background())
	}
	_, recorded := h.ledger.Handled["1"]
	assert.True(t, recorded, "recorded as not authorized after the bounded retries")
	assert.Empty(t, f.reactionsSnapshot(), "never acted on")
}

// TestCommentCommandIgnoresEditedOldComments (#84): GitHub lists comments
// by update time; an older comment edited into a command is not obeyed.
func TestCommentCommandIgnoresEditedOldComments(t *testing.T) {
	f, h, _, _ := commandFixture(t, config.CommentCommandsConfig{Enabled: true})
	h.ledger.Cursor = time.Now()
	f.addComment("1", "alice", "/itervox stop", time.Now().Add(-30*24*time.Hour))
	f.edited = map[string]bool{"1": true} // edited today, so GitHub lists it
	h.poll(context.Background())
	assert.Empty(t, f.reactionsSnapshot())
}

// TestCommentCommandRecordsBeforeActing (#84): when the record cannot be
// saved, the command is not acted on (it could otherwise run twice).
func TestCommentCommandRecordsBeforeActing(t *testing.T) {
	f, h, _, _ := commandFixture(t, config.CommentCommandsConfig{Enabled: true})
	blocker := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(blocker, nil, 0o600))
	h.ledgerPath = filepath.Join(blocker, "comment_commands.json") // parent is a file: saving fails
	f.addComment("1", "alice", "/itervox stop", time.Now())
	h.poll(context.Background())
	assert.Empty(t, f.reactionsSnapshot(), "not acted on without a durable record")
}

// TestCommentCommandRunWhileRunning (#84): `/itervox run` on an issue that
// is already running is refused, not dispatched twice.
func TestCommentCommandRunWhileRunning(t *testing.T) {
	f, h, runs, _ := commandFixture(t, config.CommentCommandsConfig{Enabled: true})
	f.addComment("1", "alice", "/itervox run", time.Now())
	h.poll(context.Background())
	require.Eventually(t, func() bool { return runs.Load() > 0 }, 5*time.Second, 20*time.Millisecond)
	h.orch = &runningOrch{commentCommandOrch: h.orch}
	f.addComment("2", "alice", "/itervox run", time.Now())
	h.poll(context.Background())
	assert.Contains(t, strings.Join(repliesOn42(t, f), "\n"), "@alice `/itervox run`: already running")
}

// runningOrch reports issue 42 as running.
type runningOrch struct{ commentCommandOrch }

func (r *runningOrch) Snapshot() orchestrator.State {
	s := r.commentCommandOrch.Snapshot()
	s.Running = map[string]*orchestrator.RunEntry{"42": {}}
	return s
}

type failingPermTracker struct{ *fakeCommentTracker }

func (f *failingPermTracker) CollaboratorPermission(context.Context, string) (string, error) {
	return "", errors.New("503")
}
