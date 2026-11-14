package guardrails

import (
	"fmt"
	"maps"
	"runtime"
	"slices"
	"strings"

	"github.com/vnovick/itervox/internal/config"
)

// EnvApproval is an environment-scoped approval command
// (guardrails.approval_commands entry {match, when_env}): Match needs a
// human approval only when every WhenEnv variable's value matches its glob
// in the environment the command runs with. A glob uses `*` and `?` like
// MatchApproval tokens; a leading `!` negates it. An unset variable is the
// empty string, so `!dev*` holds a command whose variable is unset too.
type EnvApproval struct {
	Match   string            `json:"match"`
	WhenEnv map[string]string `json:"when_env"`
}

// EnvApprovalsFromConfig converts the configuration's entries.
func EnvApprovalsFromConfig(in []config.GuardrailEnvApproval) []EnvApproval {
	var out []EnvApproval
	for _, e := range in {
		out = append(out, EnvApproval{Match: e.Match, WhenEnv: maps.Clone(e.WhenEnv)})
	}
	return out
}

// String is the rule's display form: `terraform apply` when TF_WORKSPACE=prod*.
func (e EnvApproval) String() string {
	names := slices.Sorted(maps.Keys(e.WhenEnv))
	conds := make([]string, 0, len(names))
	for _, n := range names {
		conds = append(conds, n+"="+e.WhenEnv[n])
	}
	return fmt.Sprintf("%q when %s", e.Match, strings.Join(conds, ", "))
}

// EnvLookup reads a variable's value; known is false when the caller cannot
// tell what the value will be at run time (the hook, for `X=$(cmd) tool`).
type EnvLookup func(name string) (value string, known bool)

// LookupIn returns an EnvLookup over an environment list (KEY=value
// entries; the last one wins, like exec). Names are case-insensitive on
// Windows, as the environment is there.
func LookupIn(env []string) EnvLookup {
	m := map[string]string{}
	for _, kv := range env {
		name, value, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		m[envKey(name)] = value
	}
	return func(name string) (string, bool) { return m[envKey(name)], true }
}

func envKey(name string) string {
	if runtime.GOOS == "windows" {
		return strings.ToUpper(name)
	}
	return name
}

// envMatches reports whether every condition of e holds under lookup. A
// value the lookup cannot know counts as matching (fail closed: the
// command is held rather than let through on a guess).
func (e EnvApproval) envMatches(lookup EnvLookup) bool {
	for name, glob := range e.WhenEnv {
		value, known := lookup(name)
		if !known {
			continue
		}
		neg := strings.HasPrefix(glob, "!")
		if globMatch(strings.TrimPrefix(glob, "!"), value) == neg {
			return false
		}
	}
	return true
}

// MatchEnvApproval returns the first rule whose pattern matches tool args…
// and whose environment conditions hold under lookup.
func MatchEnvApproval(rules []EnvApproval, tool string, args []string, lookup EnvLookup) (EnvApproval, bool) {
	for _, r := range rules {
		if _, ok := MatchApproval([]string{r.Match}, tool, args); ok && r.envMatches(lookup) {
			return r, true
		}
	}
	return EnvApproval{}, false
}

// matchAnyApproval checks the plain approval commands, then the
// environment-scoped ones; why describes the matching rule for the agent.
func (p Policy) matchAnyApproval(tool string, args []string, lookup EnvLookup) (why string, ok bool) {
	if pattern, ok := MatchApproval(p.ApprovalCommands, tool, args); ok {
		return fmt.Sprintf("%q", pattern), true
	}
	if r, ok := MatchEnvApproval(p.EnvApprovalCommands, tool, args, lookup); ok {
		return r.String(), true
	}
	return "", false
}

// approvalPatterns are every approval pattern, plain and scoped (the
// programs to shim).
func (p Policy) approvalPatterns() []string {
	out := slices.Clone(p.ApprovalCommands)
	for _, r := range p.EnvApprovalCommands {
		out = append(out, r.Match)
	}
	return out
}
