package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/vnovick/itervox/internal/atomicfs"
)

var initAgentProfileNames = []string{"implementer", "reviewer", "input-responder"}

func writeInitAgentFiles(workflowPath, runner string) error {
	baseDir := filepath.Dir(workflowPath)
	if baseDir == "" {
		baseDir = "."
	}
	for _, profile := range initAgentProfileNames {
		profileDir := filepath.Join(baseDir, ".itervox", "agents", profile)
		if err := os.MkdirAll(profileDir, 0o755); err != nil {
			return fmt.Errorf("itervox init: create %s: %w", profileDir, err)
		}
		if err := writeFileIfMissing(filepath.Join(profileDir, "SOUL.md"), initSoulContent(profile)); err != nil {
			return err
		}
		if err := writeFileIfMissing(filepath.Join(profileDir, "INSTRUCTIONS.md"), initInstructionsContent(profile, runner)); err != nil {
			return err
		}
	}
	return ensureItervoxGitignore(filepath.Join(baseDir, ".itervox"))
}

func writeFileIfMissing(path string, content string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("itervox init: stat %s: %w", path, err)
	}
	if err := atomicfs.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("itervox init: write %s: %w", path, err)
	}
	return nil
}

// finalizeItervoxGitignore writes the .itervox/.gitignore that keeps runtime
// files out of git, AND patches the project root .gitignore to carve-out
// agent + handoff dirs when the root broadly ignores `.itervox/`. The latter
// is a no-op when the root has no `.itervox/` blacklist (the common case for
// fresh `itervox init` projects).
//
// `itervoxDir` is the absolute path to the project's `.itervox/` directory.
func finalizeItervoxGitignore(itervoxDir string) error {
	if err := ensureItervoxGitignore(itervoxDir); err != nil {
		return err
	}
	if err := patchRootGitignoreForAgents(filepath.Dir(itervoxDir)); err != nil {
		return fmt.Errorf("itervox init: patch root .gitignore: %w", err)
	}
	return nil
}

func ensureItervoxGitignore(itervoxDir string) error {
	if err := os.MkdirAll(itervoxDir, 0o755); err != nil {
		return fmt.Errorf("itervox init: create %s: %w", itervoxDir, err)
	}
	path := filepath.Join(itervoxDir, ".gitignore")
	lines := []string{".env", "HEARTBEAT.md", "logs/", "runtime/", "/*.json"}
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("itervox init: read %s: %w", path, err)
	}
	seen := make(map[string]struct{})
	var out []string
	for _, line := range strings.Split(strings.ReplaceAll(string(existing), "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if trimmed == "*.json" {
			trimmed = "/*.json"
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	for _, line := range lines {
		if _, ok := seen[line]; ok {
			continue
		}
		out = append(out, line)
	}
	if err := atomicfs.WriteFile(path, []byte(strings.Join(out, "\n")+"\n"), 0o644); err != nil {
		return fmt.Errorf("itervox init: write %s: %w", path, err)
	}
	return nil
}

func initProfileCommand(runner string) (command string, backend string) {
	if runner == "codex" {
		return "codex", "codex"
	}
	return "claude", ""
}

func initSoulContent(profile string) string {
	switch profile {
	case "reviewer":
		return `# reviewer SOUL

## Identity
You are the code review engineer for this repository.

## Purpose
Find correctness, safety, test coverage, and integration risks before changes merge.

## Boundaries
Do not rewrite unrelated implementation. Do not commit secrets.

## Collaboration Style
Be direct, evidence-driven, and specific about required fixes.
`
	case "input-responder":
		return `# input-responder SOUL

## Identity
You are the focused unblocker for this repository.

## Purpose
Answer narrow agent questions when enough tracker and repository context exists.

## Boundaries
Do not make broad product or architecture decisions without human confirmation.

## Collaboration Style
State assumptions clearly and keep answers short.
`
	default:
		return `# implementer SOUL

## Identity
You are the implementation engineer for this repository.

## Purpose
Ship narrowly scoped changes that satisfy the tracker issue.

## Boundaries
Do not change unrelated files. Do not commit secrets.

## Collaboration Style
Be direct, evidence-driven, and explicit about blockers.
`
	}
}

// handoffProtocolSection is appended to every profile's INSTRUCTIONS.md so
// agents follow a consistent convention: read the prerendered prior-handoff
// block in their prompt context, write their own deliverable to the path
// the orchestrator computed at dispatch time, and exit. The orchestrator
// renames the file to `<basename>.partial.md` if the worker exits with a
// non-success terminal reason.
const handoffProtocolSection = `## Handoff Protocol
- The orchestrator prepends a "## Prior Agent Handoffs" block to your prompt with every prior agent's deliverable on this issue's branch, in chronological order. Read it before doing any work — it captures research findings, design decisions, and prior attempts.
- The orchestrator also passes a "## Run Context" block with two values: ` + "`run.timestamp`" + ` and ` + "`run.handoff_path`" + `.
- Before exiting, write a concise Markdown deliverable to ` + "`run.handoff_path`" + ` summarizing what you did, key decisions, and anything the next agent on this branch needs to know. Keep it scoped — a few hundred words is usually enough.
- Do not edit handoff files authored by prior agents. Add your own; the chronological order is the audit trail.
- If you exit before writing (crash, stall, max-turn timeout), the orchestrator will rename the file you started to ` + "`<basename>.partial.md`" + ` so the next agent knows it is incomplete.
`

func initInstructionsContent(profile string, runner string) string {
	switch profile {
	case "reviewer":
		return `# reviewer INSTRUCTIONS

## Required Reading
- Read project agent instructions such as AGENTS.md, CLAUDE.md, and README.md before reviewing.

## Workflow
- Inspect the diff and related tests.
- Prioritize real bugs, regressions, race conditions, missing tests, and security issues.
- Fix only narrow review findings when the tracker issue or workflow asks you to do so.

## Done Criteria
- Findings are specific and actionable.
- Verification commands are named with their results.

` + handoffProtocolSection
	case "input-responder":
		return `# input-responder INSTRUCTIONS

## Required Reading
- Read the blocked agent's question and the relevant tracker comments.

## Workflow
- Answer only the question that is blocking progress.
- Use repository evidence when the answer depends on code.
- Ask for human input if the requested decision is ambiguous or high risk.

## Done Criteria
- The response is concise enough to unblock the waiting run.

` + handoffProtocolSection
	default:
		return fmt.Sprintf(`# implementer INSTRUCTIONS

## Required Reading
- Read project agent instructions such as AGENTS.md, CLAUDE.md, and README.md before editing.

## Workflow
- Explore the issue and relevant code before changing files.
- Follow the repository's established patterns.
- Implement the smallest complete change.
- Run the relevant checks before reporting completion.

## Done Criteria
- The tracker issue is fully addressed.
- Tests or documented verification are complete.
- No secrets or unrelated changes are included.

## Runner
- Default generated runner: %s.

%s`, runner, handoffProtocolSection)
	}
}
