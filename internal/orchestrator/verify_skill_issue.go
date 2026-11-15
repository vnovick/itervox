package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/vnovick/itervox/internal/domain"
)

// VerifySkillIssueTitle is the title of the issue Repo Analysis's
// "Generate verify skill" action creates (#218, pstack's
// /create-verification-skill).
const VerifySkillIssueTitle = "Write a project verification skill"

// verifySkillIssueBody asks an agent to write the repository's verification
// skill: the commands that prove a change works, in one place every agent
// run can follow.
const verifySkillIssueBody = `Itervox's readiness check found no project verification skill or script.

Write one, so every agent run can prove its change works the same way:

1. Find how this repository is built, linted and tested (CI config, Makefile, package.json, README).
2. Add ` + "`.claude/skills/verify/SKILL.md`" + ` with frontmatter (` + "`name: verify`" + `, a one-line ` + "`description`" + ` that says when to use it) and the exact commands, in order, that prove a change: format, lint, type check, unit tests, build.
3. If the commands are long, add ` + "`scripts/verify.sh`" + ` (or a ` + "`make verify`" + ` target) that runs them and exits non-zero on the first failure, and make the skill call it.
4. Run it once on a clean checkout and paste the output in your handoff.

Itervox's readiness check passes once any of these exists: a ` + "`.claude/skills/*verif*/SKILL.md`" + `, a ` + "`scripts/verify*`" + ` file, a ` + "`verify`" + ` target in the Makefile, or a ` + "`verify`" + ` script in package.json.`

// CreateVerifySkillIssue creates the "write a verification skill" issue
// through the tracker, the same path an accepted enforcement suggestion
// takes (#217): tracker.CreateIssue in the enforcement issue state. The
// issue goes into the project of an issue Itervox is tracking (a running
// one, else the first candidate), which Linear needs; GitHub ignores it.
// Called from an HTTP handler goroutine; it reads the published snapshot
// and calls the tracker, never State.
func (o *Orchestrator) CreateVerifySkillIssue(ctx context.Context) (*domain.Issue, error) {
	if o.tracker == nil {
		return nil, errors.New("orchestrator: no tracker to create the verify-skill issue in")
	}
	created, err := o.tracker.CreateIssue(ctx, o.anyTrackedIssueID(ctx), VerifySkillIssueTitle, verifySkillIssueBody, o.enforceIssueState())
	if err != nil {
		return nil, fmt.Errorf("orchestrator: create the verify-skill issue: %w", err)
	}
	identifier := ""
	if created != nil {
		identifier = created.Identifier
	}
	slog.Info("orchestrator: verify-skill issue created", "issue_identifier", identifier)
	return created, nil
}

// anyTrackedIssueID names an issue in the tracked project: a running one
// from the snapshot, else the first active candidate; "" when there is none.
func (o *Orchestrator) anyTrackedIssueID(ctx context.Context) string {
	o.snapMu.RLock()
	for _, e := range o.lastSnap.Running {
		if e != nil && e.Issue.ID != "" {
			id := e.Issue.ID
			o.snapMu.RUnlock()
			return id
		}
	}
	o.snapMu.RUnlock()
	issues, err := o.tracker.FetchCandidateIssues(ctx)
	if err != nil || len(issues) == 0 {
		return ""
	}
	return issues[0].ID
}
