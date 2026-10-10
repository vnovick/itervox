package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/x/term"
	"github.com/vnovick/itervox/internal/templates"
)

// stdinIsTerminal reports whether init can ask a question.
var stdinIsTerminal = func() bool { return term.IsTerminal(os.Stdin.Fd()) }

// initIssueTemplateStep is init's issue-template step: with
// --issue-template it adds the template without asking; otherwise it asks
// only when stdin is a terminal, and says how to add it later when not.
func initIssueTemplateStep(repoDir, trackerKind string, flag, interactive bool, in *bufio.Reader, out io.Writer) {
	if trackerKind != "github" && trackerKind != "linear" {
		return
	}
	if !flag && !interactive {
		_, _ = fmt.Fprintf(out, "issue template: not offered (no terminal); add it later with `itervox init --tracker %s --issue-template` (keeps the existing workflow)\n", trackerKind)
		return
	}
	offerIssueTemplate(repoDir, trackerKind, flag, in, out)
}

// issueTemplateOnly is init's template-only path: with --issue-template and
// a workflow already at output (and no --force), it adds just the issue
// template and reports true, leaving the workflow and every other file as
// they are. Without it, the hint above led into init's existing-workflow
// refusal.
func issueTemplateOnly(output, repoDir, trackerKind string, flag, force bool, out io.Writer) bool {
	if !flag || force {
		return false
	}
	if _, err := os.Stat(output); err != nil {
		return false
	}
	_, _ = fmt.Fprintf(out, "issue template: %s exists; adding only the issue template\n", output)
	offerIssueTemplate(repoDir, trackerKind, true, bufio.NewReader(strings.NewReader("")), out)
	return true
}

// agentTaskTemplateRel is where the agent-ready GitHub issue template goes.
var agentTaskTemplateRel = filepath.Join(".github", "ISSUE_TEMPLATE", "agent-task.md")

// offerIssueTemplate offers the agent-ready issue template (#83) for a new
// workflow: for GitHub it writes .github/ISSUE_TEMPLATE/agent-task.md under
// repoDir after confirmation and never replaces an existing file; for Linear
// it prints the same sections to paste into a Linear issue template.
// yes answers the question without reading in.
func offerIssueTemplate(repoDir, trackerKind string, yes bool, in *bufio.Reader, out io.Writer) {
	switch trackerKind {
	case "github":
		path := filepath.Join(repoDir, agentTaskTemplateRel)
		if _, err := os.Stat(path); err == nil {
			_, _ = fmt.Fprintf(out, "issue template: %s already exists; leaving it as is\n", path)
			return
		}
		if !confirmPrompt(in, out, yes, "Add an agent-ready issue template at "+agentTaskTemplateRel+"?") {
			return
		}
		if err := writeFileExclusive(path, templates.AgentTaskGitHubTemplate()); err != nil {
			_, _ = fmt.Fprintf(out, "issue template: %v\n", err)
			return
		}
		_, _ = fmt.Fprintf(out, "issue template: wrote %s (commit it so it appears under New issue)\n", path)
	case "linear":
		if !confirmPrompt(in, out, yes, "Print an agent-ready issue template to paste into Linear (Settings → Templates)?") {
			return
		}
		_, _ = fmt.Fprintf(out, "\n----- agent-ready issue template -----\n%s----- end -----\n\n", linearTemplateBody())
	}
}

// writeFileExclusive creates path (and its parents) and fails if it exists.
func writeFileExclusive(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%s already exists; not overwriting it", path)
		}
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// linearTemplateBody is AgentTaskBody with a Blockers section for Linear,
// where Itervox reads blockers from Linear's own "blocked by" relation, not
// from phrases in the description.
func linearTemplateBody() string {
	body := string(templates.AgentTaskBody)
	if i := strings.Index(body, "## Blockers"); i >= 0 {
		body = body[:i] + "## Blockers\n\n<!-- Add them with Linear's \"Blocked by\" relation; Itervox reads that, not this text. -->\n"
	}
	return body
}
