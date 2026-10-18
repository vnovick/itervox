package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/vnovick/itervox/internal/templates/scaffold"
)

// IsKnownTemplateName re-exports scaffold.IsKnown for the init flag-handling
// path. todolist4 A.1.
func IsKnownTemplateName(name string) bool { return scaffold.IsKnown(name) }

// KnownTemplateNames returns the human-friendly list of supported template
// presets so flag errors can tell operators what's accepted.
func KnownTemplateNames() []string { return scaffold.Names() }

// templateFlagUsage is the --template help text: one line per starter
// template (#121).
func templateFlagUsage() string {
	var b strings.Builder
	b.WriteString("starter template. Without it, init writes the standard scaffold (implementer, reviewer, input-responder and deps-analyzer profiles, no automations):")
	for _, name := range scaffold.KnownTemplates {
		fmt.Fprintf(&b, "\n  %-20s %s", name, scaffold.Summary(name))
	}
	return b.String()
}

// initOptions are the init choices that shape the scaffold beyond tracker
// kind and runner: the starter template (#121; "" is the standard scaffold)
// and the preset ("" or presetCrossReview, #79). They combine.
type initOptions struct {
	Template string
	Preset   string
}

// planProfile is one agent profile a scaffold writes: its name, the backend
// it runs on and its allowed_actions ("" for none).
type planProfile struct {
	name    string
	runner  string
	actions string
}

// workflowPlan is what a template plus a preset put in the agent section and
// the automations of a generated workflow.
type workflowPlan struct {
	profiles []planProfile
	// review is how reviews run: "" (the AI Review button only), "chain"
	// (auto_review over reviewer_profiles) or presetCrossReview.
	review          string
	backendFallback bool
	automations     []string
}

func (p workflowPlan) index(name string) int {
	return slices.IndexFunc(p.profiles, func(pp planProfile) bool { return pp.name == name })
}

func (p workflowPlan) has(name string) bool { return p.index(name) >= 0 }

// Profile names the starter templates add to the standard four.
const (
	prReviewerProfileName = "pr-reviewer"
	qaProfileName         = "qa"
	reviewChain           = "chain"
)

// profileActions is the allowed_actions a scaffolded profile gets.
func profileActions(name string) string {
	switch name {
	case "input-responder":
		return "[comment, provide_input]"
	case initDepsAnalyzerProfileName:
		return "" // read-only with respect to the tracker
	default:
		return "[comment, comment_pr]"
	}
}

func newPlanProfile(name, runner string) planProfile {
	return planProfile{name: name, runner: runner, actions: profileActions(name)}
}

// planWorkflow decides the profiles, review mode, fallback and automations
// for a template, tracker kind and runner, then layers the preset on top.
func planWorkflow(trackerKind, runner string, opts initOptions) workflowPlan {
	var p workflowPlan
	add := func(names ...string) {
		for _, n := range names {
			p.profiles = append(p.profiles, newPlanProfile(n, runner))
		}
	}
	switch opts.Template {
	case scaffold.Minimal:
		add("implementer")
	case scaffold.Full:
		add(initAgentProfileNames...)
		p.review = reviewChain
		p.automations = []string{inputResponderAutomation, failureTriageAutomation}
	case scaffold.RateLimitFallback:
		add("implementer")
		p.profiles = append(p.profiles, newPlanProfile(fallbackImplementerProfile(runner), otherRunner(runner)))
		p.backendFallback = true
		p.automations = []string{rateLimitSwitchAutomation(runner)}
	case scaffold.PRReview:
		add("implementer", "reviewer", prReviewerProfileName)
		p.review = reviewChain
		p.automations = []string{reviewOpenedPRAutomation, reviewOnRequestAutomation}
	case scaffold.DailyQA:
		add("implementer", qaProfileName)
		p.automations = []string{dailyQAAutomation(trackerKind)}
	default:
		add(initAgentProfileNames...)
	}
	if opts.Preset == presetCrossReview {
		if !p.has("reviewer") {
			p.profiles = slices.Insert(p.profiles, 1, newPlanProfile("reviewer", runner))
		}
		p.profiles = slices.Insert(p.profiles, p.index("reviewer")+1, newPlanProfile(crossReviewerProfile(runner), otherRunner(runner)))
		p.review = presetCrossReview
	}
	return p
}

// fallbackImplementerProfile is the implementer on the other backend that
// the rate-limit-fallback template switches to.
func fallbackImplementerProfile(runner string) string {
	return "implementer-" + otherRunner(runner)
}

// writeAgentProfiles writes agent.profiles and the reviewer, deps-analyzer
// and backend-fallback settings that follow them.
func writeAgentProfiles(b *strings.Builder, p workflowPlan, runner string) {
	b.WriteString("  profiles:\n")
	for _, pp := range p.profiles {
		command, backend := initProfileCommand(pp.runner)
		b.WriteString("    " + pp.name + ":\n")
		b.WriteString("      command: " + command + "\n")
		if backend != "" {
			b.WriteString("      backend: " + backend + "\n")
		}
		b.WriteString("      soul_file: .itervox/agents/" + pp.name + "/SOUL.md\n")
		b.WriteString("      instructions_file: .itervox/agents/" + pp.name + "/INSTRUCTIONS.md\n")
		if pp.actions != "" {
			b.WriteString("      allowed_actions: " + pp.actions + "\n")
		}
	}

	hasReviewer := p.has("reviewer")
	switch {
	case p.review == presetCrossReview:
		cross := crossReviewerProfile(runner)
		b.WriteString("  # cross-review preset (#79): two read-only reviewers on different backends review every successful run.\n")
		b.WriteString("  reviewer_profile: " + cross + "   # Used by the AI Review button.\n")
		b.WriteString("  reviewer_profiles: [" + cross + ", reviewer]   # Each reviewer is a separate agent run.\n")
		b.WriteString("  review_quorum: any_block   # Changes are requested if either reviewer blocks.\n")
		b.WriteString("  auto_review: true\n")
	case p.review == reviewChain:
		b.WriteString("  reviewer_profile: reviewer         # Profile used by the AI Review button.\n")
		b.WriteString("  reviewer_profiles: [reviewer]      # Review chain run after each successful agent run; add profiles to fan out.\n")
		b.WriteString("  review_quorum: any_block           # any_block | majority | unanimous\n")
		b.WriteString("  auto_review: true\n")
	case hasReviewer:
		b.WriteString("  reviewer_profile: reviewer         # Profile used by the AI Review button and optional auto-review.\n")
	}
	if p.has(initDepsAnalyzerProfileName) {
		b.WriteString("  deps_analyzer_profile: " + initDepsAnalyzerProfileName + "    # Profile used by the dashboard \"Analyze dependencies\" button. Empty disables the button.\n")
	}
	if hasReviewer && p.review == "" {
		b.WriteString("  # auto_review: false               # Set to true to auto-review after each successful agent run. Coexists with workspace.auto_clear as of v0.2.0 — the clear fires on terminal tracker state, after the reviewer also completes.\n")
	}
	if p.backendFallback {
		writeBackendFallback(b, runner)
	} else {
		b.WriteString("  # backend_fallback:                 # CORE-054: reroute when a backend hits its usage limit (docs/configuration.md).\n")
		b.WriteString("  #   chain: [claude, codex]\n")
		b.WriteString("  #   profile_map:                     # each target must exist, be enabled, and run that backend\n")
		b.WriteString("  #     implementer: { codex: implementer-codex }\n")
		b.WriteString("  #   switch_back: at_reset            # at_reset | on_success | manual\n")
		b.WriteString("  #   min_dwell_minutes: 30\n")
	}
	if hasReviewer {
		writeReviewerPrompt(b)
	}
}

// writeBackendFallback writes the live agent.backend_fallback block of the
// rate-limit-fallback template: the implementer (and runs without a profile)
// move to the other backend while the runner's backend is limited.
func writeBackendFallback(b *strings.Builder, runner string) {
	other := otherRunner(runner)
	fallback := fallbackImplementerProfile(runner)
	b.WriteString("  backend_fallback:                   # CORE-054: when " + runner + " hits its usage limit, run on " + other + " until it resets.\n")
	b.WriteString("    chain: [" + runner + ", " + other + "]\n")
	b.WriteString("    profile_map:                       # each target must exist, be enabled, and run that backend\n")
	b.WriteString("      default: { " + other + ": " + fallback + " }   # runs without a profile (agent.command)\n")
	b.WriteString("      implementer: { " + other + ": " + fallback + " }\n")
	b.WriteString("    switch_back: at_reset              # at_reset | on_success | manual\n")
	b.WriteString("    min_dwell_minutes: 30\n")
}

func writeReviewerPrompt(b *strings.Builder) {
	b.WriteString("  reviewer_prompt: |\n")
	b.WriteString("    You are an AI code reviewer for issue {{ issue.identifier }}: {{ issue.title }}.\n")
	b.WriteString("\n")
	b.WriteString("    ## Your task\n")
	b.WriteString("\n")
	b.WriteString("    Review the change made for this issue. You are a read-only reviewer:\n")
	b.WriteString("    do not edit files, commit, push or move the issue.\n")
	b.WriteString("\n")
	b.WriteString("    1. Read the \"Changes to Review\" block (the diff against the base branch) and the latest handoff\n")
	b.WriteString("    2. Review for: correctness, test coverage, edge cases, security issues\n")
	b.WriteString("    3. Record your verdict as the \"Review Verdict (required)\" block describes: approve, or\n")
	b.WriteString("       block with the reasons and line comments the implementer must address\n")
	b.WriteString("\n")
	b.WriteString("    Be concise. Focus on real bugs, not style preferences.\n")
}

// writeAutomations writes the automations section: the template's live
// rules, or the commented reference when it has none.
func writeAutomations(b *strings.Builder, p workflowPlan) {
	b.WriteString("\n# ── automations (optional) ────────────────────────────────────────────\n")
	b.WriteString("# Cron and event-driven helper rules layered on top of your agent profiles.\n")
	b.WriteString("# Trigger types: cron, input_required, tracker_comment_added, issue_entered_state,\n")
	b.WriteString("#                issue_moved_to_backlog, run_failed, pr_opened, pr_merged, rate_limited,\n")
	b.WriteString("#                blockers_resolved, pr_check_completed, pr_synchronized, pr_review_submitted, pr_mergeable_changed.\n")
	b.WriteString("# Tip: an automation whose final output starts with [SILENT] posts no tracker\n")
	b.WriteString("# comment (run stays in the dashboard logs) — use it for scan-and-found-nothing crons.\n")
	b.WriteString("# Full reference: https://itervox.dev/guides/automations/\n")
	if len(p.automations) > 0 {
		b.WriteString("automations:\n")
		for _, a := range p.automations {
			b.WriteString(a)
		}
		return
	}
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
}

// The starter templates' automation rules. Instructions are Liquid; the
// trigger.* variables are listed in the automations guide.
const inputResponderAutomation = `  - id: input-responder
    enabled: true
    profile: input-responder
    trigger:
      type: input_required
    filter:
      input_context_regex: "continue|branch|which file|test command"
    policy:
      auto_resume: true
    instructions: |
      Answer narrow, low-risk unblocker questions.
      If the request is ambiguous, state the safest bounded assumption.
`

const failureTriageAutomation = `  - id: failure-triage
    enabled: true
    profile: input-responder
    trigger:
      type: run_failed
    instructions: |
      The last run on this issue failed and will not be retried: {{ trigger.error_message }}
      In one short comment, give the likely cause and the decision a human must make.
`

const reviewOpenedPRAutomation = `  - id: review-opened-pr
    enabled: true
    profile: pr-reviewer
    trigger:
      type: pr_opened
    instructions: |
      Review the pull request {{ trigger.pr_url }} ({{ trigger.pr_branch }} into {{ trigger.pr_base_branch }}).
`

const reviewOnRequestAutomation = `  - id: review-on-request
    enabled: true
    profile: pr-reviewer
    trigger:
      type: tracker_comment_added
    filter:
      body_regex: '^\s*/review\b'   # a comment that starts with /review
    instructions: |
      {{ trigger.comment_author_name }} asked for a review. Review the open pull request for this issue again.
`

func rateLimitSwitchAutomation(runner string) string {
	fallback := fallbackImplementerProfile(runner)
	return `  - id: rate-limit-switch
    enabled: true
    profile: ` + fallback + `
    trigger:
      type: rate_limited      # a run exhausted its retries on a usage limit
    policy:
      auto_switch: true
      switch_to_profile: ` + fallback + `
      switch_to_backend: ` + otherRunner(runner) + `
      cooldown_minutes: 30
    instructions: |
      {{ trigger.failed_profile }} stopped on a ` + runner + ` usage limit. Continue the issue from where that run stopped.
`
}

// completionStateFor is the state init's workflow moves a finished issue to.
func completionStateFor(trackerKind string) string {
	if trackerKind == "github" {
		return "in-review"
	}
	return "In Review"
}

func dailyQAAutomation(trackerKind string) string {
	return `  - id: daily-qa
    enabled: true
    profile: qa
    trigger:
      type: cron
      cron: "0 9 * * *"        # every day at 09:00
      timezone: UTC
    filter:
      states: ["` + completionStateFor(trackerKind) + `"]   # issues awaiting review
      limit: 10
    instructions: |
      Run your QA routine for this issue against its branch or pull request and comment the results.
      If nothing changed since your last QA comment on this issue, reply with exactly: [SILENT] no change
`
}

// writeTemplateAgentFiles writes SOUL.md and INSTRUCTIONS.md for every
// profile the plan scaffolds, keeping existing files, then the
// .itervox/.gitignore.
func writeTemplateAgentFiles(workflowPath, runner string, opts initOptions) error {
	baseDir := filepath.Dir(workflowPath)
	if baseDir == "" {
		baseDir = "."
	}
	// The profiles do not depend on the tracker kind.
	for _, pp := range planWorkflow("", runner, opts).profiles {
		dir := filepath.Join(baseDir, ".itervox", "agents", pp.name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("itervox init: create %s: %w", dir, err)
		}
		if err := writeFileIfMissing(filepath.Join(dir, "SOUL.md"), initSoulContent(pp.name)); err != nil {
			return err
		}
		if err := writeFileIfMissing(filepath.Join(dir, "INSTRUCTIONS.md"), initInstructionsContent(pp.name, pp.runner)); err != nil {
			return err
		}
	}
	return ensureItervoxGitignore(filepath.Join(baseDir, ".itervox"))
}
