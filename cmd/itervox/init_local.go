package main

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/tracker/local"
)

// localSeedIdentifier is the example issue `itervox init --tracker local`
// writes (#85).
const localSeedIdentifier = "ITX-1"

// localSeedBody is the example issue's description: it explains the format
// so the file doubles as documentation.
const localSeedBody = `This is an example issue. Each issue is one Markdown file in this
directory; the file name is its identifier.

- Front matter holds title, state, priority, labels, blocked_by and branch.
- This body is the description the agent reads.
- Comments go under a "## Comments" heading, one "### <time> — <author>"
  section each. Itervox appends its own there.

Edit this file, or copy it to ITX-2.md, and set state: Todo when an agent
should pick it up. Itervox picks up edits on its next poll.`

// seedLocalIssues writes the example issue unless one is already there.
func seedLocalIssues(dir string, out io.Writer) {
	now := time.Now().UTC().Truncate(time.Second)
	err := local.WriteIssue(dir, localSeedIdentifier, local.IssueSpec{
		Title:   "Example: describe a task for an agent",
		State:   "Backlog",
		Body:    localSeedBody,
		Created: &now,
	})
	if err != nil {
		_, _ = fmt.Fprintf(out, "itervox init: example issue not written: %v\n", err)
		return
	}
	_, _ = fmt.Fprintf(out, "itervox init: wrote the example issue %s/%s.md (in Backlog)\n", dir, localSeedIdentifier)
}

// writeLocalTrackerBlock writes the tracker settings of a local workflow:
// issues are Markdown files, so there is no service and no credential.
func writeLocalTrackerBlock(b *strings.Builder) {
	b.WriteString("  # Issues are Markdown files in .itervox/issues/ (ITX-1.md, ITX-2.md, …).\n")
	b.WriteString("  # Edit them by hand, from the dashboard, or let agents create them.\n")
	b.WriteString("  # project_slug: ITX              # Optional — identifier prefix for new issues (default ITX).\n")
	b.WriteString("  active_states: [\"Todo\", \"In Progress\"]\n")
	b.WriteString("  terminal_states: [\"Done\", \"Cancelled\"]\n")
	b.WriteString("  working_state: \"In Progress\"     # State applied when an agent starts working.\n")
	b.WriteString("  completion_state: \"In Review\"     # State applied when the agent finishes.\n")
	b.WriteString("  backlog_states: [\"Backlog\"]        # Shown in TUI (b) and Kanban; not auto-dispatched.\n")
	b.WriteString("  # failed_state: \"Backlog\"       # State for issues that exhaust all retries.\n")
}
