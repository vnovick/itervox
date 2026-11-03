package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/logging"
	"github.com/vnovick/itervox/internal/tracker"
	"github.com/vnovick/itervox/internal/tracker/linear"
)

// assigneeCheckTimeout bounds the doctor's assignee check (#182).
const assigneeCheckTimeout = 15 * time.Second

// AssigneeCheck is the outcome of the Linear assignee-scope check (#182):
// does tracker.assignee resolve to a user the key can see, and can the key
// read the issues assigned to that user across the teams it covers.
type AssigneeCheck struct {
	// Ran is false when the check does not apply (no assignee scope) or
	// could not run; SkipReason then says why when it is worth telling.
	Ran        bool
	SkipReason string
	Spec       string
	Team       string
	// User is the resolved user's label, empty when it did not resolve.
	User string
	// Unresolvable: no user the key can see matches Spec. A broken
	// configuration — the daemon's polls fail until it is fixed.
	Unresolvable bool
	// ResolveError is a redacted transport/API error from the resolution
	// (reported, not fatal: the answer is unknown, not "no such user").
	ResolveError string
	// ReadError is a redacted error from the read check.
	ReadError string
	// TeamMissing: tracker.team names no team the key can read.
	TeamMissing bool
	Teams       []string
	Issues      int
	IssueTeams  []string
}

// checkLinearAssignee runs the assignee-scope check against the tracker
// endpoint, through the same Linear client the daemon uses.
func checkLinearAssignee(ctx context.Context, cfg *config.Config) AssigneeCheck {
	if cfg == nil || cfg.Tracker.Kind != "linear" || strings.TrimSpace(cfg.Tracker.Assignee) == "" {
		return AssigneeCheck{}
	}
	ac := AssigneeCheck{Spec: strings.TrimSpace(cfg.Tracker.Assignee), Team: strings.TrimSpace(cfg.Tracker.Team)}
	if strings.TrimSpace(cfg.Tracker.APIKey) == "" {
		ac.SkipReason = "tracker.api_key is empty (set LINEAR_API_KEY, or run `itervox doctor --deploy`, which loads .itervox/.env)"
		return ac
	}
	c := linear.NewClient(linear.ClientConfig{
		APIKey: cfg.Tracker.APIKey, Endpoint: cfg.Tracker.Endpoint,
		Assignee: ac.Spec, Team: ac.Team,
	})
	ac.Ran = true
	u, err := c.ResolveAssignee(ctx)
	switch {
	case errors.Is(err, tracker.ErrAssigneeNotFound):
		ac.Unresolvable = true
		return ac
	case err != nil:
		ac.ResolveError = logging.RedactString(err.Error())
		return ac
	}
	ac.User = u.Label()
	probe, err := c.ProbeAssigneeScope(ctx)
	if err != nil {
		ac.ReadError = logging.RedactString(err.Error())
		return ac
	}
	ac.TeamMissing = !probe.TeamFound
	ac.Teams, ac.Issues, ac.IssueTeams = probe.Teams, probe.Issues, probe.IssueTeams
	return ac
}

// assigneeCheckFailed reports a broken assignee configuration (exit 1).
func assigneeCheckFailed(ac AssigneeCheck) bool { return ac.Unresolvable || ac.TeamMissing }

// renderAssigneeCheck writes the check's lines into the doctor report.
func renderAssigneeCheck(b *strings.Builder, ac AssigneeCheck) {
	switch {
	case ac.Spec == "":
		return
	case !ac.Ran:
		if ac.SkipReason != "" {
			fmt.Fprintf(b, "assignee scope: not checked — %s\n", ac.SkipReason)
		}
		return
	case ac.Unresolvable:
		fmt.Fprintf(b, "ERROR: tracker.assignee %q does not resolve to a user this API key can see — use me, a user id or the email of a workspace member\n", ac.Spec)
		return
	case ac.ResolveError != "":
		fmt.Fprintf(b, "WARNING: could not resolve tracker.assignee %q: %s\n", ac.Spec, ac.ResolveError)
		return
	}
	fmt.Fprintf(b, "assignee scope: %q resolves to %s\n", ac.Spec, ac.User)
	if ac.ReadError != "" {
		fmt.Fprintf(b, "WARNING: could not read the issues assigned to %s: %s\n", ac.User, ac.ReadError)
		return
	}
	teams := or(strings.Join(ac.Teams, ", "), "none")
	if ac.TeamMissing {
		fmt.Fprintf(b, "ERROR: tracker.team %q is not a team this API key can read (teams: %s)\n", ac.Team, teams)
		return
	}
	scope := "across the teams the key reads (" + teams + ")"
	if ac.Team != "" {
		scope = "in team " + ac.Team
	}
	fmt.Fprintf(b, "assignee reads: OK — %d issue(s) assigned to %s readable %s%s\n",
		ac.Issues, ac.User, scope, issueTeamsSuffix(ac.IssueTeams))
}

func issueTeamsSuffix(teams []string) string {
	if len(teams) == 0 {
		return ""
	}
	return "; from " + strings.Join(teams, ", ")
}
