package main

import "strings"

// writeJiraTrackerBlock writes the tracker settings of a Jira workflow. The
// site URL and the project key cannot be detected, so they are left empty:
// the workflow does not validate, and sends nothing anywhere, until the
// operator fills them in. The states are the statuses every new Jira
// software project has: a team-managed project starts with To Do, In
// Progress and Done, and so does a company-managed Scrum project. None has a
// review status, so a finished issue goes to Done.
func writeJiraTrackerBlock(b *strings.Builder) {
	b.WriteString("  endpoint: \"\"                     # Your Jira Cloud site URL, like https://your-site.atlassian.net ($VAR is not resolved here).\n")
	b.WriteString("  project_slug: \"\"                 # The Jira project key, like PROJ (issue keys are PROJ-1, PROJ-2, …).\n")
	b.WriteString("  # States are Jira status names. These are the statuses of a new team-managed\n")
	b.WriteString("  # project and of a new company-managed Scrum project. A company-managed Kanban\n")
	b.WriteString("  # project starts with Backlog, Selected for Development, In Progress and Done:\n")
	b.WriteString("  # use \"Selected for Development\" in place of \"To Do\". `itervox doctor` lists\n")
	b.WriteString("  # every state here that is not a status of the project.\n")
	b.WriteString("  active_states: [\"To Do\", \"In Progress\"]\n")
	b.WriteString("  terminal_states: [\"Done\"]\n")
	b.WriteString("  working_state: \"In Progress\"     # Status set when an agent starts. The Jira workflow must allow the move.\n")
	b.WriteString("  completion_state: \"" + completionStateFor("jira") + "\"           # Status set when the agent finishes. Use a review status, like \"In Review\", if your workflow has one.\n")
}
