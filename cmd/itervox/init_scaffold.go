package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/profiles"
	"github.com/vnovick/itervox/internal/templates/scaffold"
)

// scaffoldWorkflow writes a fresh schema-2 workflow at output for the given
// tracker kind and runner: it scans dir for repo metadata, discovers models,
// writes WORKFLOW.md and the .itervox/agents profile files, and (#239) puts
// the layered settings in .itervox/settings.jsonc, so a new project starts
// with nothing deprecated in its front matter. It does not
// check whether output already exists — callers decide that. Shared by
// `itervox init` and `itervox quickstart` (#74), which is why it reports
// errors instead of exiting.
//
// opts picks the starter template (#121) and the preset (#79).
func scaffoldWorkflow(output, dir, trackerKind, runner string, opts initOptions, out io.Writer) error {
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
	if len(info.CopyFiles) > 0 {
		_, _ = fmt.Fprintf(out, "  copy_files : %s (gitignored; copied into each worktree)\n", strings.Join(info.CopyFiles, ", "))
	}
	for _, s := range info.Stacks {
		_, _ = fmt.Fprintf(out, "  stack      : %s (%s)\n", s.Name, strings.Join(s.Commands, ", "))
	}

	// Discover available models from provider APIs (best-effort).
	_, _ = fmt.Fprintf(out, "itervox init: discovering available models...\n")
	info.ClaudeModels = listClaudeModels()
	info.CodexModels = listCodexModels()
	_, _ = fmt.Fprintf(out, "  models     : %d claude, %d codex\n", len(info.ClaudeModels), len(info.CodexModels))

	content := generateWorkflowWith(trackerKind, runner, info, output, opts)

	if err := writeInitWorkflow(output, []byte(content)); err != nil {
		return fmt.Errorf("itervox init: write %s: %w", output, err)
	}
	_, _ = fmt.Fprintf(out, "itervox init: wrote %s\n", output)
	if err := writeTemplateAgentFiles(output, runner, opts); err != nil {
		return err
	}
	if opts.Template != "" {
		_, _ = fmt.Fprintf(out, "itervox init: starter template %s: %s\n", opts.Template, scaffold.Summary(opts.Template))
	}
	if opts.Preset == presetCrossReview {
		_, _ = fmt.Fprintf(out, "itervox init: cross-review preset: %s writes, reviewed by %s and reviewer (any_block, auto_review)\n", runner, crossReviewerProfile(runner))
	}
	_, _ = fmt.Fprintf(out, "itervox init: wrote .itervox/agents profiles\n")
	if err := moveScaffoldSettings(output, out); err != nil {
		return err
	}
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

// moveScaffoldSettings moves the layered settings a freshly written workflow
// sets into .itervox/settings.jsonc (#239), with the same move `itervox init
// --update` runs: comments carried over, $VAR references kept as written,
// the effective config unchanged. A fresh workflow has no backup to keep.
// WORKFLOW.md keeps the schema marker, the non-layered sections, the
// profiles and the prompt.
func moveScaffoldSettings(output string, out io.Writer) error {
	move, err := moveFrontMatterToSettings(output, true, false, true, nil)
	if err != nil {
		return fmt.Errorf("itervox init: write %s: %w", config.ProjectSettingsPath(output), err)
	}
	if move.changed() {
		_, _ = fmt.Fprintf(out, "itervox init: wrote %s (tracker, hooks, dependencies, automations and the other settings)\n", move.SettingsPath)
	}
	for _, k := range move.Kept {
		_, _ = fmt.Fprintf(out, "itervox init: kept in %s: %s\n", output, k)
	}
	if len(move.Dropped) > 0 {
		_, _ = fmt.Fprintf(out, "itervox init: %s already sets %s; its values are kept\n", move.SettingsPath, strings.Join(move.Dropped, ", "))
	}
	return nil
}
