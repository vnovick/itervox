package main

import (
	"fmt"
	"os/exec"
	"sort"
	"strings"

	"github.com/vnovick/itervox/internal/config"
)

// unresolvableProfileCommands returns "profile: command" pairs whose agent
// binary cannot be found on PATH, sorted for stable output.
//
// Only the executable is checked — the first field of the configured command,
// since `claude --model x` is a command line, not a path. Disabled profiles
// are skipped: they cannot be dispatched, so their binary is irrelevant.
//
// This exists because the failure it catches is otherwise invisible until
// dispatch. Tools installed under a version manager (nvm, asdf, rbenv) live in
// a versioned bin directory that is on PATH in an interactive shell but often
// not in the environment a daemon was started from — and that path changes on
// every version switch. The symptom is a runtime "command not found" buried in
// the log, long after the operator has stopped looking.
func unresolvableProfileCommands(cfg *config.Config) []string {
	if cfg == nil {
		return nil
	}
	seen := make(map[string]struct{})
	var out []string
	for name, profile := range cfg.Agent.Profiles {
		if !config.ProfileEnabled(profile) {
			continue
		}
		bin := firstField(profile.Command)
		if bin == "" {
			continue
		}
		if _, err := exec.LookPath(bin); err == nil {
			continue
		}
		pair := fmt.Sprintf("%s: %s", name, bin)
		if _, dup := seen[pair]; dup {
			continue
		}
		seen[pair] = struct{}{}
		out = append(out, pair)
	}
	sort.Strings(out)
	return out
}

// firstField returns the executable portion of a command line.
func firstField(command string) string {
	fields := strings.Fields(strings.TrimSpace(command))
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}
