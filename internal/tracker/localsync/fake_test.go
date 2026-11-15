package localsync

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

// fakeRemote is an in-memory remote tracker in either Linear's or GitHub's
// dialect, close enough to each adapter's normalized output for the sync:
//
//   - linear: UUID-like ids, ENG-N identifiers, priority 0 (none) … 4,
//     states and labels separate, blockers as relations, the comment key as
//     the comment's id, issues created under a client key;
//   - github: number ids, #N identifiers, state and pN priority as labels,
//     blockers in the body ("Blocked by #3"), the comment key as a body
//     marker, no create key.
//
// Every write bumps the issue's UpdatedAt to the fake clock.
type fakeRemote struct {
	kind  string
	clock func() time.Time

	mu     sync.Mutex
	next   int
	issues map[string]*fakeIssue
	// writes counts every mutating call, for no-echo assertions.
	writes int
	// stateLabels is GitHub's state vocabulary (labels that are states).
	stateLabels []string
}

type fakeIssue struct {
	id, identifier string
	title, body    string
	state          string
	labels         []string // GitHub: includes state and pN labels
	priority       int      // Linear only
	blockers       []string // Linear only: blocker issue ids
	comments       []domain.Comment
	updated        time.Time
	created        time.Time
}

func newFakeRemote(kind string, clock func() time.Time) *fakeRemote {
	return &fakeRemote{kind: kind, clock: clock, issues: map[string]*fakeIssue{},
		stateLabels: []string{"Backlog", "Todo", "In Progress", "In Review", "Done"}}
}

var (
	_ tracker.Tracker            = (*fakeRemote)(nil)
	_ tracker.IssueEditor        = (*fakeRemote)(nil)
	_ tracker.IncrementalFetcher = (*fakeRemote)(nil)
)

// fakeLinear adds DetailBatcher, as the real Linear adapter has.
type fakeLinear struct{ *fakeRemote }

var _ tracker.DetailBatcher = fakeLinear{}

func (f fakeLinear) FetchIssueDetailsByIDs(ctx context.Context, ids []string) ([]domain.Issue, error) {
	return f.FetchIssueStatesByIDs(ctx, ids)
}

// --- seeding and human edits ---

// add creates an issue as a person would on the remote.
func (f *fakeRemote) add(title, body, state string) *fakeIssue {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.addLocked(title, body, state, "")
}

func (f *fakeRemote) addLocked(title, body, state, key string) *fakeIssue {
	f.next++
	now := f.clock()
	is := &fakeIssue{title: title, body: body, created: now, updated: now}
	if f.kind == "linear" {
		is.id = key
		if is.id == "" {
			is.id = fmt.Sprintf("lin-%04d", f.next)
		}
		is.identifier = fmt.Sprintf("ENG-%d", f.next)
		is.state = state
	} else {
		is.id = strconv.Itoa(f.next)
		is.identifier = "#" + is.id
		if state != "" {
			is.labels = append(is.labels, state)
		}
	}
	f.issues[is.id] = is
	return is
}

// edit applies a person's change at time at.
func (f *fakeRemote) edit(id string, at time.Time, change func(is *fakeIssue)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	is := f.issues[id]
	change(is)
	is.updated = at
}

func (f *fakeRemote) humanComment(id, author, body string, at time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	is := f.issues[id]
	t := at
	is.comments = append(is.comments, domain.Comment{ID: fmt.Sprintf("c-%s-%d", id, len(is.comments)+1), Body: body,
		CreatedAt: &t, AuthorName: author, AuthorID: author})
	is.updated = at
}

func (f *fakeRemote) get(id string) fakeIssue {
	f.mu.Lock()
	defer f.mu.Unlock()
	is := *f.issues[id]
	is.labels = slices.Clone(is.labels)
	is.comments = slices.Clone(is.comments)
	return is
}

func (f *fakeRemote) only() fakeIssue {
	f.mu.Lock()
	var id string
	for k := range f.issues {
		id = k
	}
	f.mu.Unlock()
	return f.get(id)
}

func (f *fakeRemote) writeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.writes
}

// --- normalization ---

func (f *fakeRemote) stateOf(is *fakeIssue) string {
	if f.kind == "linear" {
		return is.state
	}
	for _, l := range is.labels {
		for _, s := range f.stateLabels {
			if strings.EqualFold(l, s) {
				return s
			}
		}
	}
	return ""
}

var fakeBlockedRe = regexp.MustCompile(`(?i)blocked by ((?:#\d+(?:, )?)+)`)

func (f *fakeRemote) domainLocked(is *fakeIssue) domain.Issue {
	out := domain.Issue{ID: is.id, Identifier: is.identifier, Title: is.title, State: f.stateOf(is)}
	c, u := is.created, is.updated
	out.CreatedAt, out.UpdatedAt = &c, &u
	if is.body != "" {
		b := is.body
		out.Description = &b
	}
	for _, l := range is.labels {
		out.Labels = append(out.Labels, strings.ToLower(l))
	}
	if f.kind == "linear" {
		p := is.priority
		out.Priority = &p
		for _, b := range is.blockers {
			id := b
			out.BlockedBy = append(out.BlockedBy, domain.BlockerRef{ID: &id})
		}
	} else {
		for _, l := range out.Labels {
			if isGitHubPriorityLabel(l) {
				p, _ := strconv.Atoi(l[1:])
				out.Priority = &p
			}
		}
		if m := fakeBlockedRe.FindStringSubmatch(is.body); m != nil {
			for _, ref := range strings.Split(m[1], ",") {
				id := strings.TrimPrefix(strings.TrimSpace(ref), "#")
				if id != "" {
					out.BlockedBy = append(out.BlockedBy, domain.BlockerRef{ID: &id})
				}
			}
		}
	}
	out.Comments = slices.Clone(is.comments)
	return out
}

// --- tracker.Tracker ---

func (f *fakeRemote) FetchCandidateIssues(ctx context.Context) ([]domain.Issue, error) {
	return f.FetchIssuesByStates(ctx, []string{"Todo", "In Progress"})
}

func (f *fakeRemote) FetchIssuesByStates(_ context.Context, states []string) ([]domain.Issue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.Issue
	for _, is := range f.issues {
		if slices.ContainsFunc(states, func(s string) bool { return strings.EqualFold(s, f.stateOf(is)) }) {
			d := f.domainLocked(is)
			d.Comments = nil // list reads carry no comments
			out = append(out, d)
		}
	}
	return out, nil
}

func (f *fakeRemote) FetchIssueStatesByIDs(_ context.Context, ids []string) ([]domain.Issue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.Issue
	for _, id := range ids {
		if is := f.issues[id]; is != nil {
			out = append(out, f.domainLocked(is))
		}
	}
	return out, nil
}

func (f *fakeRemote) FetchIssuesUpdatedSince(_ context.Context, since time.Time) ([]domain.Issue, []string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.Issue
	for _, is := range f.issues {
		if !is.updated.Before(since) {
			d := f.domainLocked(is)
			d.Comments = nil
			out = append(out, d)
		}
	}
	return out, nil, nil
}

func (f *fakeRemote) FetchIssueDetail(_ context.Context, id string) (*domain.Issue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	is := f.issues[id]
	if is == nil {
		return nil, &tracker.NotFoundError{Adapter: f.kind, Identifier: id}
	}
	d := f.domainLocked(is)
	return &d, nil
}

func (f *fakeRemote) FetchIssueByIdentifier(ctx context.Context, identifier string) (*domain.Issue, error) {
	f.mu.Lock()
	var id string
	for _, is := range f.issues {
		if strings.EqualFold(is.identifier, identifier) {
			id = is.id
		}
	}
	f.mu.Unlock()
	return f.FetchIssueDetail(ctx, id)
}

func (f *fakeRemote) CreateComment(ctx context.Context, id, body string) (*domain.Comment, error) {
	return f.CreateCommentWithKey(ctx, id, "", body)
}

func (f *fakeRemote) CreateCommentWithKey(_ context.Context, id, key, body string) (*domain.Comment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	is := f.issues[id]
	if is == nil {
		return nil, &tracker.NotFoundError{Adapter: f.kind, Identifier: id}
	}
	f.writes++
	now := f.clock()
	c := domain.Comment{ID: fmt.Sprintf("c-%s-%d", id, len(is.comments)+1), Body: body, CreatedAt: &now, AuthorName: "itervox-bot"}
	if f.kind == "linear" && key != "" {
		c.ID = key
	} else {
		c.Body = tracker.MarkCommentKey(body, key)
	}
	is.comments = append(is.comments, c)
	is.updated = now
	return &c, nil
}

func (f *fakeRemote) FindCommentByKey(_ context.Context, id, key string) (*domain.Comment, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.issues[id].comments {
		if c.ID == key || tracker.CommentHasKey(c.Body, key) {
			return &c, true, nil
		}
	}
	return nil, false, nil
}

func (f *fakeRemote) CreateIssue(context.Context, string, tracker.NewIssue) (*domain.Issue, error) {
	return nil, fmt.Errorf("fake: CreateIssue is not what the sync uses")
}

func (f *fakeRemote) UpdateIssueState(_ context.Context, id, state string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	is := f.issues[id]
	if is == nil {
		return &tracker.NotFoundError{Adapter: f.kind, Identifier: id}
	}
	f.writes++
	if f.kind == "linear" {
		is.state = state
	} else {
		is.labels = slices.DeleteFunc(is.labels, func(l string) bool {
			return slices.ContainsFunc(f.stateLabels, func(s string) bool { return strings.EqualFold(s, l) })
		})
		is.labels = append(is.labels, state)
	}
	is.updated = f.clock()
	return nil
}

func (f *fakeRemote) SetIssueBranch(context.Context, string, string) error { return nil }

// --- tracker.IssueEditor ---

func (f *fakeRemote) CreateScopedIssue(_ context.Context, d tracker.IssueDraft) (*domain.Issue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes++
	key := ""
	if f.kind == "linear" {
		key = d.Key
	}
	is := f.addLocked(d.Title, d.Description, d.State, key)
	f.applyLocked(is, tracker.IssueFieldsPatch{Labels: &d.Labels, Priority: d.Priority, BlockedBy: &d.BlockedBy})
	out := f.domainLocked(is)
	return &out, nil
}

func (f *fakeRemote) UpdateIssueFields(_ context.Context, id string, p tracker.IssueFieldsPatch) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	is := f.issues[id]
	if is == nil {
		return &tracker.NotFoundError{Adapter: f.kind, Identifier: id}
	}
	f.writes++
	f.applyLocked(is, p)
	is.updated = f.clock()
	return nil
}

func (f *fakeRemote) applyLocked(is *fakeIssue, p tracker.IssueFieldsPatch) {
	if p.Title != nil {
		is.title = *p.Title
	}
	desc, line := tracker.SplitSyncBlockersLine(is.body)
	if p.Description != nil {
		desc = *p.Description
	}
	if f.kind == "linear" {
		is.body = desc
		if p.Priority != nil {
			is.priority = max(*p.Priority, 0)
		}
		if p.Labels != nil {
			is.labels = slices.Clone(*p.Labels)
		}
		if p.BlockedBy != nil {
			is.blockers = slices.Clone(*p.BlockedBy)
		}
		return
	}
	if p.BlockedBy != nil {
		refs := []string{}
		for _, b := range *p.BlockedBy {
			refs = append(refs, "#"+b)
		}
		line = tracker.SyncBlockersLine(refs)
	}
	is.body = tracker.JoinSyncBlockersLine(desc, line)
	var keep []string
	for _, l := range is.labels {
		isState := slices.ContainsFunc(f.stateLabels, func(s string) bool { return strings.EqualFold(s, l) })
		switch {
		case isState:
			keep = append(keep, l)
		case isGitHubPriorityLabel(l):
			if p.Priority == nil {
				keep = append(keep, l)
			}
		case p.Labels == nil:
			keep = append(keep, l)
		}
	}
	if p.Labels != nil {
		keep = append(keep, *p.Labels...)
	}
	if p.Priority != nil && *p.Priority >= 0 {
		keep = append(keep, "p"+strconv.Itoa(*p.Priority))
	}
	is.labels = keep
}

func (f *fakeRemote) FindIssueByKey(_ context.Context, key string) (*domain.Issue, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if is := f.issues[key]; is != nil && f.kind == "linear" {
		d := f.domainLocked(is)
		return &d, true, nil
	}
	return nil, false, nil
}
