package local

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// The surface the local issue sync (#118, internal/tracker/localsync) reads
// and writes the files through. Its writes are Itervox's own: they record
// the file's new stamp, so Watch does not report them as edits, and they
// re-read the file first, so a hand edit made since the last scan is never
// overwritten.

// SyncIssue is one issue file as the sync sees it.
type SyncIssue struct {
	Identifier  string
	Title       string
	State       string
	Description string
	Priority    *int
	Labels      []string // as written in the file
	BlockedBy   []string
	Comments    []SyncComment
	// Remote is the `remote:` front matter key: the linked remote issue's
	// identifier, empty when the issue is not linked.
	Remote string
	// ModTime is the file's modification time: when it was last written,
	// by a person or by Itervox.
	ModTime time.Time
}

// SyncComment is one comment in an issue file.
type SyncComment struct {
	At     time.Time
	Author string
	Body   string
}

// SyncIssues returns every issue whose file parses now, sorted by
// identifier. A file that does not parse is left out, even when an earlier
// version did: the sync never reads or writes a malformed file, as Itervox
// never writes one for dispatch either.
func (t *Tracker) SyncIssues() ([]SyncIssue, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.refreshLocked(); err != nil {
		return nil, err
	}
	var out []SyncIssue
	for _, id := range t.sortedIdentifiersLocked(func(string, string) bool { return true }) {
		e := t.entries[id]
		if e.problem != "" {
			continue
		}
		out = append(out, e.file.syncIssue(id, e.stamp.mod))
	}
	return out, nil
}

func (f issueFile) syncIssue(ident string, mod time.Time) SyncIssue {
	si := SyncIssue{Identifier: ident, Title: f.Title, State: f.State, Description: f.Body,
		Priority: f.Priority, Labels: append([]string{}, f.Labels...), BlockedBy: append([]string{}, f.BlockedBy...),
		Remote: f.Remote, ModTime: mod}
	for _, c := range f.Comments {
		si.Comments = append(si.Comments, SyncComment(c))
	}
	return si
}

// SyncEdit is a change the sync applies to one issue file. A nil field is
// left as it is; Comments are added in time order among the existing ones.
type SyncEdit struct {
	Title       *string
	State       *string
	Description *string
	// Priority sets the priority; ClearPriority removes it.
	Priority      *int
	ClearPriority bool
	Labels        *[]string
	BlockedBy     *[]string
	Remote        *string
	Comments      []SyncComment
}

// ApplySync applies edit to the issue's file and returns the issue as
// written.
func (t *Tracker) ApplySync(identifier string, edit SyncEdit) (SyncIssue, error) {
	f, err := t.update(identifier, func(f *issueFile) {
		if edit.Title != nil && strings.TrimSpace(*edit.Title) != "" {
			f.Title = strings.TrimSpace(*edit.Title)
		}
		if edit.State != nil && strings.TrimSpace(*edit.State) != "" {
			f.State = strings.TrimSpace(*edit.State)
		}
		if edit.Description != nil {
			f.Body = strings.TrimSpace(*edit.Description)
		}
		switch {
		case edit.ClearPriority:
			f.Priority = nil
		case edit.Priority != nil:
			p := *edit.Priority
			f.Priority = &p
		}
		if edit.Labels != nil {
			f.Labels = append([]string{}, (*edit.Labels)...)
		}
		if edit.BlockedBy != nil {
			f.BlockedBy = append([]string{}, (*edit.BlockedBy)...)
		}
		if edit.Remote != nil {
			f.Remote = strings.TrimSpace(*edit.Remote)
		}
		for _, c := range edit.Comments {
			f.Comments = append(f.Comments, fileComment{At: c.At.UTC().Truncate(time.Second), Author: c.Author, Body: strings.TrimSpace(c.Body)})
		}
		if len(edit.Comments) > 0 {
			sort.SliceStable(f.Comments, func(i, j int) bool { return f.Comments[i].At.Before(f.Comments[j].At) })
		}
	})
	if err != nil {
		return SyncIssue{}, err
	}
	return f.syncIssue(identifier, t.modTime(identifier)), nil
}

// CreateSyncIssue writes a new issue file for an issue pulled from the
// remote tracker, with the next free number for the prefix, and returns it
// as written. si.Identifier and si.ModTime are ignored.
func (t *Tracker) CreateSyncIssue(si SyncIssue) (SyncIssue, error) {
	if strings.TrimSpace(si.Title) == "" || strings.TrimSpace(si.State) == "" {
		return SyncIssue{}, fmt.Errorf("local tracker: an issue needs a title and a state")
	}
	f := issueFile{Title: strings.TrimSpace(si.Title), State: strings.TrimSpace(si.State), Body: strings.TrimSpace(si.Description),
		Priority: si.Priority, Labels: si.Labels, BlockedBy: si.BlockedBy, Remote: strings.TrimSpace(si.Remote)}
	for _, c := range si.Comments {
		f.Comments = append(f.Comments, fileComment{At: c.At.UTC().Truncate(time.Second), Author: c.Author, Body: strings.TrimSpace(c.Body)})
	}
	sort.SliceStable(f.Comments, func(i, j int) bool { return f.Comments[i].At.Before(f.Comments[j].At) })
	t.mu.Lock()
	ident, err := t.createLocked(f)
	var out SyncIssue
	if err == nil {
		e := t.entries[ident]
		out = e.file.syncIssue(ident, e.stamp.mod)
	}
	t.mu.Unlock()
	return out, err
}

func (t *Tracker) modTime(identifier string) time.Time {
	t.mu.Lock()
	defer t.mu.Unlock()
	if e := t.entries[identifier]; e != nil {
		return e.stamp.mod
	}
	return time.Time{}
}
