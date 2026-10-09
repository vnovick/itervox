package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/vnovick/itervox/internal/templates"
)

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
		_, _ = fmt.Fprintf(out, "\n----- agent-ready issue template -----\n%s----- end -----\n\n", templates.AgentTaskBody)
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
