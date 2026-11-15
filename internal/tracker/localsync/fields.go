package localsync

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
	"github.com/vnovick/itervox/internal/tracker/local"
)

// The synced fields. Each is compared in one shared form (values), hashed,
// and the hash recorded in the ledger as the field's last-synced value: a
// side whose hash differs from it was edited since.
const (
	fieldTitle       = "title"
	fieldDescription = "description"
	fieldState       = "state"
	fieldLabels      = "labels"
	fieldPriority    = "priority"
	fieldBlockedBy   = "blocked_by"
)

// syncedFields is every field, in the order conflicts are reported.
var syncedFields = []string{fieldTitle, fieldDescription, fieldState, fieldLabels, fieldPriority, fieldBlockedBy}

// values is one issue's fields in the shared form: local state names,
// local priority convention (1 urgent … 4 low, nil none), labels as
// written, blockers as local identifiers of linked issues.
type values struct {
	Title       string
	Description string
	State       string
	Labels      []string
	Priority    *int
	BlockedBy   []string
	// known reports, per field, whether the side reported it. A GitHub
	// issue with no state label has no state; it is never synced as "".
	known map[string]bool
}

// canonical is a field's comparable text.
func (v values) canonical(field string) string {
	switch field {
	case fieldTitle:
		return strings.TrimSpace(v.Title)
	case fieldDescription:
		return strings.TrimSpace(strings.ReplaceAll(v.Description, "\r\n", "\n"))
	case fieldState:
		return strings.ToLower(strings.TrimSpace(v.State))
	case fieldLabels:
		return strings.Join(normLabels(v.Labels), "\n")
	case fieldPriority:
		if v.Priority == nil {
			return ""
		}
		return strconv.Itoa(*v.Priority)
	case fieldBlockedBy:
		b := slices.Clone(v.BlockedBy)
		slices.Sort(b)
		return strings.Join(slices.Compact(b), "\n")
	}
	return ""
}

// hash is the ledger's record of a field's value.
func (v values) hash(field string) string {
	return hashText(field + "\x00" + v.canonical(field))
}

func hashText(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:12])
}

// display is a field's value for a conflict comment, shortened.
func (v values) display(field string) string {
	s := v.canonical(field)
	switch field {
	case fieldState:
		s = v.State
	case fieldLabels, fieldBlockedBy:
		s = strings.ReplaceAll(s, "\n", ", ")
	}
	if s == "" {
		return "(empty)"
	}
	if r := []rune(s); len(r) > 200 {
		s = string(r[:200]) + "…"
	}
	return s
}

func normLabels(labels []string) []string {
	out := make([]string, 0, len(labels))
	for _, l := range labels {
		if l = strings.ToLower(strings.TrimSpace(l)); l != "" {
			out = append(out, l)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// localValues reads a local issue. Blockers keep only linked issues: an
// unlinked blocker has no remote counterpart to compare with.
func localValues(si local.SyncIssue, linked func(localID string) bool) values {
	v := values{Title: si.Title, Description: si.Description, State: si.State, Labels: si.Labels,
		Priority: si.Priority, known: allKnown()}
	for _, b := range si.BlockedBy {
		if linked(b) {
			v.BlockedBy = append(v.BlockedBy, b)
		}
	}
	return v
}

func allKnown() map[string]bool {
	k := map[string]bool{}
	for _, f := range syncedFields {
		k[f] = true
	}
	return k
}

// dialect converts between a remote tracker's conventions and the shared
// form.
type dialect struct {
	kind string // "linear" or "github"
	// toRemoteState maps local state names to remote ones (tracker.sync.
	// state_map); names not in it are the same on both sides.
	toRemoteState map[string]string
	// stateLabels are the remote state names; on GitHub they are labels
	// that are the issue's state, not its labels.
	stateLabels []string
}

func (d dialect) remoteState(localState string) string {
	for l, r := range d.toRemoteState {
		if strings.EqualFold(strings.TrimSpace(l), strings.TrimSpace(localState)) {
			return r
		}
	}
	return localState
}

func (d dialect) localState(remoteState string) string {
	for l, r := range d.toRemoteState {
		if strings.EqualFold(strings.TrimSpace(r), strings.TrimSpace(remoteState)) {
			return l
		}
	}
	return remoteState
}

// remoteValues reads a remote issue into the shared form. localOf maps a
// remote issue ID to its linked local identifier.
func (d dialect) remoteValues(is domain.Issue, localOf func(remoteID string) (string, bool)) values {
	v := values{Title: is.Title, known: allKnown()}
	if is.Description != nil {
		v.Description, _ = tracker.SplitSyncBlockersLine(*is.Description)
	}
	if s := strings.TrimSpace(is.State); s != "" {
		v.State = d.localState(s)
	} else {
		v.known[fieldState] = false
	}
	for _, l := range is.Labels {
		if d.kind == "github" && (d.isStateLabel(l) || isGitHubPriorityLabel(l)) {
			continue
		}
		v.Labels = append(v.Labels, l)
	}
	if is.Priority != nil {
		switch p := *is.Priority; {
		case d.kind == "github":
			lp := p + 1
			v.Priority = &lp
		case p > 0:
			v.Priority = &p
		}
	}
	for _, b := range is.BlockedBy {
		if b.Origin == domain.BlockerOriginSubIssue || b.ID == nil {
			continue
		}
		if l, ok := localOf(*b.ID); ok {
			v.BlockedBy = append(v.BlockedBy, l)
		}
	}
	return v
}

func (d dialect) isStateLabel(l string) bool {
	return slices.ContainsFunc(d.stateLabels, func(s string) bool { return strings.EqualFold(strings.TrimSpace(s), strings.TrimSpace(l)) })
}

func isGitHubPriorityLabel(l string) bool {
	switch strings.ToLower(strings.TrimSpace(l)) {
	case "p0", "p1", "p2", "p3":
		return true
	}
	return false
}

// remotePriority converts a local priority to the patch convention (-1
// clears it).
func (d dialect) remotePriority(p *int) int {
	if p == nil {
		return -1
	}
	if d.kind == "github" {
		return min(max(*p-1, 0), 3)
	}
	return min(max(*p, 0), 4)
}

// remoteIdentifier is what the local file records as `remote:`.
func remoteIdentifier(is domain.Issue) string {
	return is.Identifier
}

// timeOr returns *t, or def when t is nil.
func timeOr(t *time.Time, def time.Time) time.Time {
	if t == nil {
		return def
	}
	return *t
}

// applyRemote sets field f of edit to the remote value. A blockers change
// keeps the file's unlinked blockers, which the remote cannot show.
func (c *cycle) applyRemote(edit *local.SyncEdit, f string, rv values, si local.SyncIssue) {
	switch f {
	case fieldTitle:
		edit.Title = &rv.Title
	case fieldDescription:
		edit.Description = &rv.Description
	case fieldState:
		edit.State = &rv.State
	case fieldLabels:
		labels := slices.Clone(rv.Labels)
		edit.Labels = &labels
	case fieldPriority:
		if rv.Priority == nil {
			edit.ClearPriority = true
		} else {
			edit.Priority = rv.Priority
		}
	case fieldBlockedBy:
		list := slices.Clone(rv.BlockedBy)
		for _, b := range si.BlockedBy {
			if !c.s.linked(b) {
				list = append(list, b)
			}
		}
		edit.BlockedBy = &list
	}
}

// addToPatch sets field f of patch to the local value.
func (c *cycle) addToPatch(p *tracker.IssueFieldsPatch, f string, lv values, si local.SyncIssue) {
	switch f {
	case fieldTitle:
		p.Title = &lv.Title
	case fieldDescription:
		p.Description = &lv.Description
	case fieldLabels:
		labels := slices.Clone(si.Labels)
		if labels == nil {
			labels = []string{}
		}
		p.Labels = &labels
	case fieldPriority:
		pr := c.s.dl.remotePriority(lv.Priority)
		p.Priority = &pr
	case fieldBlockedBy:
		ids := c.s.remoteIDs(lv.BlockedBy)
		p.BlockedBy = &ids
	}
}

// conflictNote is the comment recorded in the file when both sides edited
// a field since the last sync.
func conflictNote(field, kept string, won, lost values, wonAt, lostAt time.Time) string {
	other := "local"
	if kept == "local" {
		other = "remote"
	}
	return fmt.Sprintf("Sync conflict on `%s`: it was edited on both sides. Kept the %s edit (%s): %s\n\nReplaced the %s edit (%s): %s",
		field, kept, wonAt.UTC().Format(time.RFC3339), won.display(field),
		other, lostAt.UTC().Format(time.RFC3339), lost.display(field))
}
