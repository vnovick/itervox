package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// The "cross-review" preset (#79): the implementer runs on the chosen runner
// and every successful run is reviewed by two read-only reviewers, one on
// each backend — "Claude writes, Codex reviews" with `--runner claude`, the
// reverse with `--runner codex`. Each reviewer is a separate agent run.

// presetCrossReview is the --preset value.
const presetCrossReview = "cross-review"

// otherRunner is the backend the cross reviewer runs on.
func otherRunner(runner string) string {
	if runner == "codex" {
		return "claude"
	}
	return "codex"
}

// crossReviewerProfile is the reviewer profile on the other backend.
func crossReviewerProfile(runner string) string {
	return "reviewer-" + otherRunner(runner)
}

// applyCrossReviewPreset turns a generated workflow into the preset: it adds
// the cross reviewer's profile after `reviewer` and replaces the single
// reviewer settings with the two-reviewer chain, any_block quorum and
// auto_review. It edits init's own fresh output, so the anchors are fixed.
func applyCrossReviewPreset(content, runner string) (string, error) {
	cross := crossReviewerProfile(runner)
	command, backend := initProfileCommand(otherRunner(runner))

	reviewerBlockEnd := "      instructions_file: .itervox/agents/reviewer/INSTRUCTIONS.md\n      allowed_actions: [comment, comment_pr]\n"
	if !strings.Contains(content, reviewerBlockEnd) {
		return "", fmt.Errorf("itervox init: preset %s: reviewer profile not found in the generated workflow", presetCrossReview)
	}
	var block strings.Builder
	block.WriteString("    " + cross + ":\n")
	block.WriteString("      command: " + command + "\n")
	if backend != "" {
		block.WriteString("      backend: " + backend + "\n")
	}
	block.WriteString("      soul_file: .itervox/agents/" + cross + "/SOUL.md\n")
	block.WriteString("      instructions_file: .itervox/agents/" + cross + "/INSTRUCTIONS.md\n")
	block.WriteString("      allowed_actions: [comment, comment_pr]\n")
	content = strings.Replace(content, reviewerBlockEnd, reviewerBlockEnd+block.String(), 1)

	settingsLine := "  reviewer_profile: reviewer         # Profile used by the AI Review button and optional auto-review.\n"
	autoLine := "  # auto_review: false               # Set to true to auto-review after each successful agent run. Coexists with workspace.auto_clear as of v0.2.0 — the clear fires on terminal tracker state, after the reviewer also completes.\n"
	if !strings.Contains(content, settingsLine) || !strings.Contains(content, autoLine) {
		return "", fmt.Errorf("itervox init: preset %s: reviewer settings not found in the generated workflow", presetCrossReview)
	}
	content = strings.Replace(content, settingsLine,
		"  # cross-review preset (#79): two read-only reviewers on different backends review every successful run.\n"+
			"  reviewer_profile: "+cross+"   # Used by the AI Review button.\n"+
			"  reviewer_profiles: ["+cross+", reviewer]   # Each reviewer is a separate agent run.\n"+
			"  review_quorum: any_block   # Changes are requested if either reviewer blocks.\n"+
			"  auto_review: true\n", 1)
	content = strings.Replace(content, autoLine, "", 1)
	return content, nil
}

// writeCrossReviewerFiles writes the cross reviewer's SOUL.md and
// INSTRUCTIONS.md (the read-only reviewer scaffold), keeping existing files.
func writeCrossReviewerFiles(workflowPath, runner string) error {
	cross := crossReviewerProfile(runner)
	dir := filepath.Join(filepath.Dir(workflowPath), ".itervox", "agents", cross)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("itervox init: create %s: %w", dir, err)
	}
	if err := writeFileIfMissing(filepath.Join(dir, "SOUL.md"), initSoulContent(cross)); err != nil {
		return err
	}
	return writeFileIfMissing(filepath.Join(dir, "INSTRUCTIONS.md"), initInstructionsContent(cross, otherRunner(runner)))
}

// lookPath is exec.LookPath, replaceable in tests.
var lookPath = exec.LookPath

// offerCrossReviewPreset asks, on a terminal, whether to use the preset when
// both agent CLIs are installed (the cross reviewer needs the other one).
// No terminal, or either CLI missing, means no question and no preset.
func offerCrossReviewPreset(runner string, terminal bool, in *bufio.Reader, out io.Writer) string {
	if !terminal {
		return ""
	}
	if _, err := lookPath("claude"); err != nil {
		return ""
	}
	if _, err := lookPath("codex"); err != nil {
		return ""
	}
	writer, reviewer := "Claude", "Codex"
	if runner == "codex" {
		writer, reviewer = "Codex", "Claude"
	}
	q := fmt.Sprintf("Use the cross-review preset (%s writes; %s and %s review every run, read-only; each review is a separate agent run)?", writer, reviewer, writer)
	if confirmPrompt(in, out, false, q) {
		return presetCrossReview
	}
	return ""
}
