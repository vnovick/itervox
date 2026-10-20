package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/atomicfs"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/tracker"
	"github.com/vnovick/itervox/internal/tracker/github"
)

// `/itervox` comment commands (#84). A maintainer drives Itervox from a
// GitHub issue comment:
//
//	/itervox run [profile]   dispatch the issue (optionally under a profile)
//	/itervox stop            stop (pause) the issue's running agent
//	/itervox review          dispatch the configured reviewer
//
// Security model:
//   - only users with write access (admin, maintain, write) or listed in
//     comment_commands.allow are obeyed; everyone else is ignored (or gets a
//     short reply with reply_to_unauthorized);
//   - Itervox's own comments (the managed marker) and bots are never obeyed,
//     and neither is the token's own account unless allow_token_user is set,
//     because agents comment through that account;
//   - each comment is acted on at most once: its ID is recorded on disk
//     before the action, so neither a retry nor a restart repeats it.
//
// Comments are read with one repository-wide request per poll, not per issue.

const (
	commentCommandPollInterval = 30 * time.Second
	commentCommandOverlap      = 2 * time.Minute // clock skew between GitHub and here
	commentCommandPermTTL      = 10 * time.Minute
)

// itervoxCommand is a parsed `/itervox` command.
type itervoxCommand struct {
	Verb    string // run, stop, review, or the unknown word
	Profile string // run's optional profile
}

// parseItervoxCommand reads a command from the comment's first non-empty
// line, which must start with `/itervox`. A mention later in a comment is
// not a command.
func parseItervoxCommand(body string) (itervoxCommand, bool) {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if fields[0] != "/itervox" {
			return itervoxCommand{}, false
		}
		if len(fields) == 1 {
			return itervoxCommand{Verb: "help"}, true
		}
		cmd := itervoxCommand{Verb: strings.ToLower(fields[1])}
		if cmd.Verb == "run" && len(fields) > 2 {
			cmd.Profile = fields[2]
		}
		return cmd, true
	}
	return itervoxCommand{}, false
}

// commentCommandTracker is what the handler needs from the GitHub client.
type commentCommandTracker interface {
	ListRepoCommentsSince(ctx context.Context, since time.Time) ([]github.RepoComment, error)
	CollaboratorPermission(ctx context.Context, login string) (string, error)
	AuthenticatedLogin(ctx context.Context) (string, error)
	AddCommentReaction(ctx context.Context, commentID, content string) error
	CreateComment(ctx context.Context, issueID, body string) (*domain.Comment, error)
	UpdateIssueState(ctx context.Context, issueID, stateName string) error
	FetchIssueStatesByIDs(ctx context.Context, issueIDs []string) ([]domain.Issue, error)
}

// commentCommandOrch is what the handler needs from the orchestrator.
type commentCommandOrch interface {
	Snapshot() orchestrator.State
	SetIssueProfile(identifier, profileName string)
	CancelIssue(identifier string) error
	ResumeIssue(identifier string) error
	DispatchReviewer(identifier string) error
	ProfilesCfg() map[string]config.AgentProfile
	TrackerStatesCfg() (active, terminal []string, completion string)
	Refresh()
}

// commentCommandLedger is the on-disk record of handled comments.
type commentCommandLedger struct {
	Version int                  `json:"version"`
	Cursor  time.Time            `json:"cursor"`
	Handled map[string]time.Time `json:"handled"` // comment ID → the comment's creation time
	// Floor is when commands were enabled: nothing created before it is
	// ever acted on, across restarts too (the cursor's overlap window
	// would otherwise reach back past it after a quick restart).
	Floor time.Time `json:"floor,omitempty"`
}

type commentCommandHandler struct {
	cfg        config.CommentCommandsConfig
	tr         commentCommandTracker
	orch       commentCommandOrch
	ledgerPath string
	ledger     commentCommandLedger
	tokenLogin string
	perms      map[string]cachedPermission
	failures   map[string]int // comment ID → polls whose permission check failed
	now        func() time.Time
	// floor, when set, is the earliest creation time a comment may have:
	// the time the handler started without a readable ledger, so neither
	// enabling the feature nor losing the ledger replays recent commands.
	floor time.Time
}

// commentCommandMaxCheckFailures bounds how long a comment whose author's
// permission cannot be read keeps the cursor back: after this many polls it
// is recorded as not authorized, so one broken check cannot pin the window.
const commentCommandMaxCheckFailures = 10

type cachedPermission struct {
	perm string
	at   time.Time
}

func newCommentCommandHandler(cfg config.CommentCommandsConfig, tr commentCommandTracker, orch commentCommandOrch, ledgerPath string) *commentCommandHandler {
	h := &commentCommandHandler{cfg: cfg, tr: tr, orch: orch, ledgerPath: ledgerPath,
		perms: map[string]cachedPermission{}, failures: map[string]int{}, now: time.Now}
	h.loadLedger()
	return h
}

// loadLedger reads the ledger; without one, commands start from now, so
// enabling the feature never replays a repository's old comments.
func (h *commentCommandHandler) loadLedger() {
	start := h.now().UTC()
	h.ledger = commentCommandLedger{Version: 1, Cursor: start, Handled: map[string]time.Time{}, Floor: start}
	h.floor = start
	raw, err := os.ReadFile(h.ledgerPath)
	if err != nil {
		return
	}
	var l commentCommandLedger
	if err := json.Unmarshal(raw, &l); err != nil || l.Version != 1 {
		slog.Warn("comment commands: unreadable ledger; starting from now", "path", h.ledgerPath, "error", err)
		return
	}
	if l.Handled == nil {
		l.Handled = map[string]time.Time{}
	}
	h.ledger = l
	h.floor = l.Floor // zero for a ledger written before the floor was kept
}

func (h *commentCommandHandler) saveLedger() error {
	// A comment created before the window can never be acted on again (see
	// poll), so its record can go; nothing still inside the window is
	// dropped. This is safe only because the cursor never moves back (see
	// poll): a window that later reopened would find records gone.
	for id, created := range h.ledger.Handled {
		if created.Before(h.windowStart()) {
			delete(h.ledger.Handled, id)
		}
	}
	data, err := json.MarshalIndent(h.ledger, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(h.ledgerPath), 0o755); err != nil {
		return err
	}
	return atomicfs.WriteFile(h.ledgerPath, data, 0o600)
}

// windowStart is the earliest creation time a comment may have to be acted
// on: the cursor less the clock-skew overlap, and never before the floor.
func (h *commentCommandHandler) windowStart() time.Time {
	return laterTime(h.ledger.Cursor.Add(-commentCommandOverlap), h.floor)
}

func laterTime(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// poll reads new comments and acts on the commands among them.
func (h *commentCommandHandler) poll(ctx context.Context) {
	if h.tokenLogin == "" && !h.cfg.AllowTokenUser {
		login, err := h.tr.AuthenticatedLogin(ctx)
		if err != nil {
			slog.Warn("comment commands: cannot read the token's login; skipping this poll", "error", err)
			return
		}
		h.tokenLogin = login
	}
	windowStart := h.windowStart()
	comments, err := h.tr.ListRepoCommentsSince(ctx, windowStart)
	if err != nil {
		slog.Warn("comment commands: listing comments failed", "error", err)
		return
	}
	var retryFrom time.Time // oldest command whose permission check failed
	// The cursor moves only at the end of the poll: the record saves made
	// while acting prune against the window this poll started with.
	latest := h.ledger.Cursor
	for _, c := range comments {
		if c.CreatedAt.After(latest) {
			latest = c.CreatedAt
		}
		if _, done := h.ledger.Handled[c.ID]; done {
			continue
		}
		// `since` matches edits too: an older comment edited into a command
		// is not one. Only comments created inside the window count.
		if c.CreatedAt.Before(windowStart) {
			continue
		}
		if tracker.IsManagedComment(domain.Comment{Body: c.Body}) {
			continue // Itervox's own comment
		}
		if c.OnPullRequest {
			continue // commands act on issues; a PR is not dispatched or labelled
		}
		cmd, ok := parseItervoxCommand(c.Body)
		if !ok {
			continue
		}
		if retry := h.handle(ctx, c, cmd); retry && (retryFrom.IsZero() || c.CreatedAt.Before(retryFrom)) {
			retryFrom = c.CreatedAt
		}
	}
	// Keep a command whose check failed inside the next poll's window, so a
	// longer outage than the overlap cannot drop it: the cursor stays where
	// it was (the command is inside that window, being listed by it). It
	// never moves back, so the records pruned against an earlier window are
	// never needed again.
	if !retryFrom.IsZero() && retryFrom.Before(latest) {
		latest = laterTime(h.ledger.Cursor, retryFrom)
	}
	h.ledger.Cursor = latest
	if err := h.saveLedger(); err != nil {
		slog.Warn("comment commands: saving the ledger failed", "error", err)
	}
}

// authorized reports whether c's author may run commands, and why not.
func (h *commentCommandHandler) authorized(ctx context.Context, c github.RepoComment) (bool, string) {
	// The token's own account first, even when it is allow-listed: agents
	// post with that token, so only allow_token_user lets it run commands.
	if h.tokenLogin != "" && strings.EqualFold(c.Login, h.tokenLogin) {
		return false, "token user"
	}
	if slices.ContainsFunc(h.cfg.Allow, func(a string) bool { return strings.EqualFold(a, c.Login) }) {
		return true, ""
	}
	if strings.EqualFold(c.UserType, "Bot") {
		return false, "bot"
	}
	perm, err := h.permission(ctx, c.Login)
	if err != nil {
		return false, "permission check failed: " + err.Error()
	}
	switch perm {
	case "admin", "write":
		return true, ""
	}
	return false, "permission " + perm
}

func (h *commentCommandHandler) permission(ctx context.Context, login string) (string, error) {
	key := strings.ToLower(login)
	if p, ok := h.perms[key]; ok && h.now().Sub(p.at) < commentCommandPermTTL {
		return p.perm, nil
	}
	perm, err := h.tr.CollaboratorPermission(ctx, login)
	if err != nil {
		return "", err
	}
	h.perms[key] = cachedPermission{perm: perm, at: h.now()}
	return perm, nil
}

// handle records the comment as handled (durably, first) and acts on it.
// It reports retry when the comment must be looked at again next poll.
func (h *commentCommandHandler) handle(ctx context.Context, c github.RepoComment, cmd itervoxCommand) (retry bool) {
	ok, why := h.authorized(ctx, c)
	if !ok && strings.HasPrefix(why, "permission check failed") {
		h.failures[c.ID]++
		if h.failures[c.ID] < commentCommandMaxCheckFailures {
			// Not recorded: the check is retried on the next poll.
			slog.Warn("comment commands: "+why, "comment_id", c.ID, "login", c.Login)
			return true
		}
		slog.Warn("comment commands: giving up on a comment whose author's permission cannot be read",
			"comment_id", c.ID, "login", c.Login, "attempts", h.failures[c.ID])
		why = "permission unreadable"
	}
	delete(h.failures, c.ID)
	h.ledger.Handled[c.ID] = c.CreatedAt.UTC()
	if err := h.saveLedger(); err != nil {
		// Without a durable record the command could run twice; do not act.
		delete(h.ledger.Handled, c.ID)
		slog.Warn("comment commands: cannot record the comment; not acting on it", "comment_id", c.ID, "error", err)
		return true
	}
	if !ok {
		slog.Info("comment commands: ignored a command from a user without access",
			"comment_id", c.ID, "login", c.Login, "reason", why)
		if h.cfg.ReplyToUnauthorized && why != "token user" && why != "bot" && why != "permission unreadable" {
			h.reply(ctx, c, fmt.Sprintf("@%s only maintainers with write access can run `/itervox` commands here.", c.Login))
		}
		return false
	}
	slog.Info("comment commands: running", "comment_id", c.ID, "issue", c.IssueNumber, "login", c.Login, "verb", cmd.Verb)
	msg, err := h.act(ctx, c.IssueNumber, cmd)
	if err != nil {
		h.react(ctx, c, "confused")
		h.reply(ctx, c, fmt.Sprintf("@%s `/itervox %s`: %v", c.Login, cmd.Verb, err))
		return false
	}
	h.react(ctx, c, "+1")
	if msg != "" {
		h.reply(ctx, c, msg)
	}
	return false
}

// act performs cmd on issue #number. A non-empty message is posted as a
// reply; an error is reported as one.
func (h *commentCommandHandler) act(ctx context.Context, number string, cmd itervoxCommand) (string, error) {
	identifier := "#" + number
	switch cmd.Verb {
	case "run":
		return h.run(ctx, number, identifier, cmd.Profile)
	case "stop":
		if err := h.orch.CancelIssue(identifier); err != nil {
			return "", fmt.Errorf("nothing to stop (%v)", err)
		}
		return "", nil
	case "review":
		if err := h.orch.DispatchReviewer(identifier); err != nil {
			return "", err
		}
		return "", nil
	default:
		return "", fmt.Errorf("unknown command; use `/itervox run [profile]`, `/itervox stop` or `/itervox review`")
	}
}

func (h *commentCommandHandler) run(ctx context.Context, number, identifier, profile string) (string, error) {
	if profile != "" {
		p, ok := h.orch.ProfilesCfg()[profile]
		if !ok || (p.Enabled != nil && !*p.Enabled) {
			return "", fmt.Errorf("no enabled profile %q", profile)
		}
	}
	snap := h.orch.Snapshot()
	if _, running := snap.Running[number]; running {
		return "", fmt.Errorf("already running")
	}
	if _, paused := snap.PausedIdentifiers[identifier]; paused {
		if profile != "" {
			h.orch.SetIssueProfile(identifier, profile)
		}
		if err := h.orch.ResumeIssue(identifier); err != nil {
			return "", err
		}
		h.orch.Refresh()
		return "", nil
	}
	active, terminal, _ := h.orch.TrackerStatesCfg()
	issues, err := h.tr.FetchIssueStatesByIDs(ctx, []string{number})
	if err != nil || len(issues) == 0 {
		return "", fmt.Errorf("cannot read the issue's state (%v)", err)
	}
	state := issues[0].State
	if slices.ContainsFunc(terminal, func(s string) bool { return strings.EqualFold(s, state) }) {
		return "", fmt.Errorf("the issue is closed (%s)", state)
	}
	if profile != "" {
		h.orch.SetIssueProfile(identifier, profile)
	}
	if !slices.ContainsFunc(active, func(s string) bool { return strings.EqualFold(s, state) }) {
		if len(active) == 0 {
			return "", fmt.Errorf("no active state is configured")
		}
		if err := h.tr.UpdateIssueState(ctx, number, active[0]); err != nil {
			return "", fmt.Errorf("cannot move the issue to %q (%v)", active[0], err)
		}
	}
	h.orch.Refresh()
	return "", nil
}

func (h *commentCommandHandler) react(ctx context.Context, c github.RepoComment, content string) {
	if err := h.tr.AddCommentReaction(ctx, c.ID, content); err != nil {
		slog.Warn("comment commands: reaction failed", "comment_id", c.ID, "error", err)
	}
}

func (h *commentCommandHandler) reply(ctx context.Context, c github.RepoComment, body string) {
	if _, err := h.tr.CreateComment(ctx, c.IssueNumber, tracker.MarkManagedComment(body)); err != nil {
		slog.Warn("comment commands: reply failed", "comment_id", c.ID, "error", err)
	}
}

// startCommentCommands polls for commands when tracker.comment_commands is
// enabled and the tracker is GitHub.
//
// The returned channel closes when the poller has stopped (after ctx is
// cancelled); run() waits for it, so a WORKFLOW.md reload never has two
// pollers sharing the ledger.
func startCommentCommands(ctx context.Context, cfg *config.Config, workflowPath string, tr tracker.Tracker, orch *orchestrator.Orchestrator) <-chan struct{} {
	done := make(chan struct{})
	if !cfg.Tracker.CommentCommands.Enabled {
		close(done)
		return done
	}
	gh, ok := tr.(commentCommandTracker)
	if !ok {
		slog.Warn("comment commands: enabled, but the tracker cannot read repository comments (GitHub only)")
		close(done)
		return done
	}
	ledger := filepath.Join(filepath.Dir(workflowPath), ".itervox", "comment_commands.json")
	h := newCommentCommandHandler(cfg.Tracker.CommentCommands, gh, orch, ledger)
	slog.Info("comment commands: enabled", "ledger", ledger, "allow", cfg.Tracker.CommentCommands.Allow,
		"allow_token_user", cfg.Tracker.CommentCommands.AllowTokenUser)
	go func() {
		defer close(done)
		defer failFastOnPanic("comment-commands")
		ticker := time.NewTicker(commentCommandPollInterval)
		defer ticker.Stop()
		for {
			h.poll(ctx)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return done
}
