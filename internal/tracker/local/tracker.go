// Package local is the file-based tracker (#85): one Markdown file per
// issue in a directory (by default <project>/.itervox/issues/), editable by
// hand, from the dashboard, or by agents through the agent actions.
//
// The directory is the source of truth. Every call rescans it, re-reading
// only files whose size or modification time changed, so edits made in an
// editor are picked up on the next poll without a restart. A file that does
// not parse is reported (Problems, and a log line when it changes) and its
// last good version keeps being served; it is never fatal.
package local

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vnovick/itervox/internal/atomicfs"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

// DefaultPrefix is the identifier prefix for new issues (ITX-1, ITX-2, …).
const DefaultPrefix = "ITX"

// ItervoxAuthor is the author name of comments Itervox writes.
const ItervoxAuthor = "Itervox"

// identifierRe is what an issue file name (without .md) must look like.
var identifierRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*-[0-9]+$`)

// Config configures a Tracker.
type Config struct {
	Dir            string // the issues directory
	Prefix         string // identifier prefix for CreateIssue; DefaultPrefix when empty
	ActiveStates   []string
	TerminalStates []string
}

// Problem is a file that could not be read.
type Problem struct {
	Path string
	Err  string
}

// Tracker is the local file tracker.
type Tracker struct {
	dir    string
	prefix string
	now    func() time.Time

	mu       sync.Mutex
	active   []string
	terminal []string
	entries  map[string]*entry // identifier → entry
}

type entry struct {
	path    string
	file    issueFile
	good    bool // file holds a successfully parsed version
	stamp   stamp
	problem string
}

type stamp struct {
	mod  time.Time
	size int64
}

// New returns a tracker over cfg.Dir. The directory is created on the first
// write; a missing directory reads as no issues.
func New(cfg Config) *Tracker {
	prefix := strings.TrimSpace(cfg.Prefix)
	if prefix == "" {
		prefix = DefaultPrefix
	}
	return &Tracker{dir: cfg.Dir, prefix: prefix, now: time.Now,
		active: append([]string{}, cfg.ActiveStates...), terminal: append([]string{}, cfg.TerminalStates...),
		entries: map[string]*entry{}}
}

var (
	_ tracker.Tracker             = (*Tracker)(nil)
	_ tracker.DetailBatcher       = (*Tracker)(nil)
	_ tracker.IdempotentCommenter = (*Tracker)(nil)
	_ tracker.StateListSetter     = (*Tracker)(nil)
)

// Dir is the issues directory.
func (t *Tracker) Dir() string { return t.dir }

// SetStateLists replaces the active and terminal states (dashboard settings).
func (t *Tracker) SetStateLists(active, terminal []string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.active = append([]string{}, active...)
	t.terminal = append([]string{}, terminal...)
}

// Problems lists the files that currently fail to parse.
func (t *Tracker) Problems() []Problem {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.refreshLocked()
	var out []Problem
	for _, e := range t.entries {
		if e.problem != "" {
			out = append(out, Problem{Path: e.path, Err: e.problem})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// refreshLocked re-reads changed files and drops deleted ones.
func (t *Tracker) refreshLocked() {
	dirEntries, err := os.ReadDir(t.dir)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("local tracker: cannot read the issues directory", "dir", t.dir, "error", err)
		}
		t.entries = map[string]*entry{}
		return
	}
	seen := map[string]bool{}
	for _, de := range dirEntries {
		name := de.Name()
		if de.IsDir() || strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".md") {
			continue // includes atomicfs temp files (.name.tmp-*)
		}
		ident := strings.TrimSuffix(name, ".md")
		if !identifierRe.MatchString(ident) {
			continue
		}
		path := filepath.Join(t.dir, name)
		info, err := de.Info()
		if err != nil {
			continue
		}
		seen[ident] = true
		st := stamp{mod: info.ModTime(), size: info.Size()}
		e := t.entries[ident]
		if e != nil && e.stamp == st {
			continue
		}
		if e == nil {
			e = &entry{path: path}
			t.entries[ident] = e
		}
		e.stamp = st
		data, err := os.ReadFile(path) //nolint:gosec // a file in the configured issues directory
		if err == nil {
			var f issueFile
			if f, err = parseIssueFile(data); err == nil {
				e.file, e.good, e.problem = f, true, ""
				continue
			}
		}
		msg := err.Error()
		if msg != e.problem {
			slog.Warn("local tracker: cannot read an issue file; serving its last good version, if any",
				"path", path, "error", msg)
		}
		e.problem = msg
	}
	for ident := range t.entries {
		if !seen[ident] {
			delete(t.entries, ident)
		}
	}
}

// issueLocked builds the domain issue for ident, resolving blocker states
// and branches from the other files. A blocker with no file has no state,
// which keeps the dependent blocked (fail-safe).
func (t *Tracker) issueLocked(ident string) (domain.Issue, bool) {
	e := t.entries[ident]
	if e == nil || !e.good {
		return domain.Issue{}, false
	}
	is := e.file.toIssue(ident)
	for _, b := range e.file.BlockedBy {
		id := b
		ref := domain.BlockerRef{ID: &id, Identifier: &id}
		if be := t.entries[b]; be != nil && be.good {
			st := be.file.State
			ref.State = &st
			if be.file.Branch != "" {
				br := be.file.Branch
				ref.BranchName = &br
			}
		}
		is.BlockedBy = append(is.BlockedBy, ref)
	}
	return is, true
}

// sortedIdentifiersLocked orders issues by prefix, then number.
func (t *Tracker) sortedIdentifiersLocked() []string {
	ids := make([]string, 0, len(t.entries))
	for id, e := range t.entries {
		if e.good {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool {
		pi, ni := splitIdent(ids[i])
		pj, nj := splitIdent(ids[j])
		if pi != pj {
			return pi < pj
		}
		return ni < nj
	})
	return ids
}

func splitIdent(id string) (string, int) {
	i := strings.LastIndex(id, "-")
	n, _ := strconv.Atoi(id[i+1:])
	return strings.ToUpper(id[:i]), n
}

func matchesState(state string, states []string) bool {
	for _, s := range states {
		if strings.EqualFold(strings.TrimSpace(s), strings.TrimSpace(state)) {
			return true
		}
	}
	return false
}

func (t *Tracker) issuesWhere(keep func(domain.Issue) bool) []domain.Issue {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.refreshLocked()
	var out []domain.Issue
	for _, id := range t.sortedIdentifiersLocked() {
		if is, ok := t.issueLocked(id); ok && keep(is) {
			out = append(out, is)
		}
	}
	return out
}

// FetchCandidateIssues returns the issues in an active state.
func (t *Tracker) FetchCandidateIssues(_ context.Context) ([]domain.Issue, error) {
	t.mu.Lock()
	active := append([]string{}, t.active...)
	t.mu.Unlock()
	return t.issuesWhere(func(is domain.Issue) bool { return matchesState(is.State, active) }), nil
}

// FetchIssuesByStates returns the issues in any of stateNames.
func (t *Tracker) FetchIssuesByStates(_ context.Context, stateNames []string) ([]domain.Issue, error) {
	if len(stateNames) == 0 {
		return []domain.Issue{}, nil
	}
	return t.issuesWhere(func(is domain.Issue) bool { return matchesState(is.State, stateNames) }), nil
}

// FetchIssueStatesByIDs returns the issues with the given IDs; unknown IDs
// are left out.
func (t *Tracker) FetchIssueStatesByIDs(_ context.Context, issueIDs []string) ([]domain.Issue, error) {
	if len(issueIDs) == 0 {
		return []domain.Issue{}, nil
	}
	want := map[string]bool{}
	for _, id := range issueIDs {
		want[id] = true
	}
	return t.issuesWhere(func(is domain.Issue) bool { return want[is.ID] }), nil
}

// FetchIssueDetailsByIDs is FetchIssueStatesByIDs: comments are always
// included.
func (t *Tracker) FetchIssueDetailsByIDs(ctx context.Context, issueIDs []string) ([]domain.Issue, error) {
	return t.FetchIssueStatesByIDs(ctx, issueIDs)
}

// FetchIssueDetail returns one issue with its comments.
func (t *Tracker) FetchIssueDetail(_ context.Context, issueID string) (*domain.Issue, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.refreshLocked()
	is, ok := t.issueLocked(issueID)
	if !ok {
		return nil, &tracker.NotFoundError{Adapter: "local", Identifier: issueID}
	}
	return &is, nil
}

// FetchIssueByIdentifier returns one issue; the ID is the identifier.
func (t *Tracker) FetchIssueByIdentifier(ctx context.Context, identifier string) (*domain.Issue, error) {
	return t.FetchIssueDetail(ctx, identifier)
}

// update applies change to issueID's file and writes it.
func (t *Tracker) update(issueID string, change func(f *issueFile)) (issueFile, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.refreshLocked()
	e := t.entries[issueID]
	if e == nil || !e.good {
		return issueFile{}, &tracker.NotFoundError{Adapter: "local", Identifier: issueID}
	}
	if e.problem != "" {
		return issueFile{}, fmt.Errorf("local tracker: %s does not parse (%s); fix it before Itervox writes to it", e.path, e.problem)
	}
	f := e.file
	f.Labels = append([]string{}, f.Labels...)
	f.BlockedBy = append([]string{}, f.BlockedBy...)
	f.Comments = append([]fileComment{}, f.Comments...)
	change(&f)
	now := t.now().UTC().Truncate(time.Second)
	f.Updated = &now
	if err := t.writeLocked(e, f); err != nil {
		return issueFile{}, err
	}
	return f, nil
}

func (t *Tracker) writeLocked(e *entry, f issueFile) error {
	if err := os.MkdirAll(t.dir, 0o755); err != nil {
		return fmt.Errorf("local tracker: %w", err)
	}
	if err := atomicfs.WriteFile(e.path, f.render(), 0o644); err != nil {
		return fmt.Errorf("local tracker: write %s: %w", e.path, err)
	}
	e.file, e.good, e.problem = f, true, ""
	if info, err := os.Stat(e.path); err == nil {
		e.stamp = stamp{mod: info.ModTime(), size: info.Size()}
	}
	return nil
}

// UpdateIssueState sets the issue's state.
func (t *Tracker) UpdateIssueState(_ context.Context, issueID, stateName string) error {
	_, err := t.update(issueID, func(f *issueFile) { f.State = stateName })
	return err
}

// SetIssueBranch records the issue's branch.
func (t *Tracker) SetIssueBranch(_ context.Context, issueID, branchName string) error {
	_, err := t.update(issueID, func(f *issueFile) { f.Branch = branchName })
	return err
}

// CreateComment appends a comment by Itervox.
func (t *Tracker) CreateComment(_ context.Context, issueID, body string) (*domain.Comment, error) {
	at := t.now().UTC().Truncate(time.Second)
	f, err := t.update(issueID, func(f *issueFile) {
		f.Comments = append(f.Comments, fileComment{At: at, Author: ItervoxAuthor, Body: strings.TrimSpace(body)})
	})
	if err != nil {
		return nil, err
	}
	return &domain.Comment{ID: fmt.Sprintf("%s-c%d", issueID, len(f.Comments)), Body: strings.TrimSpace(body),
		CreatedAt: &at, AuthorID: ItervoxAuthor, AuthorName: ItervoxAuthor}, nil
}

// CreateCommentWithKey appends a comment carrying an idempotency key.
func (t *Tracker) CreateCommentWithKey(ctx context.Context, issueID, key, body string) (*domain.Comment, error) {
	return t.CreateComment(ctx, issueID, tracker.MarkCommentKey(body, key))
}

// FindCommentByKey reports whether a comment with key exists on the issue.
func (t *Tracker) FindCommentByKey(ctx context.Context, issueID, key string) (*domain.Comment, bool, error) {
	is, err := t.FetchIssueDetail(ctx, issueID)
	if err != nil {
		return nil, false, err
	}
	for i := range is.Comments {
		if tracker.CommentHasKey(is.Comments[i].Body, key) {
			c := is.Comments[i]
			return &c, true, nil
		}
	}
	return nil, false, nil
}

// CreateIssue writes a new issue file with the next free number.
func (t *Tracker) CreateIssue(_ context.Context, _ string, title, body, stateName string) (*domain.Issue, error) {
	if strings.TrimSpace(title) == "" {
		return nil, fmt.Errorf("local tracker: an issue needs a title")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.refreshLocked()
	next := 1
	for id := range t.entries {
		if p, n := splitIdent(id); strings.EqualFold(p, t.prefix) && n >= next {
			next = n + 1
		}
	}
	ident := fmt.Sprintf("%s-%d", t.prefix, next)
	now := t.now().UTC().Truncate(time.Second)
	f := issueFile{Title: strings.TrimSpace(title), State: stateName, Body: strings.TrimSpace(body),
		Created: &now, Updated: &now, Extra: map[string]any{}}
	e := &entry{path: filepath.Join(t.dir, ident+".md")}
	if _, err := os.Stat(e.path); err == nil {
		return nil, fmt.Errorf("local tracker: %s already exists", e.path)
	}
	if err := t.writeLocked(e, f); err != nil {
		return nil, err
	}
	t.entries[ident] = e
	is, _ := t.issueLocked(ident)
	return &is, nil
}

// WriteIssue writes a new issue file for identifier (used to seed examples
// and the demo). It refuses to overwrite.
func WriteIssue(dir, identifier string, f IssueSpec) error {
	if !identifierRe.MatchString(identifier) {
		return fmt.Errorf("local tracker: %q is not an issue identifier (like ITX-1)", identifier)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, identifier+".md")
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("local tracker: %s already exists", path)
	}
	file := issueFile{Title: f.Title, State: f.State, Priority: f.Priority, Labels: f.Labels,
		BlockedBy: f.BlockedBy, Body: f.Body, Created: f.Created, Updated: f.Created, Extra: map[string]any{}}
	return atomicfs.WriteFile(path, file.render(), 0o644)
}

// IssueSpec describes an issue for WriteIssue.
type IssueSpec struct {
	Title     string
	State     string
	Priority  *int
	Labels    []string
	BlockedBy []string
	Body      string
	Created   *time.Time
}
