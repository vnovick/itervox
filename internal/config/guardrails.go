package config

import (
	"fmt"
	"log/slog"
	"path"
	"slices"
	"strings"
)

// DefaultGuardrailApprovalCommands is guardrails.approval_commands when the
// key is absent (#204): deploy and publish commands an agent may only run
// after a human approves them. Each entry is a sequence of glob tokens: the
// first matches the program name, the rest must appear among its arguments
// in order (other arguments may sit between them).
var DefaultGuardrailApprovalCommands = []string{
	"vercel --prod",
	"fly deploy",
	"kubectl apply",
	"terraform apply",
	"gcloud deploy",
	"npm publish",
	"cargo publish",
}

// DefaultGuardrailHumanLane is guardrails.lanes.human when the key is
// absent: the files that decide HOW CI checks a change rather than what it
// checks — CI workflows and pipelines, ownership rules, Itervox's own
// configuration (not the per-run handoffs it commits), and common test-runner,
// coverage and lint configuration. A push that changes one waits for a human
// approval, and merge_pr never merges the issue's pull request. Deliberately
// conservative: build manifests (go.mod, package.json, pyproject.toml, whose
// tool sections cannot be told apart by path) are not in it.
var DefaultGuardrailHumanLane = []string{
	".github/**",
	".gitlab-ci.yml",
	".circleci/**",
	".itervox/**",
	"!.itervox/handoff/**",
	"WORKFLOW.md",
	"CODEOWNERS",
	".github/CODEOWNERS",
	"jest.config.*",
	"vitest.config.*",
	"pytest.ini",
	".coveragerc",
	"codecov.yml",
	".golangci.yml",
	"Makefile",
}

// GuardrailLanesConfig is guardrails.lanes: which changed files take an
// agent's push out of the auto lane. Patterns are gitignore-style globs
// over repository-relative paths (internal/guardrails Lanes: `**` spans
// directories, a bare name matches at any depth, `!` excludes).
type GuardrailLanesConfig struct {
	// Human: a push changing one of these waits for a human approval, and
	// merge_pr refuses to merge the issue's pull request. Absent:
	// DefaultGuardrailHumanLane; a list replaces it; [] turns the lane off.
	Human []string
	// SecondOpinion: a push changing one of these goes through, and merge_pr
	// then needs an approving review from a reviewer on a different backend
	// than the writer's. Default empty.
	SecondOpinion []string
}

// GuardrailsConfig is the guardrails: block (#204): limits on what an agent
// started by Itervox may do with git, the GitHub/GitLab CLIs and deploy
// commands. Applied to dispatched local runs and to tmux sessions started
// with "New session"; never to hand-started sessions. Guidance, not a
// sandbox: an agent that calls git by another path bypasses it. Read-only
// after startup.
type GuardrailsConfig struct {
	// Enabled turns the guardrails on (default true).
	Enabled bool
	// AllowForcePush lets an agent force-push and delete its own issue
	// branch. Pushes to other branches and tag pushes stay refused.
	// Default false.
	AllowForcePush bool
	// ApprovalCommands are the command patterns that need a human approval
	// before they run. Absent: DefaultGuardrailApprovalCommands. A list
	// replaces the defaults, so write them out to keep them; an empty list
	// turns approvals off.
	ApprovalCommands []string
	// Lanes classifies what a push changes (guardrails.lanes).
	Lanes GuardrailLanesConfig
}

// parseGuardrailsConfig reads the guardrails: block. A pattern whose program
// token is a path or a glob cannot be intercepted on PATH; it is kept (it can
// still match a shimmed program's arguments) but warned about.
func parseGuardrailsConfig(m map[string]any) GuardrailsConfig {
	c := GuardrailsConfig{
		Enabled:        boolField(m, "enabled", true),
		AllowForcePush: boolField(m, "allow_force_push", false),
	}
	raw := strSliceField(m, "approval_commands", nil)
	if v, set := m["approval_commands"]; !set || v == nil {
		raw = DefaultGuardrailApprovalCommands
	}
	for _, p := range raw {
		p = strings.Join(strings.Fields(p), " ")
		if p == "" || slices.Contains(c.ApprovalCommands, p) {
			continue
		}
		if program, _, _ := strings.Cut(p, " "); strings.ContainsAny(program, "/*?[") {
			slog.Warn("config: guardrails.approval_commands entry names its program by path or glob; only a bare program name can be intercepted",
				"pattern", p)
		}
		c.ApprovalCommands = append(c.ApprovalCommands, p)
	}
	c.Lanes = parseGuardrailLanes(nestedMap(m, "lanes"))
	return c
}

// parseGuardrailLanes reads guardrails.lanes. An absent or null human list
// is the default; a list (even []) replaces it. Patterns are trimmed and
// de-duplicated.
func parseGuardrailLanes(m map[string]any) GuardrailLanesConfig {
	human := strSliceField(m, "human", nil)
	if v, set := m["human"]; !set || v == nil {
		human = DefaultGuardrailHumanLane
	}
	return GuardrailLanesConfig{
		Human:         cleanLanePatterns(human),
		SecondOpinion: cleanLanePatterns(strSliceField(m, "second_opinion", nil)),
	}
}

func cleanLanePatterns(raw []string) []string {
	var out []string
	for _, p := range raw {
		if p = strings.TrimSpace(p); p != "" && !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	return out
}

// ValidateGuardrailLanes rejects a guardrails.lanes pattern that can never
// match (a malformed `[` class, or a bare `!`): a lane pattern that silently
// matches nothing would let the files it names through.
func ValidateGuardrailLanes(l GuardrailLanesConfig) error {
	for _, lane := range []struct {
		name string
		list []string
	}{{"human", l.Human}, {"second_opinion", l.SecondOpinion}} {
		for _, p := range lane.list {
			body := strings.Trim(strings.TrimPrefix(p, "!"), "/")
			if body == "" {
				return fmt.Errorf("guardrails.lanes.%s: pattern %q names no path", lane.name, p)
			}
			for _, seg := range strings.Split(strings.ReplaceAll(body, `\`, "/"), "/") {
				if _, err := path.Match(seg, ""); err != nil {
					return fmt.Errorf("guardrails.lanes.%s: invalid pattern %q: %w", lane.name, p, err)
				}
			}
		}
	}
	return nil
}
