package main

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/gitexec"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/server"
)

// hotspotGit runs one read-only git command in dir and returns its stdout.
type hotspotGit func(ctx context.Context, dir string, args ...string) ([]byte, error)

// gitexecHotspotGit is the production hotspotGit: every call goes through
// internal/gitexec (scrubbed environment, explicit dir) under
// hotspotsGitTimeout, with optional locks off so a read never contends with
// a worker's git.
func gitexecHotspotGit(ctx context.Context, dir string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, hotspotsGitTimeout)
	defer cancel()
	cmd := gitexec.Command(ctx, dir, args...)
	cmd.Env = append(cmd.Env, "LANG=C", "LC_ALL=C", "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	return cmd.Output()
}

var (
	// prNumberInURL reads the number off a GitHub pull or GitLab merge
	// request URL.
	prNumberInURL = regexp.MustCompile(`/(?:pull|pulls|merge_requests)/(\d+)(?:[/?#]|$)`)
	// revertedTitle is a `git revert` subject: Revert "<title>".
	revertedTitle = regexp.MustCompile(`^Revert "(.*)"`)
	// revertBranch is GitHub's revert-PR branch name: revert-<N>-<branch>.
	revertBranch = regexp.MustCompile(`\brevert-(\d+)-`)
	// revertsCommit is git revert's body line.
	revertsCommit = regexp.MustCompile(`(?i)this reverts commit ([0-9a-f]{7,40})`)
	// prRef is a "#<N>" reference.
	prRef = regexp.MustCompile(`#(\d+)\b`)
	// prSubject is how a PR lands on the base branch: a squash subject
	// ending "(#N)" or a merge commit "Merge pull request #N ...".
	prSubject = regexp.MustCompile(`\(#(\d+)\)\s*$|^Merge pull request #(\d+)\b`)
)

// prNumber is the PR number in a PR URL; 0 when there is none.
func prNumber(url string) int {
	m := prNumberInURL.FindStringSubmatch(url)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// safeRefPart rejects branch names that could be read as an option or a
// revision range.
func safeRefPart(b string) bool {
	return b != "" && !strings.HasPrefix(b, "-") && !strings.Contains(b, "..") &&
		!strings.ContainsAny(b, " \t\n~^:?*[\\")
}

// splitNUL splits `git ... -z` output.
func splitNUL(out []byte) []string {
	var files []string
	for _, f := range bytes.Split(out, []byte{0}) {
		if len(f) > 0 {
			files = append(files, string(f))
		}
	}
	return files
}

// resolveBaseRef is the base branch as origin has it, else the local branch.
func resolveBaseRef(ctx context.Context, git hotspotGit, src hotspotSource) string {
	if !safeRefPart(src.base) {
		return ""
	}
	for _, ref := range []string{"refs/remotes/origin/" + src.base, "refs/heads/" + src.base} {
		if _, err := git(ctx, src.repo, "rev-parse", "--verify", "--quiet", ref+"^{commit}"); err == nil {
			return ref
		}
	}
	return ""
}

// revertedPRs scans the newest hotspotsRevertScanDepth commits on the base
// for reverts and returns the PR numbers they undid: numbers in a reverted
// title (Revert "Add x (#12)"), GitHub revert-PR branches (revert-12-...),
// and the PR numbers in the subjects of the commits a revert names
// ("This reverts commit <sha>"). Two git calls per refresh.
func revertedPRs(ctx context.Context, git hotspotGit, repo, baseRef string) map[int]bool {
	out, err := git(ctx, repo, "log", "-n", strconv.Itoa(hotspotsRevertScanDepth), "--format=%H%x1f%s%x1f%b%x1e", baseRef, "--")
	if err != nil {
		return nil
	}
	reverted := map[int]bool{}
	addRefs := func(s string, re *regexp.Regexp) {
		for _, m := range re.FindAllStringSubmatch(s, -1) {
			if n, err := strconv.Atoi(m[1]); err == nil {
				reverted[n] = true
			}
		}
	}
	var shas []string
	for _, rec := range strings.Split(string(out), "\x1e") {
		parts := strings.SplitN(strings.TrimLeft(rec, "\n"), "\x1f", 3)
		if len(parts) < 3 {
			continue
		}
		subject, body := parts[1], parts[2]
		isRevert := strings.HasPrefix(strings.ToLower(subject), "revert") || revertsCommit.MatchString(body) ||
			revertBranch.MatchString(subject)
		if !isRevert {
			continue
		}
		if m := revertedTitle.FindStringSubmatch(subject); m != nil {
			addRefs(m[1], prRef)
		}
		addRefs(subject, revertBranch)
		for _, m := range revertsCommit.FindAllStringSubmatch(body, -1) {
			if len(shas) < hotspotsMaxRevertedLookups {
				shas = append(shas, m[1])
			}
		}
	}
	if len(shas) > 0 {
		args := append([]string{"log", "--no-walk", "--ignore-missing", "--format=%s"}, shas...)
		if subjects, err := git(ctx, repo, args...); err == nil {
			for _, s := range strings.Split(string(subjects), "\n") {
				if m := prSubject.FindStringSubmatch(s); m != nil {
					addRefs("#"+cmp.Or(m[1], m[2]), prRef)
				}
			}
		}
	}
	return reverted
}

// lookupRunFiles finds the files a run's work changed: the diff of its
// branch against the merge base with the base branch while the branch
// exists and is not merged, else the commit on the base that names the
// run's PR against its first parent (squash or merge commit).
func lookupRunFiles(ctx context.Context, git hotspotGit, repo, baseRef string, r orchestrator.CompletedRun, now time.Time) hotspotRunFiles {
	e := hotspotRunFiles{checkedAt: now}
	if b := r.BranchName; safeRefPart(b) {
		for _, ref := range []string{"refs/remotes/origin/" + b, "refs/heads/" + b} {
			out, err := git(ctx, repo, "diff", "--name-only", "-z", baseRef+"..."+ref, "--")
			if err != nil {
				continue // no such ref here
			}
			if files := splitNUL(out); len(files) > 0 {
				e.files, e.method, e.resolved = files[:min(len(files), hotspotsMaxFilesPerRun)], hotspotByBranch, true
				return e
			}
			break // the branch exists but adds nothing: merged; find its PR commit
		}
	}
	n := prNumber(r.PRURL)
	if n == 0 {
		return e
	}
	num := strconv.Itoa(n)
	out, err := git(ctx, repo, "log", "-n", "1", "-E", "--format=%H",
		`--grep=\(#`+num+`\)$`, "--grep=^Merge pull request #"+num+"( |$)", baseRef, "--")
	sha := strings.TrimSpace(string(out))
	if err != nil || sha == "" {
		return e
	}
	out, err = git(ctx, repo, "diff", "--name-only", "-z", sha+"^1", sha, "--")
	if err != nil {
		return e
	}
	if files := splitNUL(out); len(files) > 0 {
		e.files, e.method, e.commit, e.resolved = files[:min(len(files), hotspotsMaxFilesPerRun)], hotspotByPRCommit, sha, true
	}
	return e
}

// hotspotAgg accumulates one hotspot row.
type hotspotAgg struct {
	row    server.RepoHotspotRow
	issues map[string]struct{}
}

func (a *hotspotAgg) add(run server.RepoHotspotRun) {
	for _, s := range run.Signals {
		switch s {
		case server.HotspotSignalFailed:
			a.row.Failed++
		case server.HotspotSignalStalled:
			a.row.Stalled++
		case server.HotspotSignalChangeRequest:
			a.row.ChangeRequests++
		case server.HotspotSignalInputRequest:
			a.row.InputRequests++
		case server.HotspotSignalReverted:
			a.row.Reverted++
		}
	}
	a.row.Total += len(run.Signals)
	a.issues[run.Identifier] = struct{}{}
	if len(a.row.Runs) < hotspotsMaxRunsPerRow {
		a.row.Runs = append(a.row.Runs, run)
	}
}

// hotspotRows turns the accumulators into rows, most problems first.
func hotspotRows(aggs map[string]*hotspotAgg) []server.RepoHotspotRow {
	rows := make([]server.RepoHotspotRow, 0, len(aggs))
	for _, a := range aggs {
		a.row.Issues = make([]string, 0, len(a.issues))
		for id := range a.issues {
			a.row.Issues = append(a.row.Issues, id)
		}
		slices.Sort(a.row.Issues)
		rows = append(rows, a.row)
	}
	slices.SortFunc(rows, func(x, y server.RepoHotspotRow) int {
		return cmp.Or(cmp.Compare(y.Total, x.Total), cmp.Compare(len(y.Issues), len(x.Issues)), cmp.Compare(x.Path, y.Path))
	})
	return rows[:min(len(rows), hotspotsMaxRows)]
}

// parentDirs are a file's ancestor directories, each ending in "/".
func parentDirs(file string) []string {
	var dirs []string
	for i, c := range file {
		if c == '/' {
			dirs = append(dirs, file[:i+1])
		}
	}
	return dirs
}

// computeHotspots is one refresh: signals from history, attribution from git
// (cached per run), aggregation per file and directory. cache is the
// refresh's own copy; the updated copy is returned.
func computeHotspots(ctx context.Context, git hotspotGit, src hotspotSource, runs []orchestrator.CompletedRun, cache map[string]hotspotRunFiles, now time.Time) (server.RepoHotspotsResponse, map[string]hotspotRunFiles) {
	resp := server.RepoHotspotsResponse{Repo: src.repo, RunsConsidered: len(runs)}
	baseRef := resolveBaseRef(ctx, git, src)
	resp.BaseRef = baseRef
	var reverted map[int]bool
	if baseRef == "" {
		resp.Notes = append(resp.Notes, fmt.Sprintf("Base branch %q was not found in %s, so runs not attributed earlier cannot be attributed to files.", src.base, src.repo))
	} else {
		reverted = revertedPRs(ctx, git, src.repo, baseRef)
	}

	// The revert signal goes to the newest non-reviewer run of each
	// reverted PR, so one revert counts once.
	revertedRun := map[int]int{} // PR number -> index in runs
	for i, r := range runs {
		if n := prNumber(r.PRURL); n > 0 && reverted[n] && r.Kind != "reviewer" {
			revertedRun[n] = i
		}
	}
	files, dirs := map[string]*hotspotAgg{}, map[string]*hotspotAgg{}
	aggFor := func(m map[string]*hotspotAgg, path string) *hotspotAgg {
		a := m[path]
		if a == nil {
			a = &hotspotAgg{row: server.RepoHotspotRow{Path: path}, issues: map[string]struct{}{}}
			m[path] = a
		}
		return a
	}
	lookups, legacyReviewers := 0, 0
	for i := len(runs) - 1; i >= 0; i-- { // newest first: the lookup budget favours recent runs
		r := runs[i]
		signals := hotspotSignals(r)
		if idx, ok := revertedRun[prNumber(r.PRURL)]; ok && idx == i {
			signals = append(signals, server.HotspotSignalReverted)
		}
		if r.Kind == "reviewer" && r.ReviewVerdict == "" && r.Status == "succeeded" {
			legacyReviewers++
		}
		if len(signals) == 0 {
			continue
		}
		resp.RunsWithSignals++
		key := hotspotRunKey(r)
		entry, cached := cache[key]
		if baseRef != "" && (!cached || (!entry.resolved && now.Sub(entry.checkedAt) >= hotspotsUnresolvedRetry)) {
			if lookups >= hotspotsMaxLookupsPerRefresh || ctx.Err() != nil {
				resp.RunsPending++
				continue
			}
			lookups++
			entry = lookupRunFiles(ctx, git, src.repo, baseRef, r, now)
			if ctx.Err() == nil { // a timed-out lookup is retried, not cached
				cache[key] = entry
			}
		}
		if !entry.resolved {
			resp.RunsUnattributed++
			continue
		}
		resp.RunsAttributed++
		run := server.RepoHotspotRun{
			RunID: r.RunID, Identifier: r.Identifier, Status: r.Status, Kind: r.Kind, Profile: r.ProfileName,
			FinishedAt: r.FinishedAt, PRURL: r.PRURL, Signals: signals,
		}
		runDirs := map[string]struct{}{}
		for _, f := range entry.files {
			aggFor(files, f).add(run)
			for _, d := range parentDirs(f) {
				runDirs[d] = struct{}{}
			}
		}
		for d := range runDirs {
			aggFor(dirs, d).add(run)
		}
	}
	resp.Files, resp.Directories = hotspotRows(files), hotspotRows(dirs)
	if resp.RunsUnattributed > 0 {
		resp.Notes = append(resp.Notes, fmt.Sprintf("%d run(s) that went wrong could not be attributed to files: their branch is not in %s (or adds nothing) and no commit on the base branch names their PR.", resp.RunsUnattributed, src.repo))
	}
	if resp.RunsPending > 0 {
		resp.Notes = append(resp.Notes, fmt.Sprintf("%d run(s) are attributed on a later refresh (at most %d git lookups per refresh).", resp.RunsPending, hotspotsMaxLookupsPerRefresh))
	}
	if legacyReviewers > 0 {
		resp.Notes = append(resp.Notes, fmt.Sprintf("%d reviewer run(s) recorded no verdict (history written before verdicts were kept) and are not counted as change requests.", legacyReviewers))
	}
	return resp, cache
}
