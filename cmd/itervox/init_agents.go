package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/vnovick/itervox/internal/atomicfs"
	"github.com/vnovick/itervox/internal/profiles"
)

// writeBuiltinProfileFilesIfMissing scaffolds embedded SOUL.md and
// INSTRUCTIONS.md to .itervox/agents/<name>/ for every profile name in
// profileNames that resolves to a shipped built-in. Existing files are left
// intact (writeFileIfMissing semantics). Returns nil if no built-ins are
// referenced.
func writeBuiltinProfileFilesIfMissing(workflowPath string, profileNames []string) error {
	base := filepath.Dir(workflowPath)
	if base == "" {
		base = "."
	}
	for _, name := range profileNames {
		b := profiles.Lookup(name)
		if b == nil {
			continue
		}
		dir := filepath.Join(base, ".itervox", "agents", name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("itervox init: create %s: %w", dir, err)
		}
		if err := writeFileIfMissing(filepath.Join(dir, "SOUL.md"), b.Soul+"\n"); err != nil {
			return err
		}
		if err := writeFileIfMissing(filepath.Join(dir, "INSTRUCTIONS.md"), b.Instructions+"\n"); err != nil {
			return err
		}
	}
	return nil
}

var initAgentProfileNames = []string{"implementer", "reviewer", "input-responder", "deps-analyzer"}

// initDepsAnalyzerProfileName is the canonical name for the file-backed
// dependency-analyzer profile written by `itervox init`. The dashboard's
// "Analyze dependencies" button keys off agent.deps_analyzer_profile, which is
// wired to this name in the generated WORKFLOW.md front matter.
const initDepsAnalyzerProfileName = "deps-analyzer"

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
	lines := []string{".env", "HEARTBEAT.md", "daemon.pid", "dashboard_url", "STARTUP_ERROR.md", "logs/", "runtime/", "/*.json", "bin/", "*.db"}
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

// writeDepsAnalyzerProfileFiles writes the deps-analyzer SOUL + INSTRUCTIONS
// scaffolds into `.itervox/agents/deps-analyzer/` next to the given workflow
// path. Existing files are left intact (writeFileIfMissing semantics) so a
// user's prior edits to the scaffold are preserved across re-runs.
func writeDepsAnalyzerProfileFiles(workflowPath, runner string) error {
	dir := filepath.Join(filepath.Dir(workflowPath), ".itervox", "agents", initDepsAnalyzerProfileName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("itervox init --update: create %s: %w", dir, err)
	}
	if err := writeFileIfMissing(filepath.Join(dir, "SOUL.md"), initSoulContent(initDepsAnalyzerProfileName)); err != nil {
		return err
	}
	return writeFileIfMissing(filepath.Join(dir, "INSTRUCTIONS.md"), initInstructionsContent(initDepsAnalyzerProfileName, runner))
}

// detectMigrationRunner inspects existing agent.profiles entries and returns
// "codex" when any profile's command mentions codex, else "claude". Used by
// migrate paths that don't have a --runner flag to pick a sensible default
// for newly-scaffolded profiles.
func detectMigrationRunner(profiles map[string]any) string {
	for _, raw := range profiles {
		profile, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		command, _ := profile["command"].(string)
		if strings.Contains(strings.ToLower(command), "codex") {
			return "codex"
		}
		backend, _ := profile["backend"].(string)
		if strings.EqualFold(backend, "codex") {
			return "codex"
		}
	}
	return "claude"
}

func initSoulContent(profile string) string {
	switch profile {
	case initDepsAnalyzerProfileName:
		return `# deps-analyzer SOUL

## Identity
You are the dependency analyzer for this itervox-managed project. You read
the full issue list and surface dependency relations the tracker does not
declare.

## Purpose
Detect natural-language "X depends on Y" / "X blocked by Y" / "X is a
sub-task of Y" relations in issue titles and bodies, and emit them as a
strict JSON edge list. You never modify tracker state and never speculate
beyond explicit textual evidence.

## Boundaries
- Never write to the tracker. Read-only.
- Skip any relation already declared by the tracker.
- Stop on ambiguity rather than guess.

## Output Contract
A single JSON object on stdout: {"edges":[{"source":"FOO-12","target":"FOO-34","evidence":"..."}]}.
Nothing else — no surrounding prose, no markdown fences.
`
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
	case initDepsAnalyzerProfileName:
		return `# deps-analyzer INSTRUCTIONS

## Required Reading
- Read the supplied "## Issues" block in your prompt. Each entry has identifier, title, body, and current state.
- Read the supplied "## Existing Tracker Edges" block; these are relations the tracker already declares.

## Workflow
1. Walk every issue. For each issue, look for natural-language references that imply a dependency:
   - "blocked by FOO-12"
   - "depends on FOO-34"
   - "sub-task of FOO-56"
   - "needs FOO-78 first"
2. Skip any relation already present in the tracker-edges block.
3. For each surviving relation, emit one edge object with:
   - "source": the blocking issue identifier
   - "target": the blocked issue identifier
   - "evidence": a short quotation or paraphrase showing why you inferred the edge
4. If you cannot identify any non-tracker relations, return {"edges":[]}.

## Done Criteria
- Output is a single JSON object on stdout matching the schema below.
- No prose before or after the JSON; no markdown code fences.
- Every emitted edge has all three fields populated.
- You never modified the tracker, the workspace, or any file.

## Output Schema
{
  "edges": [
    { "source": "FOO-12", "target": "FOO-34", "evidence": "issue body mentions \"blocked by FOO-12\"" }
  ]
}

## Ambiguity Policy
If the evidence is weak or contradictory, omit the edge. Operators prefer
the dashboard miss an inferred relation over showing a false one.
`
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
