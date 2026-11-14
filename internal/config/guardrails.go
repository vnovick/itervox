package config

import (
	"fmt"
	"log/slog"
	"path"
	"regexp"
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
	// before they run, whatever the environment: the plain-string entries
	// of guardrails.approval_commands (and object entries without when_env).
	// Absent: DefaultGuardrailApprovalCommands. A list replaces the
	// defaults, so write them out to keep them; an empty list turns
	// approvals off.
	ApprovalCommands []string
	// EnvApprovalCommands are the object entries of
	// guardrails.approval_commands that carry when_env: the pattern needs a
	// human approval only when every named environment variable matches its
	// glob in the environment the command runs with. Default empty.
	EnvApprovalCommands []GuardrailEnvApproval
	// Lanes classifies what a push changes (guardrails.lanes).
	Lanes GuardrailLanesConfig
}

// GuardrailEnvApproval is one environment-scoped approval_commands entry:
//
//   - match: "terraform apply"
//     when_env: {TF_WORKSPACE: "prod*"}
//
// Match has the plain-entry syntax. WhenEnv maps an environment variable to
// a glob (`*` any run of characters, `?` one); a leading `!` negates it. An
// unset variable is the empty string, so `!dev*` also holds a command run
// with the variable unset. Every entry must match for the rule to apply.
type GuardrailEnvApproval struct {
	// Match is the command pattern (approval_commands syntax).
	Match string
	// WhenEnv maps a variable name to the glob its value must match.
	WhenEnv map[string]string
	// invalid is why the entry could not be read; ValidateGuardrailApprovals
	// reports it (an entry is never dropped silently: that would let the
	// command it names run without an approval).
	invalid string
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
		if p = cleanApprovalPattern(p); p != "" && !slices.Contains(c.ApprovalCommands, p) {
			c.ApprovalCommands = append(c.ApprovalCommands, p)
		}
	}
	// Object entries ({match, when_env}); strSliceField keeps only strings,
	// so the plain entries above are read exactly as before.
	if list, ok := m["approval_commands"].([]any); ok {
		for _, item := range list {
			obj, isMap := toStringMap(item)
			if !isMap {
				continue
			}
			e := parseEnvApproval(obj)
			if len(e.WhenEnv) == 0 && e.invalid == "" {
				// No condition: a plain entry written as an object.
				if !slices.Contains(c.ApprovalCommands, e.Match) {
					c.ApprovalCommands = append(c.ApprovalCommands, e.Match)
				}
				continue
			}
			c.EnvApprovalCommands = append(c.EnvApprovalCommands, e)
		}
	}
	c.Lanes = parseGuardrailLanes(nestedMap(m, "lanes"))
	return c
}

// cleanApprovalPattern collapses a pattern's whitespace and warns about a
// program token no shim can intercept.
func cleanApprovalPattern(p string) string {
	p = strings.Join(strings.Fields(p), " ")
	if program, _, _ := strings.Cut(p, " "); p != "" && strings.ContainsAny(program, "/*?[") {
		slog.Warn("config: guardrails.approval_commands entry names its program by path or glob; only a bare program name can be intercepted",
			"pattern", p)
	}
	return p
}

// toStringMap returns item as a map with string keys, when it is one.
func toStringMap(item any) (map[string]any, bool) {
	switch v := item.(type) {
	case map[string]any:
		return v, true
	case map[any]any:
		out := make(map[string]any, len(v))
		for k, val := range v {
			out[fmt.Sprint(k)] = val
		}
		return out, true
	}
	return nil, false
}

// parseEnvApproval reads one {match, when_env} entry. Problems are kept on
// the entry for ValidateGuardrailApprovals.
func parseEnvApproval(obj map[string]any) GuardrailEnvApproval {
	var e GuardrailEnvApproval
	for k := range obj {
		if k != "match" && k != "when_env" {
			e.invalid = fmt.Sprintf("unknown key %q (want match, when_env)", k)
		}
	}
	match, ok := obj["match"].(string)
	if !ok && obj["match"] != nil {
		e.invalid = "match must be a string"
	}
	e.Match = cleanApprovalPattern(match)
	if e.Match == "" && e.invalid == "" {
		e.invalid = "match names no command"
	}
	if raw, set := obj["when_env"]; set && raw != nil {
		env, isMap := toStringMap(raw)
		if !isMap {
			e.invalid = "when_env must be a map of variable name to glob"
			return e
		}
		e.WhenEnv = make(map[string]string, len(env))
		for name, v := range env {
			switch v.(type) {
			case string, int, int64, uint64, float64, bool:
				e.WhenEnv[name] = fmt.Sprint(v)
			default:
				e.invalid = fmt.Sprintf("when_env.%s must be a string glob", name)
			}
		}
	}
	return e
}

// envVarNameRe is a portable environment variable name.
var envVarNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ValidateGuardrailApprovals rejects an approval_commands object entry that
// cannot be applied as written (no match, an unknown key, a when_env that is
// not a map of variable names to string globs): an entry
// that silently never matched would let the command it names run.
func ValidateGuardrailApprovals(g GuardrailsConfig) error {
	for _, e := range g.EnvApprovalCommands {
		if e.invalid != "" {
			return fmt.Errorf("guardrails.approval_commands: entry %q: %s", e.Match, e.invalid)
		}
		for name := range e.WhenEnv {
			if !envVarNameRe.MatchString(name) {
				return fmt.Errorf("guardrails.approval_commands: entry %q: when_env name %q is not an environment variable name", e.Match, name)
			}
		}
	}
	return nil
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
