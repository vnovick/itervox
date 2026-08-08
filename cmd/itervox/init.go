package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/atomicfs"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/profiles"
	"github.com/vnovick/itervox/internal/templates"
)

// generateWorkflow builds the WORKFLOW.md content from scanned repo info.
func generateWorkflow(trackerKind, runner string, info repoInfo) string {
	var b strings.Builder

	// ── frontmatter ───────────────────────────────────────────────────────────
	b.WriteString("---\n")
	b.WriteString("itervox_schema_version: 2\n\n")
	b.WriteString("tracker:\n")
	b.WriteString("  kind: " + trackerKind + "\n")

	switch trackerKind {
	case "linear":
		b.WriteString("  api_key: $LINEAR_API_KEY          # export LINEAR_API_KEY=lin_api_...\n")
	case "github":
		b.WriteString("  api_key: $GITHUB_TOKEN            # export GITHUB_TOKEN=ghp_...\n")
	}

	slug := "owner/repo"
	if info.Owner != "" && info.Repo != "" {
		if trackerKind == "linear" {
			slug = info.Repo + "-<slug>"
		} else {
			slug = info.Owner + "/" + info.Repo
		}
	}
	if trackerKind == "linear" {
		b.WriteString("  # project_slug: <slug>  # Optional — filter to one project.\n")
		b.WriteString("  #                        Select interactively via TUI (p) or web dashboard instead.\n")
		b.WriteString("  active_states: [\"Todo\", \"In Progress\"]\n")
		b.WriteString("  terminal_states: [\"Done\", \"Cancelled\", \"Duplicate\"]\n")
		b.WriteString("  working_state: \"In Progress\"     # State applied when an agent starts working.\n")
		b.WriteString("  #                                  # Set to \"\" to disable auto-transition.\n")
		b.WriteString("  completion_state: \"In Review\"     # State applied when the agent finishes.\n")
		b.WriteString("  backlog_states: [\"Backlog\"]        # Discard target; shown in TUI (b) and Kanban; not auto-dispatched.\n")
		b.WriteString("  # failed_state: \"Backlog\"       # State for issues that exhaust all retries.\n")
	} else {
		b.WriteString("  project_slug: " + slug + "\n")
		b.WriteString("  # GitHub uses labels to map states. Labels must exist in your repo.\n")
		b.WriteString("  # NOTE: GitHub Projects v2 'Status' field is separate from labels — Itervox\n")
		b.WriteString("  #       only reads labels. See README for Projects automation setup.\n")
		b.WriteString("  # Create them with: gh label create \"todo\" --color \"0075ca\" --repo " + slug + "\n")
		b.WriteString("  #                   gh label create \"in-progress\" --color \"e4e669\" --repo " + slug + "\n")
		b.WriteString("  #                   gh label create \"in-review\" --color \"d93f0b\" --repo " + slug + "\n")
		b.WriteString("  #                   gh label create \"done\" --color \"0e8a16\" --repo " + slug + "\n")
		b.WriteString("  #                   gh label create \"cancelled\" --color \"cccccc\" --repo " + slug + "\n")
		b.WriteString("  #                   gh label create \"backlog\" --color \"f9f9f9\" --repo " + slug + "\n")
		b.WriteString("  active_states: [\"todo\", \"in-progress\"]\n")
		b.WriteString("  terminal_states: [\"done\", \"cancelled\"]\n")
		b.WriteString("  working_state: \"in-progress\"  # Label applied when an agent starts.\n")
		b.WriteString("  #                               # MUST exist as a label in your repo.\n")
		b.WriteString("  #                               # Set to \"\" to disable, or reuse an active label.\n")
		b.WriteString("  completion_state: \"in-review\"  # Label applied when the agent finishes.\n")
		b.WriteString("  # backlog_states: [\"backlog\"]  # Shown in TUI (b) and Kanban; not auto-dispatched.\n")
		b.WriteString("  #                               # Must be an array — not a bare string.\n")
		b.WriteString("  backlog_states: [\"backlog\"]\n")
		b.WriteString("  # failed_state: \"backlog\"        # Label for issues that exhaust all retries.\n")
	}

	b.WriteString("\npolling:\n  interval_ms: 60000\n")

	b.WriteString("\nagent:\n")
	if runner == "codex" {
		b.WriteString("  command: codex\n")
		b.WriteString("  backend: codex\n")
	} else {
		b.WriteString("  command: claude\n")
	}
	b.WriteString("  max_turns: 60\n")
	b.WriteString("  max_concurrent_agents: 3\n")
	b.WriteString("  max_retries: 5\n")
	b.WriteString("  turn_timeout_ms: 3600000\n")
	b.WriteString("  read_timeout_ms: 120000\n")
	b.WriteString("  stall_timeout_ms: 300000\n")
	b.WriteString("  profiles:\n")
	for _, profile := range initAgentProfileNames {
		command, backend := initProfileCommand(runner)
		b.WriteString("    " + profile + ":\n")
		b.WriteString("      command: " + command + "\n")
		if backend != "" {
			b.WriteString("      backend: " + backend + "\n")
		}
		b.WriteString("      soul_file: .itervox/agents/" + profile + "/SOUL.md\n")
		b.WriteString("      instructions_file: .itervox/agents/" + profile + "/INSTRUCTIONS.md\n")
		switch profile {
		case "reviewer":
			b.WriteString("      allowed_actions: [comment, comment_pr]\n")
		case "input-responder":
			b.WriteString("      allowed_actions: [comment, provide_input]\n")
		case initDepsAnalyzerProfileName:
			// Dependency analyzer is read-only with respect to the tracker;
			// it has no allowed_actions.
		default:
			b.WriteString("      allowed_actions: [comment, comment_pr]\n")
		}
	}

	// Reviewer prompt — used when a reviewer worker is dispatched (via auto_review or AI Review button).
	// Uses the reviewer_prompt template instead of the main WORKFLOW.md body.
	b.WriteString("  reviewer_profile: reviewer         # Profile used by the AI Review button and optional auto-review.\n")
	b.WriteString("  deps_analyzer_profile: " + initDepsAnalyzerProfileName + "    # Profile used by the dashboard \"Analyze dependencies\" button. Empty disables the button.\n")
	b.WriteString("  # auto_review: false               # Set to true to auto-review after each successful agent run. Coexists with workspace.auto_clear as of v0.2.0 — the clear fires on terminal tracker state, after the reviewer also completes.\n")
	b.WriteString("  reviewer_prompt: |\n")
	b.WriteString("    You are an AI code reviewer for issue {{ issue.identifier }}: {{ issue.title }}.\n")
	b.WriteString("\n")
	b.WriteString("    ## Your task\n")
	b.WriteString("\n")
	b.WriteString("    Review the pull request created for this issue.\n")
	b.WriteString("\n")
	b.WriteString("    1. Run `gh pr diff` to read the PR changes\n")
	b.WriteString("    2. Review for: correctness, test coverage, edge cases, security issues, code style\n")
	b.WriteString("    3. If you find problems:\n")
	b.WriteString("       - Fix them directly in the workspace\n")
	b.WriteString("       - Commit and push: `git add -A && git commit -m \"fix: reviewer corrections\" && git push`\n")
	b.WriteString("       - Post a comment on the tracker issue summarising what you fixed\n")
	b.WriteString("    4. If the PR is clean:\n")
	b.WriteString("       - Post an approval comment: \"AI review passed — no issues found\"\n")
	b.WriteString("\n")
	b.WriteString("    Be concise. Focus on real bugs, not style preferences.\n")

	// Write discovered models so the dashboard profile editor has suggestions.
	if len(info.ClaudeModels) > 0 || len(info.CodexModels) > 0 {
		b.WriteString("  available_models:\n")
		if len(info.ClaudeModels) > 0 {
			b.WriteString("    claude:\n")
			for _, m := range info.ClaudeModels {
				fmt.Fprintf(&b, "      - { id: %q, label: %q }\n", m.ID, m.Label)
			}
		}
		if len(info.CodexModels) > 0 {
			b.WriteString("    codex:\n")
			for _, m := range info.CodexModels {
				fmt.Fprintf(&b, "      - { id: %q, label: %q }\n", m.ID, m.Label)
			}
		}
	}

	cloneURL := info.CloneURL
	if cloneURL == "" {
		cloneURL = "git@github.com:owner/" + info.ProjectName + ".git"
	}
	b.WriteString("\nworkspace:\n")
	b.WriteString("  root: ~/.itervox/workspaces/" + info.ProjectName + "\n")
	b.WriteString("  worktree: true\n")
	b.WriteString("  clone_url: " + cloneURL + "\n")
	b.WriteString("  base_branch: " + info.DefaultBranch + "\n")

	b.WriteString("\nhooks:\n")
	b.WriteString("  # after_create and before_run are no longer needed for clone/reset —\n")
	b.WriteString("  # Itervox maintains a bare clone and creates worktrees automatically.\n")
	b.WriteString("  # Add custom hooks here if your project needs extra setup.\n")
	b.WriteString("  # before_run runs once per worker attempt; after_run runs after each turn.\n")
	b.WriteString("  # after_create: |\n")
	b.WriteString("  #   pnpm install --frozen-lockfile\n")
	b.WriteString("  # before_run: |\n")
	b.WriteString("  #   make prepare-agent-workspace\n")
	b.WriteString("  # after_run: |\n")
	b.WriteString("  #   git status --short\n")
	b.WriteString("  # before_remove: |\n")
	b.WriteString("  #   tar -czf ../workspace-backup.tgz .\n")

	// port: 0 = OS picks a free port. Operators running a single itervox can
	// pin a fixed port (e.g. 8090) here; running two itervox daemons in
	// parallel in different repos is the common case, and `0` makes it
	// just work. The actual bound URL is written to .itervox/dashboard_url
	// (read by the Vite dev proxy) and surfaced in HEARTBEAT.md + the
	// startup banner.
	b.WriteString("\nserver:\n  port: 0\n")

	b.WriteString("\n# ── automations (optional) ────────────────────────────────────────────\n")
	b.WriteString("# Cron and event-driven helper rules layered on top of your agent profiles.\n")
	b.WriteString("# Trigger types: cron, input_required, tracker_comment_added, issue_entered_state,\n")
	b.WriteString("#                issue_moved_to_backlog, run_failed, pr_opened, pr_merged,\n")
	b.WriteString("#                rate_limited, blockers_resolved.\n")
	b.WriteString("# Tip: an automation whose final output starts with [SILENT] posts no tracker\n")
	b.WriteString("# comment (run stays in the dashboard logs) — use it for scan-and-found-nothing crons.\n")
	b.WriteString("# Full reference: https://itervox.dev/guides/automations/\n")
	b.WriteString("# automations:\n")
	b.WriteString("#   - id: input-responder\n")
	b.WriteString("#     enabled: true\n")
	b.WriteString("#     profile: input-responder   # must match an entry under agent.profiles\n")
	b.WriteString("#     trigger:\n")
	b.WriteString("#       type: input_required\n")
	b.WriteString("#     filter:\n")
	b.WriteString("#       input_context_regex: \"continue|branch|which file|test command\"\n")
	b.WriteString("#     policy:\n")
	b.WriteString("#       auto_resume: true\n")
	b.WriteString("#     instructions: |\n")
	b.WriteString("#       Answer narrow, low-risk unblocker questions.\n")
	b.WriteString("#       If the request is ambiguous, state the safest bounded assumption.\n")

	b.WriteString("---\n\n")

	// ── prompt body ───────────────────────────────────────────────────────────
	b.WriteString("You are an expert engineer working on **" + info.ProjectName + "**.\n\n")

	b.WriteString("## Your issue\n\n")
	b.WriteString("**{{ issue.identifier }}: {{ issue.title }}**\n\n")
	b.WriteString("{% if issue.description %}\n{{ issue.description }}\n{% endif %}\n\n")
	b.WriteString("Issue URL: {{ issue.url }}\n\n")
	b.WriteString("{% if issue.comments %}\n## Comments\n\n")
	b.WriteString("{% for comment in issue.comments %}\n**{{ comment.author_name }}**: {{ comment.body }}\n\n{% endfor %}\n{% endif %}\n\n")
	b.WriteString("---\n\n")

	// CLAUDE.md or conventions placeholder
	if info.HasClaudeMD {
		b.WriteString("## Project Conventions\n\n")
		b.WriteString("This project has a `CLAUDE.md`. Read it before touching any code:\n\n")
		b.WriteString("```bash\ncat CLAUDE.md\n```\n\n")
		b.WriteString("Follow all conventions, architecture rules, and preferences documented there.\n\n")
		b.WriteString("---\n\n")
	}

	// AGENTS.md — multi-agent configuration
	if info.HasAgentsMD {
		b.WriteString("## Multi-Agent Configuration\n\n")
		b.WriteString("This project has an `AGENTS.md`. Read it for multi-agent conventions and coordination rules:\n\n")
		b.WriteString("```bash\ncat AGENTS.md\n```\n\n")
		b.WriteString("---\n\n")
	}

	b.WriteString("## Step 1 — Explore before touching anything\n\n")
	b.WriteString("Read the issue. Explore the relevant code before making changes.\n\n")
	if info.HasClaudeMD {
		b.WriteString("Re-read `CLAUDE.md` if you are unsure about conventions.\n\n")
	}
	b.WriteString("---\n\n")

	b.WriteString("## Step 2 — Create a branch\n\n")
	b.WriteString("```bash\n")
	if trackerKind == "linear" {
		b.WriteString("git checkout -b {{ issue.branch_name | default: issue.identifier | downcase }}\n")
	} else {
		b.WriteString("git checkout -b {{ issue.branch_name | default: issue.identifier | replace: \"#\", \"\" | downcase }}\n")
	}
	b.WriteString("```\n\n---\n\n")

	b.WriteString("## Step 3 — Implement\n\n")
	b.WriteString("Read `CLAUDE.md` to understand project conventions before writing any code:\n\n")
	b.WriteString("```bash\ncat CLAUDE.md\n```\n\n")
	b.WriteString("If `CLAUDE.md` does not exist, explore the repository structure, identify the dominant patterns and conventions, create `CLAUDE.md` documenting them, and then implement.\n\n")
	if len(info.Stacks) > 0 {
		b.WriteString("Detected stacks: ")
		for i, s := range info.Stacks {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(s.Name)
		}
		b.WriteString(". Follow their conventions as documented in `CLAUDE.md`.\n\n")
	}
	b.WriteString("---\n\n")

	b.WriteString("## Step 4 — Run checks\n\n")
	b.WriteString("Read `CLAUDE.md` for the project's test and lint commands. If `CLAUDE.md` does not exist, discover the check commands by exploring the repository (look for `Makefile`, `package.json` scripts, CI config, etc.).\n\n")
	b.WriteString("```bash\n")
	if len(info.Stacks) > 0 {
		for _, s := range info.Stacks {
			b.WriteString("# " + s.Name + "\n")
			for _, cmd := range s.Commands {
				b.WriteString(cmd + "\n")
			}
		}
	} else {
		b.WriteString("# Run the project's test and lint commands (check CLAUDE.md or discover from repo)\n")
	}
	b.WriteString("```\n\n---\n\n")

	b.WriteString("## Step 5 — Commit and open PR\n\n")
	b.WriteString("```bash\n")
	b.WriteString("git add <specific files>\n")
	b.WriteString("git commit -m \"feat: <description> ({{ issue.identifier }})\"\n")
	b.WriteString("git push -u origin HEAD\n")
	b.WriteString("gh pr create --title \"<title> ({{ issue.identifier }})\" --body \"Closes {{ issue.url }}\"\n")
	b.WriteString("```\n\n---\n\n")

	b.WriteString("## Step 6 — Post PR link to tracker\n\n")
	b.WriteString("After the PR is open, post its URL as a comment on the tracker issue so it is visible in ")
	if trackerKind == "linear" {
		b.WriteString("Linear:\n\n")
		b.WriteString("```bash\n")
		b.WriteString("PR_URL=$(gh pr view --json url -q .url)\n")
		b.WriteString("curl -s -X POST https://api.linear.app/graphql \\\n")
		b.WriteString("  -H \"Authorization: $LINEAR_API_KEY\" \\\n")
		b.WriteString("  -H \"Content-Type: application/json\" \\\n")
		b.WriteString("  -d \"{\\\"query\\\":\\\"mutation { commentCreate(input: { issueId: \\\\\\\"{{ issue.id }}\\\\\\\", body: \\\\\\\"PR: ${PR_URL}\\\\\\\" }) { success } }\\\"}\"\n")
		b.WriteString("```\n\n---\n\n")
	} else {
		b.WriteString("GitHub:\n\n")
		b.WriteString("```bash\n")
		b.WriteString("PR_URL=$(gh pr view --json url -q .url)\n")
		b.WriteString("gh issue comment {{ issue.identifier | remove: \"#\" }} --body \"🤖 Opened PR: ${PR_URL}\"\n")
		b.WriteString("```\n\n---\n\n")
	}

	b.WriteString("## Rules\n\n")
	b.WriteString("- Complete the issue fully before stopping.\n")
	b.WriteString("- Never commit `.env` files or secrets.\n")
	if info.HasClaudeMD {
		b.WriteString("- All conventions in `CLAUDE.md` apply — do not deviate without a documented reason.\n")
	}
	b.WriteString("\n")

	// Append the static "Asking for human input" block. Sourced from the
	// templates package so the sentinel contract has a single source of truth
	// instead of drifting between inline strings here and the markdown files.
	b.Write(templates.HumanInput)

	return b.String()
}

// runInit scans the current (or specified) directory for repo metadata and
// generates a WORKFLOW.md pre-filled with discovered values.
func runInit(args []string) {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	trackerKind := fs.String("tracker", "", "tracker kind: linear or github (required)")
	runner := fs.String("runner", "claude", "default runner backend: claude or codex")
	output := fs.String("output", "WORKFLOW.md", "output file path")
	workflowPath := fs.String("workflow", "WORKFLOW.md", "workflow path for --update")
	dir := fs.String("dir", ".", "directory to scan for repo metadata")
	force := fs.Bool("force", false, "overwrite output file if it already exists")
	update := fs.Bool("update", false, "migrate an existing WORKFLOW.md to the latest schema")
	templateName := fs.String("template", "minimal", "scaffold preset: minimal | full | rate-limit-fallback | pr-review | daily-qa")
	// --server-port is opt-in for --update. When set (>= 0), the migration
	// rewrites server.port in WORKFLOW.md. The recommended value for the
	// "two itervox daemons in different repos" workflow is 0 (OS picks a
	// free port). Negative = leave the field unchanged.
	serverPort := fs.Int("server-port", -1, "for --update: rewrite server.port (use 0 to let the OS pick a free port; -1 = leave unchanged)")
	// --analyze / --no-analyze override the placeholder-env heuristic that
	// gates the synchronous one-shot dependency-analysis pass on fresh init.
	// Default (analyzeMode == "auto") preserves the existing behaviour: run
	// the pass when the env file looks populated; skip when it still has the
	// placeholder hex chars from the scaffold.
	analyzeMode := fs.String("analyze", "auto", "init-time dependency analysis: auto | always | never")
	_ = fs.Parse(args)
	switch *analyzeMode {
	case "auto", "always", "never":
	default:
		fmt.Fprintf(os.Stderr, "itervox init: invalid --analyze %q (auto|always|never)\n", *analyzeMode)
		fatalExit(2)
	}
	if !IsKnownTemplateName(*templateName) {
		fmt.Fprintf(os.Stderr, "itervox init: unknown --template %q (valid: %s)\n", *templateName, strings.Join(KnownTemplateNames(), ", "))
		fatalExit(2)
	}
	_ = *templateName // currently scaffolds the same default; future presets emit different blocks.

	if *update {
		result, err := migrateWorkflowToSchema2(*workflowPath, *force, time.Now().UTC())
		if err != nil {
			fmt.Fprintf(os.Stderr, "itervox init --update: %v\n", err)
			fatalExit(1)
		}
		if result.Changed {
			fmt.Printf("itervox init --update: migrated %s to schema %d\n", *workflowPath, config.LatestWorkflowSchemaVersion)
			// gaps_11 G-19 — tell the operator the .bak is disposable once
			// the migration is confirmed, so stale backups don't linger.
			fmt.Printf("itervox init --update: backup written to %s — review the migrated workflow, then remove the backup once the migration is confirmed\n", result.BackupPath)
			if len(result.Profiles) > 0 {
				fmt.Printf("itervox init --update: migrated profiles: %s\n", strings.Join(result.Profiles, ", "))
			}
		} else {
			fmt.Printf("itervox init --update: %s already uses schema %d\n", *workflowPath, config.LatestWorkflowSchemaVersion)
		}
		if *serverPort >= 0 {
			if err := rewriteServerPort(*workflowPath, *serverPort); err != nil {
				fmt.Fprintf(os.Stderr, "itervox init --update: rewrite server.port: %v\n", err)
				fatalExit(1)
			}
			fmt.Printf("itervox init --update: server.port set to %d in %s\n", *serverPort, *workflowPath)
		}
		for _, warning := range result.Warnings {
			fmt.Fprintf(os.Stderr, "itervox init --update: warning: %s\n", warning)
		}
		return
	}

	switch *trackerKind {
	case "linear", "github":
		// valid
	case "":
		fmt.Fprintln(os.Stderr, "itervox init: --tracker is required (linear or github)")
		fs.Usage()
		fatalExit(1)
	default:
		fmt.Fprintf(os.Stderr, "itervox init: unknown tracker %q (supported: linear, github)\n", *trackerKind)
		fatalExit(1)
	}

	switch *runner {
	case "claude", "codex":
		// valid
	default:
		fmt.Fprintf(os.Stderr, "itervox init: unknown runner %q (supported: claude, codex)\n", *runner)
		fatalExit(1)
	}

	// Validate that the selected runner CLI is available on PATH.
	switch *runner {
	case "claude":
		if err := agent.ValidateClaudeCLI(); err != nil {
			fmt.Fprintf(os.Stderr, "itervox init: %v\n", err)
			fatalExit(1)
		}
	case "codex":
		if err := agent.ValidateCodexCLI(); err != nil {
			fmt.Fprintf(os.Stderr, "itervox init: %v\n", err)
			fatalExit(1)
		}
	}

	if _, err := os.Stat(*output); err == nil && !*force {
		fmt.Fprint(os.Stderr, existingWorkflowInitMessage(*output))
		fatalExit(1)
	}

	fmt.Printf("itervox init: scanning %s...\n", *dir)
	info := scanRepo(*dir)

	if info.RemoteURL != "" {
		fmt.Printf("  git remote : %s\n", info.RemoteURL)
	}
	fmt.Printf("  branch     : %s\n", info.DefaultBranch)
	fmt.Printf("  runner     : %s\n", *runner)
	if info.HasClaudeMD {
		fmt.Printf("  CLAUDE.md  : found — prompt will reference it\n")
	} else {
		fmt.Printf("  CLAUDE.md  : not found — add one for best results\n")
	}
	if info.HasAgentsMD {
		fmt.Printf("  AGENTS.md  : found — prompt will reference it\n")
	}
	for _, s := range info.Stacks {
		fmt.Printf("  stack      : %s (%s)\n", s.Name, strings.Join(s.Commands, ", "))
	}

	// Discover available models from provider APIs (best-effort).
	fmt.Printf("itervox init: discovering available models...\n")
	info.ClaudeModels = agent.ListClaudeModels()
	info.CodexModels = agent.ListCodexModels()
	fmt.Printf("  models     : %d claude, %d codex\n", len(info.ClaudeModels), len(info.CodexModels))

	content := generateWorkflow(*trackerKind, *runner, info)

	if err := atomicfs.WriteFile(*output, []byte(content), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "itervox init: write %s: %v\n", *output, err)
		fatalExit(1)
	}
	fmt.Printf("itervox init: wrote %s\n", *output)
	if err := writeInitAgentFiles(*output, *runner); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		fatalExit(1)
	}
	fmt.Printf("itervox init: wrote .itervox/agents profiles\n")
	// P0-A — scaffold built-in profile files to disk so operators see them
	// in version control next to their custom profiles. Idempotent; uses
	// writeFileIfMissing semantics so operator edits are preserved.
	if err := writeBuiltinProfileFilesIfMissing(*output, profiles.Names()); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		fatalExit(1)
	}

	// Create .itervox/.env if it doesn't exist.
	outputDir := filepath.Dir(*output)
	if outputDir == "" {
		outputDir = "."
	}
	envDir := filepath.Join(outputDir, ".itervox")
	envPath := filepath.Join(envDir, ".env")
	if _, err := os.Stat(envPath); os.IsNotExist(err) {
		_ = os.MkdirAll(envDir, 0o755)
		var envContent string
		switch *trackerKind {
		case "linear":
			envContent = "# Itervox environment — this file is gitignored.\n# See WORKFLOW.md for which variables are referenced.\nLINEAR_API_KEY=lin_api_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx\n"
		case "github":
			envContent = "# Itervox environment — this file is gitignored.\n# See WORKFLOW.md for which variables are referenced.\nGITHUB_TOKEN=ghp_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx\n"
		}
		if err := os.WriteFile(envPath, []byte(envContent), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "itervox init: write %s: %v\n", envPath, err)
		} else {
			fmt.Printf("itervox init: wrote %s\n", envPath)
		}
	}

	// Ensure .itervox runtime files are gitignored and the root .gitignore
	// has carve-outs for agent / handoff dirs (no-op if root .gitignore
	// doesn't broadly ignore .itervox/).
	if err := finalizeItervoxGitignore(envDir); err != nil {
		fmt.Fprintf(os.Stderr, "itervox init: %v\n", err)
	}

	// Phase 1.3 — best-effort one-shot dependency analysis pass. Default
	// behaviour ("auto") skips when the .env stub still has placeholder hex
	// chars (the operator hasn't filled in credentials yet); the dashboard's
	// "Analyze dependencies" button runs the pass instead. The --analyze
	// flag lets operators override:
	//   auto    (default) — run only when env file is populated
	//   always           — run unconditionally; fail loud on missing keys
	//   never            — skip; operator will run the pass later
	shouldAnalyze := decideInitDepsAnalysis(*analyzeMode, envPath)
	switch {
	case !shouldAnalyze && *analyzeMode == "never":
		fmt.Printf("itervox init: skipping initial dependency analysis (--analyze=never)\n")
	case !shouldAnalyze:
		fmt.Printf("itervox init: skipping initial dependency analysis (fill in %s, then run \"Analyze dependencies\" from the dashboard)\n", envPath)
	default:
		// Load the freshly-written .env so the analysis pass sees the same
		// secrets the daemon will see on first launch.
		loadDotEnv()
		issueCount, analyzedCount, edgeCount, sidecarPath, guarded, err := runInitDepsAnalysis(*output, "auto")
		switch {
		case err != nil:
			fmt.Fprintf(os.Stderr, "itervox init: WARNING: dependency analysis pass skipped (%v); run \"itervox deps analyze\" or click \"Analyze dependencies\" from the dashboard once credentials are configured.\n", err)
		case guarded:
			// First-run init against an empty fetch with a pre-existing
			// sidecar (e.g. --update re-running init) — nothing was
			// written; see the empty-fetch guard in runInitDepsAnalysis.
			fmt.Printf("itervox init: fetch returned no issues; refusing to overwrite %d inferred edge(s) already in %s (sidecar left unchanged)\n", edgeCount, sidecarPath)
		default:
			// #52 IssuesScanned honesty — see deps.go's runDepsAnalyze for
			// the same phrasing.
			fmt.Printf("itervox init: scanned %d issue(s) (%d analyzed, %d revalidated); inferred %d edge(s) (wrote %s)\n",
				issueCount, analyzedCount, issueCount-analyzedCount, edgeCount, sidecarPath)
		}
	}

	fmt.Printf("Next steps:\n")
	fmt.Printf("  1. Edit %s — fill in your API key\n", envPath)
	runCmd := "itervox"
	if *output != "WORKFLOW.md" {
		runCmd = "itervox -workflow " + *output
	}
	if *trackerKind == "linear" {
		fmt.Printf("  2. Run: %s\n", runCmd)
		fmt.Printf("  3. Select a project via the TUI (press p) or the web dashboard\n")
	} else {
		fmt.Printf("  2. Run: %s\n", runCmd)
	}
}

func existingWorkflowInitMessage(output string) string {
	return fmt.Sprintf("itervox init: %s already exists; not overwriting.\nExisting workflows may need migration:\n  itervox init --update --workflow %s\nUse --force to overwrite instead.\n", output, output)
}
