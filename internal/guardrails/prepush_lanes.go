package guardrails

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/gitexec"
)

// Lane limits: how many paths a request or report carries, and how many a
// message names before "and K more".
const (
	MaxLanePathsSent  = 50
	MaxLanePathsShown = 10
)

// LanePathSummary names up to n of paths ("`a`, `b` and 3 more"); total is
// how many there were in all (at least len(paths)).
func LanePathSummary(paths []string, total, n int) string {
	total = max(total, len(paths))
	shown := paths[:min(len(paths), max(n, 1))]
	quoted := make([]string, len(shown))
	for i, p := range shown {
		quoted[i] = "`" + p + "`"
	}
	s := strings.Join(quoted, ", ")
	if more := total - len(shown); more > 0 {
		s += fmt.Sprintf(" and %d more", more)
	}
	return s
}

// laneRepo answers the questions lane classification asks of the pushing
// repository.
type laneRepo interface {
	// MergeBase is the merge base of a and b; false when either is unknown.
	MergeBase(a, b string) (string, bool)
	// ChangedPaths lists the paths that differ between commits from and to
	// (renames as a deletion plus an addition).
	ChangedPaths(from, to string) ([]string, error)
}

func (g gitRepo) MergeBase(a, b string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := gitexec.Command(ctx, g.dir, "merge-base", a, b).Output()
	if err != nil {
		return "", false
	}
	mb := strings.TrimSpace(string(out))
	return mb, mb != ""
}

func (g gitRepo) ChangedPaths(from, to string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	// --no-renames: a moved workflow file is a deletion of its old path
	// plus an addition, and both are classified.
	out, err := gitexec.Command(ctx, g.dir, "diff", "--name-only", "-z", "--no-renames", "--no-ext-diff", from, to, "--").Output()
	if err != nil {
		return nil, fmt.Errorf("git diff %s %s: %w", shortSHA(from), shortSHA(to), err)
	}
	var paths []string
	for _, p := range bytes.Split(out, []byte{0}) {
		if len(p) > 0 {
			paths = append(paths, string(p))
		}
	}
	return paths, nil
}

func shortSHA(sha string) string { return sha[:min(len(sha), 12)] }

// pushedPaths lists the files one ref update changes that the issue's pull
// request will carry:
//
//   - an existing remote ref: the files that differ between the remote tip
//     and the pushed commit and that also differ from the base (merging the
//     base branch in does not count as changing what the base changed);
//   - a new branch, or a remote tip this repository does not have: the
//     files that differ between the pushed commit and its merge base with
//     the issue's base (the first of BaseRefs that has one).
func (p Policy) pushedPaths(u RefUpdate, repo laneRepo) ([]string, error) {
	base := ""
	for _, ref := range p.BaseRefs {
		if mb, ok := repo.MergeBase(ref, u.LocalSHA); ok {
			base = mb
			break
		}
	}
	if !zeroSHA(u.RemoteSHA) {
		if changed, err := repo.ChangedPaths(u.RemoteSHA, u.LocalSHA); err == nil {
			if base == "" {
				return changed, nil
			}
			vsBase, err := repo.ChangedPaths(base, u.LocalSHA)
			if err != nil {
				return changed, nil
			}
			return slices.DeleteFunc(changed, func(f string) bool { return !slices.Contains(vsBase, f) }), nil
		}
	}
	if base == "" {
		return nil, fmt.Errorf("no base branch to compare %s with (tried %s)", u.RemoteRef, strings.Join(p.BaseRefs, ", "))
	}
	return repo.ChangedPaths(base, u.LocalSHA)
}

// errUnknownChanges is the stand-in path of a push whose changed files
// could not be listed: it is held for a human (fail closed).
var errUnknownChanges = errors.New("changed files unknown")

// classifyPush puts the files every ref update changes into lanes. A ref
// whose files cannot be listed counts as human, with a stand-in path that
// says why.
func (p Policy) classifyPush(updates []RefUpdate, repo laneRepo) LaneResult {
	var paths []string
	unknown := ""
	for _, u := range updates {
		if zeroSHA(u.LocalSHA) || !strings.HasPrefix(u.RemoteRef, "refs/heads/") {
			continue // a deletion changes no file; tags were already refused
		}
		got, err := p.pushedPaths(u, repo)
		if err != nil {
			unknown = fmt.Sprintf("(%v for %s: %v)", errUnknownChanges, u.RemoteRef, err)
			continue
		}
		paths = append(paths, got...)
	}
	r := p.Lanes.Classify(paths)
	if unknown != "" {
		r.Human = append([]string{unknown}, r.Human...)
		r.Lane = LaneHuman
	}
	return r
}

// laneKey identifies a held push: the same refs and commits join the
// pending request, new commits ask again.
func laneKey(dir string, updates []RefUpdate) string {
	args := make([]string, 0, len(updates))
	for _, u := range updates {
		args = append(args, u.RemoteRef+"="+u.LocalSHA)
	}
	return commandKey(dir, "git push (lane)", args)
}

// holdHumanLane waits for a human to approve a push that changes human-lane
// files. ok means approved, once; otherwise code is the hook's exit status.
func holdHumanLane(p Policy, dir, remote string, updates []RefUpdate, r LaneResult, stderr io.Writer) (code int, ok bool) {
	refs := make([]string, 0, len(updates))
	for _, u := range updates {
		refs = append(refs, u.RemoteRef)
	}
	display := displayCommand("git", append([]string{"push", remote}, refs...))
	sent := r.Human[:min(len(r.Human), MaxLanePathsSent)]
	return waitForApproval(p, approvalAsk{
		req: ApprovalRequest{Key: laneKey(dir, updates), Command: display, Lane: LaneHuman,
			Paths: sent, PathsTotal: len(r.Human)},
		why: "(guardrails.lanes.human: it changes " + LanePathSummary(sent, len(r.Human), MaxLanePathsShown) +
			"; an agent may change what CI checks, never how CI checks)",
		unavailableKind: KindLaneHuman,
		unavailableHint: "Ask the user to review and push these changes themselves, or leave them out of your push.",
		retryHint:       "If this times out, push the same commits again later: an approval is kept for them and lets one push through.",
	}, stderr)
}

// reportLanes tells the daemon which lane the push reached, so merge
// policy knows. A failed report refuses the push: merge policy must never
// miss a lane.
func reportLanes(p Policy, r LaneResult, stderr io.Writer) int {
	c := newClient(p)
	if !c.enabled() || !IsLane(r.Lane) {
		return 0 // no daemon (itervox run): no merge policy to inform
	}
	paths := r.Paths()
	err := c.reportLane(LaneReport{Lane: r.Lane, Paths: paths[:min(len(paths), MaxLanePathsSent)], PathsTotal: len(paths)})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "guardrails: this push changes %s files (%s), and Itervox could not record that for its merge policy: %v. Push again in a moment.\n",
			r.Lane, LanePathSummary(paths, len(paths), MaxLanePathsShown), err)
		return 1
	}
	return 0
}
