package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
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
	commentCommandLedgerMax    = 2000
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
	Handled map[string]time.Time `json:"handled"` // comment ID → when it was handled
}

type commentCommandHandler struct {
	cfg        config.CommentCommandsConfig
	tr         commentCommandTracker
	orch       commentCommandOrch
	ledgerPath string
	ledger     commentCommandLedger
	tokenLogin string
	perms      map[string]cachedPermission
	now        func() time.Time
}

type cachedPermission struct {
	perm string
	at   time.Time
}

func newCommentCommandHandler(cfg config.CommentCommandsConfig, tr commentCommandTracker, orch commentCommandOrch, ledgerPath string) *commentCommandHandler {
	h := &commentCommandHandler{cfg: cfg, tr: tr, orch: orch, ledgerPath: ledgerPath,
		perms: map[string]cachedPermission{}, now: time.Now}
	h.loadLedger()
	return h
}

// loadLedger reads the ledger; without one, commands start from now, so
// enabling the feature never replays a repository's old comments.
func (h *commentCommandHandler) loadLedger() {
	h.ledger = commentCommandLedger{Version: 1, Cursor: h.now().UTC(), Handled: map[string]time.Time{}}
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
}

func (h *commentCommandHandler) saveLedger() error {
	if len(h.ledger.Handled) > commentCommandLedgerMax {
		type kv struct {
			id string
			at time.Time
		}
		all := make([]kv, 0, len(h.ledger.Handled))
		for id, at := range h.ledger.Handled {
			all = append(all, kv{id, at})
		}
		sort.Slice(all, func(i, j int) bool { return all[i].at.After(all[j].at) })
		h.ledger.Handled = map[string]time.Time{}
		for _, e := range all[:commentCommandLedgerMax] {
			h.ledger.Handled[e.id] = e.at
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
	comments, err := h.tr.ListRepoCommentsSince(ctx, h.ledger.Cursor.Add(-commentCommandOverlap))
	if err != nil {
		slog.Warn("comment commands: listing comments failed", "error", err)
		return
	}
	var retryFrom time.Time // oldest command whose permission check failed
	for _, c := range comments {
		if c.CreatedAt.After(h.ledger.Cursor) {
			h.ledger.Cursor = c.CreatedAt
		}
		if _, done := h.ledger.Handled[c.ID]; done {
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
	// longer outage than the overlap cannot drop it.
	if !retryFrom.IsZero() && retryFrom.Before(h.ledger.Cursor) {
		h.ledger.Cursor = retryFrom
	}
	if err := h.saveLedger(); err != nil {
		slog.Warn("comment commands: saving the ledger failed", "error", err)
	}
}

// authorized reports whether c's author may run commands, and why not.
func (h *commentCommandHandler) authorized(ctx context.Context, c github.RepoComment) (bool, string) {
	if slices.ContainsFunc(h.cfg.Allow, func(a string) bool { return strings.EqualFold(a, c.Login) }) {
		return true, ""
	}
	if strings.EqualFold(c.UserType, "Bot") {
		return false, "bot"
	}
	if h.tokenLogin != "" && strings.EqualFold(c.Login, h.tokenLogin) {
		return false, "token user"
	}
	perm, err := h.permission(ctx, c.Login)
	if err != nil {
		return false, "permission check failed: " + err.Error()
	}
	switch perm {
	case "admin", "maintain", "write":
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
		// Not recorded: the check is retried on the next poll.
		slog.Warn("comment commands: "+why, "comment_id", c.ID, "login", c.Login)
		return true
	}
	h.ledger.Handled[c.ID] = h.now().UTC()
	if err := h.saveLedger(); err != nil {
		// Without a durable record the command could run twice; do not act.
		delete(h.ledger.Handled, c.ID)
		slog.Warn("comment commands: cannot record the comment; not acting on it", "comment_id", c.ID, "error", err)
		return true
	}
	if !ok {
		slog.Info("comment commands: ignored a command from a user without access",
			"comment_id", c.ID, "login", c.Login, "reason", why)
		if h.cfg.ReplyToUnauthorized && why != "token user" && why != "bot" {
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
	if profile != "" {
		h.orch.SetIssueProfile(identifier, profile)
	}
	if _, paused := snap.PausedIdentifiers[identifier]; paused {
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
func startCommentCommands(ctx context.Context, cfg *config.Config, workflowPath string, tr tracker.Tracker, orch *orchestrator.Orchestrator) {
	if !cfg.Tracker.CommentCommands.Enabled {
		return
	}
	gh, ok := tr.(commentCommandTracker)
	if !ok {
		slog.Warn("comment commands: enabled, but the tracker cannot read repository comments (GitHub only)")
		return
	}
	ledger := filepath.Join(filepath.Dir(workflowPath), ".itervox", "comment_commands.json")
	h := newCommentCommandHandler(cfg.Tracker.CommentCommands, gh, orch, ledger)
	slog.Info("comment commands: enabled", "ledger", ledger, "allow", cfg.Tracker.CommentCommands.Allow,
		"allow_token_user", cfg.Tracker.CommentCommands.AllowTokenUser)
	go func() {
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
}
