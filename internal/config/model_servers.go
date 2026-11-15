package config

import (
	"slices"
	"sort"
	"strings"
)

// Model servers: the base URLs a profile command points its agent CLI at
// (`ANTHROPIC_BASE_URL=http://127.0.0.1:8000 claude`), for a local engine
// such as colibri or a proxy. `itervox doctor` probes them; the desktop app
// reads them from GET /api/v1/model-servers and probes them itself. Only
// assignments in the profile command are seen; a CLI's own config file
// (Codex's config.toml, OpenCode's providers) is not read.

// ModelServerEnvVars are the variables an agent CLI reads to talk to a
// model server other than its vendor's.
var ModelServerEnvVars = []string{"ANTHROPIC_BASE_URL", "OPENAI_BASE_URL", "OPENAI_API_BASE"}

// ModelServerURLs returns the model-server variables assigned in command
// before the program, e.g. `ANTHROPIC_BASE_URL=http://127.0.0.1:8000 claude`,
// through an optional `env` prefix and its flags, quotes stripped.
func ModelServerURLs(command string) map[string]string {
	out := map[string]string{}
	for _, token := range strings.Fields(command) {
		if token == "env" || strings.HasPrefix(token, "-") {
			continue
		}
		if !IsEnvAssignment(token) {
			break
		}
		key, val, _ := strings.Cut(token, "=")
		val = strings.Trim(val, `"'`)
		if val != "" && slices.Contains(ModelServerEnvVars, key) {
			out[key] = val
		}
	}
	return out
}

// ModelServerUse is one model server (a variable and its URL) and the run
// modes whose commands point at it.
type ModelServerUse struct {
	Var string
	URL string
	// Profiles are the run modes using it, sorted; defaultLabel stands for
	// agent.command.
	Profiles []string
	// Pools are the quota pools of those profiles, sorted and distinct;
	// empty when none sets one.
	Pools []string
	// Unchecked is set when the URL comes from a shell variable ($VAR),
	// which the agent's shell expands at run time.
	Unchecked bool
}

// ModelServerUses groups the model servers of agent.command (named
// defaultLabel) and of every enabled profile by variable and URL, sorted by
// URL then variable. A profile with an empty command runs defaultCommand.
func ModelServerUses(defaultLabel, defaultCommand string, profiles map[string]AgentProfile) []ModelServerUse {
	type key struct{ v, u string }
	type users struct{ profiles, pools []string }
	byKey := map[key]*users{}
	add := func(name, command, pool string) {
		for v, u := range ModelServerURLs(command) {
			k := key{v, u}
			if byKey[k] == nil {
				byKey[k] = &users{}
			}
			byKey[k].profiles = append(byKey[k].profiles, name)
			if pool != "" && !slices.Contains(byKey[k].pools, pool) {
				byKey[k].pools = append(byKey[k].pools, pool)
			}
		}
	}
	add(defaultLabel, defaultCommand, "")
	for name, p := range profiles {
		if !ProfileEnabled(p) {
			continue
		}
		command := p.Command
		if strings.TrimSpace(command) == "" {
			command = defaultCommand
		}
		add(name, command, p.Pool)
	}
	out := make([]ModelServerUse, 0, len(byKey))
	for k, u := range byKey {
		sort.Strings(u.profiles)
		sort.Strings(u.pools)
		out = append(out, ModelServerUse{
			Var:       k.v,
			URL:       k.u,
			Profiles:  u.profiles,
			Pools:     u.pools,
			Unchecked: strings.Contains(k.u, "$"),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].URL != out[j].URL {
			return out[i].URL < out[j].URL
		}
		return out[i].Var < out[j].Var
	})
	return out
}
