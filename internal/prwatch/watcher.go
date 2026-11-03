package prwatch

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Event kinds; each is the name of the automation trigger it can fire.
const (
	CheckCompleted   = "pr_check_completed"
	Synchronized     = "pr_synchronized"
	ReviewSubmitted  = "pr_review_submitted"
	MergeableChanged = "pr_mergeable_changed"
)

// Event sources.
const (
	SourceWebhook = "webhook"
	SourcePoll    = "poll"
)

// Event is one observation of activity on a managed pull request.
type Event struct {
	Kind string
	// Identifier is the issue the managed PR belongs to.
	Identifier string
	PRURL      string
	PRNumber   int
	Branch     string
	HeadSHA    string
	// MergeableState is the PR's mergeable_state when known.
	MergeableState string
	// CheckCompleted: check run name or status context, conclusion (lower
	// case) and completion time.
	CheckName   string
	Conclusion  string
	CompletedAt time.Time
	// ReviewSubmitted.
	ReviewID     string
	ReviewAuthor string
	ReviewState  string // lower case
	SubmittedAt  time.Time
	Source       string
}

// Managed is a pull request itervox manages.
type Managed struct {
	URL        string
	Identifier string
	// Since: checks completed and reviews submitted before it are skipped.
	Since time.Time
}

// Config configures a Watcher.
type Config struct {
	// Client reads GitHub.
	Client *Client
	// Managed lists the managed PRs (the orchestrator's last snapshot).
	Managed func() []Managed
	// Kinds reports which event kinds have an automation rule; nothing is
	// fetched for the others.
	Kinds func() map[string]bool
	// Emit hands one event on. It may block (it resolves the issue and
	// sends to the orchestrator).
	Emit func(ctx context.Context, ev Event)
	// Webhooks: check runs, statuses and reviews arrive by webhook, so the
	// poll reads only each PR (its head SHA and mergeable_state, which no
	// webhook reports).
	Webhooks bool
}

// Watcher polls the open managed PRs and routes webhook deliveries to them.
// It remembers, per PR, what it already emitted so an unchanged PR emits
// nothing; after a restart it emits once more and the orchestrator's
// persisted dedup drops the repeats.
type Watcher struct {
	cfg Config

	mu  sync.Mutex
	prs map[string]*prState // key: lower-case PR URL
}

type prState struct {
	ref       PRRef
	url       string
	closed    bool
	number    int
	headSHA   string
	branch    string
	mergeable string
	// emittedHead / emittedMergeable: what was last emitted as
	// Synchronized / MergeableChanged.
	emittedHead      string
	emittedMergeable string
	// seen holds the check and review keys already emitted.
	seen map[string]struct{}
}

// New returns a Watcher.
func New(cfg Config) *Watcher {
	return &Watcher{cfg: cfg, prs: map[string]*prState{}}
}

// managedByURL returns the managed PRs keyed by lower-case URL.
func (w *Watcher) managedByURL() map[string]Managed {
	out := map[string]Managed{}
	if w.cfg.Managed == nil {
		return out
	}
	for _, m := range w.cfg.Managed() {
		out[strings.ToLower(m.URL)] = m
	}
	return out
}

func (w *Watcher) kinds() map[string]bool {
	if w.cfg.Kinds == nil {
		return nil
	}
	return w.cfg.Kinds()
}

// state returns the PR's state, creating it. Caller holds w.mu.
func (w *Watcher) state(m Managed) (*prState, bool) {
	key := strings.ToLower(m.URL)
	if st, ok := w.prs[key]; ok {
		return st, true
	}
	ref, ok := ParsePRURL(m.URL)
	if !ok {
		return nil, false
	}
	st := &prState{ref: ref, url: m.URL, number: ref.Number, seen: map[string]struct{}{}}
	w.prs[key] = st
	return st, true
}

// Poll reads every open managed PR once and emits what changed. With no
// PR-event rule configured it fetches nothing.
func (w *Watcher) Poll(ctx context.Context) {
	kinds := w.kinds()
	if len(kinds) == 0 || w.cfg.Client == nil {
		return
	}
	managed := w.managedByURL()
	w.mu.Lock()
	for key := range w.prs {
		if _, ok := managed[key]; !ok {
			delete(w.prs, key) // no longer managed (issue finished)
		}
	}
	w.mu.Unlock()
	for _, m := range sortedManaged(managed) {
		if ctx.Err() != nil {
			return
		}
		w.pollPR(ctx, m, kinds)
	}
}

func sortedManaged(m map[string]Managed) []Managed {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	out := make([]Managed, 0, len(keys))
	for _, k := range keys {
		out = append(out, m[k])
	}
	return out
}

func (w *Watcher) pollPR(ctx context.Context, m Managed, kinds map[string]bool) {
	w.mu.Lock()
	st, ok := w.state(m)
	closed := ok && st.closed
	w.mu.Unlock()
	if !ok || closed {
		return
	}
	pr, err := w.cfg.Client.PR(ctx, st.ref)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			slog.Warn("pr events: pull request not readable (deleted, or the GitHub token cannot see it); not polling it again this session",
				"pr_url", m.URL)
			w.mu.Lock()
			st.closed = true
			w.mu.Unlock()
			return
		}
		if ctx.Err() == nil {
			slog.Warn("pr events: poll failed", "pr_url", m.URL, "error", err)
		}
		return
	}
	var out []Event
	w.mu.Lock()
	if pr.State != "open" {
		st.closed = true
		w.mu.Unlock()
		return
	}
	st.headSHA, st.branch, st.mergeable = pr.Head.SHA, pr.Head.Ref, pr.MergeableState
	if pr.Number > 0 {
		st.number = pr.Number
	}
	base := w.baseEvent(st, m, SourcePoll)
	if kinds[Synchronized] && st.headSHA != "" && st.headSHA != st.emittedHead {
		st.emittedHead = st.headSHA
		ev := base
		ev.Kind = Synchronized
		out = append(out, ev)
	}
	if kinds[MergeableChanged] && st.mergeable != "" && st.mergeable != "unknown" && st.mergeable != st.emittedMergeable {
		st.emittedMergeable = st.mergeable
		ev := base
		ev.Kind = MergeableChanged
		out = append(out, ev)
	}
	w.mu.Unlock()

	if !w.cfg.Webhooks && kinds[CheckCompleted] && base.HeadSHA != "" {
		out = append(out, w.pollChecks(ctx, st, m, base)...)
	}
	if !w.cfg.Webhooks && kinds[ReviewSubmitted] {
		out = append(out, w.pollReviews(ctx, st, m, base)...)
	}
	for _, ev := range out {
		w.cfg.Emit(ctx, ev)
	}
}

func (w *Watcher) baseEvent(st *prState, m Managed, source string) Event {
	return Event{
		Identifier: m.Identifier, PRURL: m.URL, PRNumber: st.number, Branch: st.branch,
		HeadSHA: st.headSHA, MergeableState: st.mergeable, Source: source,
	}
}

func (w *Watcher) pollChecks(ctx context.Context, st *prState, m Managed, base Event) []Event {
	var out []Event
	runs, err := w.cfg.Client.CheckRuns(ctx, st.ref, base.HeadSHA)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("pr events: reading check runs failed", "pr_url", m.URL, "error", err)
		}
	}
	for _, r := range runs {
		if r.Status != "completed" || r.Conclusion == "" {
			continue
		}
		if ev, ok := w.checkEvent(st, m, base, r.Name, r.Conclusion, r.CompletedAt); ok {
			out = append(out, ev)
		}
	}
	statuses, err := w.cfg.Client.Statuses(ctx, st.ref, base.HeadSHA)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("pr events: reading commit statuses failed", "pr_url", m.URL, "error", err)
		}
	}
	latest := map[string]bool{}
	for _, s := range statuses { // newest first: the first per context is current
		if latest[s.Context] {
			continue
		}
		latest[s.Context] = true
		if !statusCompleted(s.State) {
			continue
		}
		if ev, ok := w.checkEvent(st, m, base, s.Context, s.State, s.UpdatedAt); ok {
			out = append(out, ev)
		}
	}
	return out
}

func statusCompleted(state string) bool {
	switch strings.ToLower(state) {
	case "success", "failure", "error":
		return true
	}
	return false
}

// checkEvent builds a CheckCompleted event unless it predates the PR's
// management or was already emitted.
func (w *Watcher) checkEvent(st *prState, m Managed, base Event, name, conclusion string, completedAt time.Time) (Event, bool) {
	if !completedAt.IsZero() && completedAt.Before(m.Since) {
		return Event{}, false
	}
	conclusion = strings.ToLower(conclusion)
	key := "check|" + base.HeadSHA + "|" + strings.ToLower(name) + "|" + conclusion
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, done := st.seen[key]; done {
		return Event{}, false
	}
	st.seen[key] = struct{}{}
	ev := base
	ev.Kind, ev.CheckName, ev.Conclusion, ev.CompletedAt = CheckCompleted, name, conclusion, completedAt
	return ev, true
}

func (w *Watcher) pollReviews(ctx context.Context, st *prState, m Managed, base Event) []Event {
	reviews, err := w.cfg.Client.Reviews(ctx, st.ref)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("pr events: reading reviews failed", "pr_url", m.URL, "error", err)
		}
		return nil
	}
	var out []Event
	for _, r := range reviews {
		if ev, ok := w.reviewEvent(st, m, base, r.ID, r.User.Login, r.State, r.SubmittedAt); ok {
			out = append(out, ev)
		}
	}
	return out
}

func (w *Watcher) reviewEvent(st *prState, m Managed, base Event, id int64, author, state string, submittedAt time.Time) (Event, bool) {
	state = strings.ToLower(state)
	if id == 0 || state == "" || state == "pending" || submittedAt.IsZero() || submittedAt.Before(m.Since) {
		return Event{}, false
	}
	key := "review|" + strconv.FormatInt(id, 10)
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, done := st.seen[key]; done {
		return Event{}, false
	}
	st.seen[key] = struct{}{}
	ev := base
	ev.Kind, ev.ReviewID, ev.ReviewAuthor, ev.ReviewState, ev.SubmittedAt = ReviewSubmitted, strconv.FormatInt(id, 10), author, state, submittedAt
	return ev, true
}

// managedFor returns the managed PR and its state for a PR URL, or false
// when itervox does not manage it.
func (w *Watcher) managedFor(prURL string) (Managed, *prState, bool) {
	m, ok := w.managedByURL()[strings.ToLower(prURL)]
	if !ok {
		return Managed{}, nil, false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	st, ok := w.state(m)
	return m, st, ok
}

// urlForHead returns the managed PR whose last seen head is sha.
func (w *Watcher) urlForHead(sha string) string {
	if sha == "" {
		return ""
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, st := range w.prs {
		if !st.closed && st.headSHA == sha {
			return st.url
		}
	}
	return ""
}
