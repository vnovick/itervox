package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/logging"
	"github.com/vnovick/itervox/internal/tracker/jira"
)

// Jira status check for `itervox doctor`.
//
// The Jira tracker matches workflow states to Jira status names. An active
// state that is not a status of the project matches no issue, and a move to
// a working, completion or failed state that is not one fails on every run.
// Doctor lists every configured state that is not a status of the project,
// with the statuses the project has.

// JiraStatusCheck is the outcome of the Jira status check.
type JiraStatusCheck struct {
	// Ran is false when the check does not apply (not a Jira tracker, or the
	// workflow did not validate) or the read failed.
	Ran     bool
	Project string
	Checked []string
	Missing []string
	// Available are the project's statuses, for the fix hint.
	Available []string
	// APIError is a redacted error from reading the statuses. Reported,
	// never fatal.
	APIError string
}

// checkJiraStatuses reads the project's statuses and diffs them against the
// configured states, ignoring case like the adapter's transition lookup.
func checkJiraStatuses(ctx context.Context, cfg *config.Config, schemaPassed bool) JiraStatusCheck {
	if cfg == nil || cfg.Tracker.Kind != "jira" || !schemaPassed {
		return JiraStatusCheck{} // an invalid workflow: the schema error already says what is wrong
	}
	sc := JiraStatusCheck{Project: cfg.Tracker.ProjectSlug, Checked: trackerStateNames(cfg.Tracker, "")}
	have, err := jira.NewClient(jira.ClientConfig{
		Endpoint:    cfg.Tracker.Endpoint,
		APIKey:      cfg.Tracker.APIKey,
		ProjectSlug: cfg.Tracker.ProjectSlug,
	}).ProjectStatuses(ctx)
	if err != nil {
		sc.APIError = logging.RedactString(err.Error())
		return sc
	}
	sc.Ran, sc.Available = true, have
	sc.Missing = withoutLabels(sc.Checked, have)
	return sc
}

func renderJiraStatusCheck(b *strings.Builder, sc JiraStatusCheck) {
	switch {
	case sc.APIError != "":
		fmt.Fprintf(b, "jira statuses: could not check project %s: %s\n", sc.Project, sc.APIError)
	case !sc.Ran:
		// Not a Jira tracker, or the workflow did not validate.
	case len(sc.Missing) == 0:
		fmt.Fprintf(b, "jira statuses: OK (%d configured states are statuses of project %s)\n", len(sc.Checked), sc.Project)
	default:
		quoted := make([]string, len(sc.Missing))
		for i, n := range sc.Missing {
			quoted[i] = strconv.Quote(n)
		}
		fmt.Fprintf(b, "ERROR: %d configured state(s) are not statuses of Jira project %s: %s. No issue is picked up in a missing active state, and a move to a missing state fails.\n",
			len(sc.Missing), sc.Project, strings.Join(quoted, ", "))
		fmt.Fprintf(b, "  statuses in %s: %s\n", sc.Project, strings.Join(sc.Available, ", "))
		fmt.Fprintf(b, "  Fix: change each one to a status above (case is ignored) in the tracker state settings (`itervox config show` names the file that sets it), or add the status to the project's workflow in Jira.\n")
	}
}
