package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/gitexec"
	"github.com/vnovick/itervox/internal/workspace"
)

// "Done needs evidence" (#80). A profile with require_evidence must prove
// its checks before Itervox moves the issue to completion_state: the agent
// records each check (command, output, pass) in a JSON file, stamped with
// the commit it ran on, and "ci" is satisfied by the pull request's checks.
// Without that proof the run ends input-required with the reason, instead
// of moving the issue.

// EvidenceDirRelPath is where evidence files live in the workspace.
const EvidenceDirRelPath = ".itervox/evidence"

// evidenceFile is the agent-written evidence record.
type evidenceFile struct {
	Commit string          `json:"commit"`
	Checks []evidenceCheck `json:"checks"`
}

type evidenceCheck struct {
	Name    string `json:"name"`
	Command string `json:"command"`
	Output  string `json:"output"`
	Passed  bool   `json:"passed"`
}

// evidenceRelPathFor is the evidence file for a profile. It is stable across
// runs (unlike run.handoff_path) so a resumed session still knows it; the
// commit stamp is what keeps an old file from counting for new code.
func evidenceRelPathFor(profileName string) string {
	role := strings.ReplaceAll(strings.TrimSpace(profileName), " ", "-")
	if role == "" {
		role = "agent"
	}
	return filepath.Join(EvidenceDirRelPath, role+".json")
}

// buildEvidenceBlock tells the agent which checks to prove and how. Empty
// when nothing is required.
func buildEvidenceBlock(required []string, relPath string) string {
	var fileChecks []string
	needCI := false
	for _, c := range required {
		if c == config.EvidenceCheckCI {
			needCI = true
			continue
		}
		fileChecks = append(fileChecks, c)
	}
	if len(fileChecks) == 0 && !needCI {
		return ""
	}
	lines := []string{"## Evidence (required)", ""}
	if len(fileChecks) > 0 {
		lines = append(lines,
			fmt.Sprintf("- run.evidence_path: `%s`", relPath),
			fmt.Sprintf("- required checks: %s", "`"+strings.Join(fileChecks, "`, `")+"`"),
			"",
			"This issue is not moved on until you prove these checks passed. After your last commit,",
			"run each check and write JSON to `run.evidence_path`:",
			"",
			"```json",
			`{"commit": "<output of git rev-parse HEAD>", "checks": [`,
			`  {"name": "test", "command": "the exact command you ran", "output": "the last lines of its output", "passed": true}`,
			"]}",
			"```",
			"",
			"Evidence counts only for the commit it names, so record it after committing.",
		)
	}
	if needCI {
		if len(fileChecks) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, "- `ci`: the pull request's CI checks must all pass before the issue moves on.")
	}
	return strings.Join(lines, "\n")
}

// evidenceVerdict is the result of checking a run's evidence.
type evidenceVerdict struct {
	Missing []string // one human-readable reason per unmet check
}

func (v evidenceVerdict) ok() bool { return len(v.Missing) == 0 }

// checkEvidence evaluates required against the evidence file in wsPath and,
// for "ci", the checks on prURL. readChecks is workspace.ReadPRChecks in
// production.
func checkEvidence(
	ctx context.Context,
	wsPath, relPath string,
	required []string,
	prURL string,
	readChecks func(ctx context.Context, prURL string) (workspace.PRChecks, error),
) evidenceVerdict {
	var v evidenceVerdict
	var fileChecks []string
	for _, c := range required {
		if c != config.EvidenceCheckCI {
			fileChecks = append(fileChecks, c)
		}
	}

	if len(fileChecks) > 0 {
		passed, reason := readEvidenceFile(ctx, wsPath, relPath)
		for _, name := range fileChecks {
			switch {
			case reason != "":
				v.Missing = append(v.Missing, fmt.Sprintf("`%s`: %s", name, reason))
			case !passed[name]:
				v.Missing = append(v.Missing, fmt.Sprintf("`%s`: no passing entry with a command and output in `%s`", name, relPath))
			}
		}
	}

	if containsString(required, config.EvidenceCheckCI) {
		if prURL == "" {
			v.Missing = append(v.Missing, "`ci`: no pull request was found for this run")
		} else {
			sum, err := readChecks(ctx, prURL)
			switch {
			case err != nil:
				v.Missing = append(v.Missing, fmt.Sprintf("`ci`: could not read the checks on %s (%v)", prURL, err))
			case sum.Failed > 0:
				v.Missing = append(v.Missing, fmt.Sprintf("`ci`: %d of %d checks failed on %s", sum.Failed, sum.Total, prURL))
			case sum.Pending > 0:
				v.Missing = append(v.Missing, fmt.Sprintf("`ci`: %d of %d checks still running on %s", sum.Pending, sum.Total, prURL))
			}
		}
	}
	return v
}

// readEvidenceFile returns the passing check names in the evidence file, or
// a reason the whole file does not count.
func readEvidenceFile(ctx context.Context, wsPath, relPath string) (map[string]bool, string) {
	raw, err := os.ReadFile(filepath.Join(wsPath, relPath))
	if err != nil {
		return nil, fmt.Sprintf("no evidence file at `%s`", relPath)
	}
	var ev evidenceFile
	if err := json.Unmarshal(raw, &ev); err != nil {
		return nil, fmt.Sprintf("`%s` is not valid JSON (%v)", relPath, err)
	}
	if head := worktreeHEAD(ctx, wsPath); head != "" {
		commit := strings.ToLower(strings.TrimSpace(ev.Commit))
		if len(commit) < 7 || !evidenceCoversHEAD(ctx, wsPath, commit, head) {
			return nil, fmt.Sprintf("the evidence in `%s` is for commit %q, not the current %s", relPath, ev.Commit, head[:12])
		}
		// The stamp names a commit, so a staged or unstaged change to a
		// tracked file on top of it is code the checks did not run on.
		if dirty := uncommittedChange(ctx, wsPath); dirty != "" {
			return nil, fmt.Sprintf("`%s` has uncommitted changes the evidence in `%s` does not cover; commit them and run the checks again", dirty, relPath)
		}
	}
	passed := map[string]bool{}
	for _, c := range ev.Checks {
		name := strings.ToLower(strings.TrimSpace(c.Name))
		if c.Passed && strings.TrimSpace(c.Command) != "" && strings.TrimSpace(c.Output) != "" {
			passed[name] = true
		}
	}
	return passed, ""
}

// evidenceCoversHEAD reports whether evidence stamped with commit still
// holds for head: commit is head, or an ancestor of it where every later
// change is Itervox's own bookkeeping under .itervox/ (the handoff commit the
// worker makes after the run, or the agent committing its evidence file).
// Any code change after the stamp means the checks ran on other code.
func evidenceCoversHEAD(ctx context.Context, wsPath, commit, head string) bool {
	// Only a hex commit id counts: a ref name ("main", the issue branch)
	// would resolve to whatever it points at later and never go stale.
	if !evidenceCommitRe.MatchString(commit) {
		return false
	}
	if strings.HasPrefix(head, commit) {
		return true
	}
	out, err := gitexec.Command(ctx, wsPath, "rev-parse", "--verify", "--quiet", commit+"^{commit}").Output()
	if err != nil {
		return false
	}
	stamp := strings.TrimSpace(string(out))
	if gitexec.Command(ctx, wsPath, "merge-base", "--is-ancestor", stamp, head).Run() != nil {
		return false
	}
	changed, err := gitexec.Command(ctx, wsPath, "diff", "--no-renames", "--name-only", stamp, head).Output()
	if err != nil {
		return false
	}
	for _, f := range strings.Split(strings.TrimSpace(string(changed)), "\n") {
		if f == "" {
			continue
		}
		if !isEvidenceBookkeeping(f) {
			return false
		}
	}
	return true
}

// uncommittedChange returns a tracked file with a staged or unstaged change
// in wsPath outside Itervox's bookkeeping directories (handoff, evidence), or
// "" when there is none. A failing git status counts as a change.
func uncommittedChange(ctx context.Context, wsPath string) string {
	out, err := gitexec.Command(ctx, wsPath, "status", "--porcelain=v1", "-z", "--untracked-files=no", "--no-renames").Output()
	if err != nil {
		return "(git status failed)"
	}
	for _, rec := range strings.Split(string(out), "\x00") {
		if len(rec) < 4 { // "XY path"
			continue
		}
		if f := rec[3:]; !isEvidenceBookkeeping(f) {
			return f
		}
	}
	return ""
}

// isEvidenceBookkeeping reports whether a repository path is Itervox's own
// bookkeeping, which may change after the evidence stamp.
func isEvidenceBookkeeping(f string) bool {
	return strings.HasPrefix(f, HandoffDirRelPath+"/") || strings.HasPrefix(f, EvidenceDirRelPath+"/")
}

// evidenceCommitRe is the shape of an evidence commit stamp: an
// abbreviated or full hex commit id, lower-cased by the caller.
var evidenceCommitRe = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

// worktreeHEAD is the full HEAD commit of wsPath, or "" outside a git work
// tree (directory workspaces), where the commit stamp cannot be checked.
func worktreeHEAD(ctx context.Context, wsPath string) string {
	out, err := gitexec.Command(ctx, wsPath, "rev-parse", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(string(out)))
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// evidenceHoldContext is the input-required text for a run held for
// evidence: why, and what to do.
func evidenceHoldContext(completionState string, v evidenceVerdict) string {
	return fmt.Sprintf("Needs evidence before moving to %q (require_evidence):\n- %s\n\n"+
		"Ask the agent to run the missing checks and record them (or wait for CI), then reply to resume; "+
		"the issue moves on once the evidence is in place.",
		completionState, strings.Join(v.Missing, "\n- "))
}

func (o *Orchestrator) prChecksReader() func(ctx context.Context, prURL string) (workspace.PRChecks, error) {
	if o.readPRChecks != nil {
		return o.readPRChecks
	}
	return workspace.ReadPRChecks
}
