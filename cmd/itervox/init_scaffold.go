package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/profiles"
)

// scaffoldWorkflow writes a fresh schema-2 workflow at output for the given
// tracker kind and runner: it scans dir for repo metadata, discovers models,
// writes WORKFLOW.md and the .itervox/agents profile files. It does not
// check whether output already exists — callers decide that. Shared by
// `itervox init` and `itervox quickstart` (#74), which is why it reports
// errors instead of exiting.
//
// preset is "" or presetCrossReview (#79).
func scaffoldWorkflow(output, dir, trackerKind, runner, preset string, out io.Writer) error {
	_, _ = fmt.Fprintf(out, "itervox init: scanning %s...\n", dir)
	info := scanRepo(dir)

	if info.RemoteURL != "" {
		_, _ = fmt.Fprintf(out, "  git remote : %s\n", info.RemoteURL)
	}
	_, _ = fmt.Fprintf(out, "  branch     : %s\n", info.DefaultBranch)
	_, _ = fmt.Fprintf(out, "  runner     : %s\n", runner)
	if info.HasClaudeMD {
		_, _ = fmt.Fprintf(out, "  CLAUDE.md  : found — prompt will reference it\n")
	} else {
		_, _ = fmt.Fprintf(out, "  CLAUDE.md  : not found — add one for best results\n")
	}
	if info.HasAgentsMD {
		_, _ = fmt.Fprintf(out, "  AGENTS.md  : found — prompt will reference it\n")
	}
	for _, s := range info.Stacks {
		_, _ = fmt.Fprintf(out, "  stack      : %s (%s)\n", s.Name, strings.Join(s.Commands, ", "))
	}

	// Discover available models from provider APIs (best-effort).
	_, _ = fmt.Fprintf(out, "itervox init: discovering available models...\n")
	info.ClaudeModels = listClaudeModels()
	info.CodexModels = listCodexModels()
	_, _ = fmt.Fprintf(out, "  models     : %d claude, %d codex\n", len(info.ClaudeModels), len(info.CodexModels))

	content := generateWorkflow(trackerKind, runner, info, output)
	if preset == presetCrossReview {
		var err error
		if content, err = applyCrossReviewPreset(content, runner); err != nil {
			return err
		}
	}

	if err := writeInitWorkflow(output, []byte(content)); err != nil {
		return fmt.Errorf("itervox init: write %s: %w", output, err)
	}
	_, _ = fmt.Fprintf(out, "itervox init: wrote %s\n", output)
	if err := writeInitAgentFiles(output, runner); err != nil {
		return err
	}
	if preset == presetCrossReview {
		if err := writeCrossReviewerFiles(output, runner); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(out, "itervox init: cross-review preset: %s writes, reviewed by %s and reviewer (any_block, auto_review)\n", runner, crossReviewerProfile(runner))
	}
	_, _ = fmt.Fprintf(out, "itervox init: wrote .itervox/agents profiles\n")
	// P0-A — scaffold built-in profile files to disk so operators see them
	// in version control next to their custom profiles. Idempotent; uses
	// writeFileIfMissing semantics so operator edits are preserved.
	return writeBuiltinProfileFilesIfMissing(output, profiles.Names())
}

// Model discovery seams: provider API calls tests replace.
var (
	listClaudeModels = agent.ListClaudeModels
	listCodexModels  = agent.ListCodexModels
)
