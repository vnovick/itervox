package main

import (
	"fmt"
	"path"
	"slices"
	"sort"
	"strings"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/guardrails"
)

// guardrailsGuideURL is the guardrails page the Lanes section points to.
const guardrailsGuideURL = "https://itervox.dev/guides/guardrails/"

// LanesCheck is doctor's "Lanes" section: the guardrails.lanes globs in
// effect, and the places where a check the daemon runs is still defined by
// a file the agent can change on its own. An agent may change what CI
// checks, never how CI checks; a verify command whose definition sits in
// the auto lane breaks that. Every finding warns; none changes the exit
// code (like the Permissions section).
type LanesCheck struct {
	// Loaded is false when there is no configuration to report on.
	Loaded bool
	// GuardrailsOff: guardrails.enabled is false, so no lane applies.
	GuardrailsOff bool
	Human         []string
	// HumanDefault: guardrails.lanes.human is the built-in list.
	HumanDefault  bool
	SecondOpinion []string
	// Editable are the verify checks defined by a file outside the human lane.
	Editable []verifyDefinition
	// SelfReported are require_evidence names no verify check runs.
	SelfReported []selfReportedEvidence
}

// verifyDefinition is one verify check whose command is defined by files
// the agent can change without a human.
type verifyDefinition struct {
	Profile, Check, Run string
	Files               []string
}

// selfReportedEvidence is a profile's require_evidence names that only the
// agent's own evidence file backs.
type selfReportedEvidence struct {
	Profile string
	Checks  []string
}

// verifyDefiningFiles maps a command word to the files that decide what it
// runs. A verify check naming the word is only as trustworthy as those
// files: `make test` runs whatever the Makefile says. `go test` is absent on
// purpose: what it runs is the code under test, which is the agent's to
// change. Paths are relative to the directory the command runs in.
var verifyDefiningFiles = map[string][]string{
	"make":   {"Makefile"},
	"npm":    {"package.json"},
	"pnpm":   {"package.json"},
	"yarn":   {"package.json"},
	"pytest": {"pytest.ini", "pyproject.toml", "setup.cfg", "tox.ini", "conftest.py"},
	"tox":    {"tox.ini"},
	"just":   {"justfile"},
}

func checkLanes(cfg *config.Config) LanesCheck {
	if cfg == nil {
		return LanesCheck{}
	}
	l := cfg.Guardrails.Lanes
	c := LanesCheck{
		Loaded:        true,
		GuardrailsOff: !cfg.Guardrails.Enabled,
		Human:         l.Human,
		HumanDefault:  slices.Equal(l.Human, config.DefaultGuardrailHumanLane),
		SecondOpinion: l.SecondOpinion,
	}
	lanes := guardrails.Lanes{Human: l.Human, SecondOpinion: l.SecondOpinion}
	names := make([]string, 0, len(cfg.Agent.Profiles))
	for name, p := range cfg.Agent.Profiles {
		if config.ProfileEnabled(p) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		p := cfg.Agent.Profiles[name]
		verified := map[string]bool{}
		for _, v := range p.Verify {
			verified[v.Name] = true
			var files []string
			for _, f := range verifyCommandFiles(v.Run) {
				if lanes.Lane(f) != guardrails.LaneHuman && !slices.Contains(files, f) {
					files = append(files, f)
				}
			}
			if len(files) > 0 {
				c.Editable = append(c.Editable, verifyDefinition{Profile: name, Check: v.Name, Run: v.Run, Files: files})
			}
		}
		var self []string
		for _, e := range p.RequireEvidence {
			// "ci" is the pull request's own CI checks, not an evidence file.
			if e != config.EvidenceCheckCI && !verified[e] {
				self = append(self, e)
			}
		}
		if len(self) > 0 {
			c.SelfReported = append(c.SelfReported, selfReportedEvidence{Profile: name, Checks: self})
		}
	}
	return c
}

// verifyCommandFiles are the repository-relative files that define what a
// verify command line runs: for each command word in verifyDefiningFiles,
// its files, resolved against the directory a preceding `cd` moved to; and
// a script the command runs by relative path (`./scripts/test.sh`). A rough
// shell reading, enough for the one-line commands verify takes.
func verifyCommandFiles(run string) []string {
	var out []string
	dir := ""
	for _, seg := range splitShellSegments(run) {
		words := strings.Fields(seg)
		for len(words) > 0 && isShellAssignment(words[0]) {
			words = words[1:]
		}
		if len(words) == 0 {
			continue
		}
		if words[0] == "cd" {
			if len(words) > 1 && !strings.HasPrefix(words[1], "/") && !strings.HasPrefix(words[1], "~") && !strings.HasPrefix(words[1], "$") {
				dir = path.Join(dir, unquote(words[1]))
			}
			continue
		}
		for i, w := range words {
			w = unquote(w)
			if files, ok := verifyDefiningFiles[w]; ok {
				for _, f := range files {
					out = append(out, path.Join(dir, f))
				}
				continue
			}
			// A script run by relative path: the command word itself, or the
			// script argument of an interpreter (`bash scripts/ci.sh`).
			script := strings.HasPrefix(w, "./") || (i > 0 && strings.HasSuffix(w, ".sh") && !strings.HasPrefix(w, "-"))
			// `./...` is a Go package pattern, not a script.
			if script && !strings.HasPrefix(w, "/") && !strings.Contains(w, "...") && !strings.Contains(w, "$") && path.Clean(w) != "." {
				out = append(out, path.Join(dir, w))
			}
		}
	}
	return out
}

// splitShellSegments splits a command line at &&, ||, ;, | and newlines.
func splitShellSegments(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		return r == '&' || r == '|' || r == ';' || r == '\n' || r == '(' || r == ')'
	})
}

func isShellAssignment(w string) bool {
	name, _, ok := strings.Cut(w, "=")
	return ok && name != "" && !strings.ContainsAny(name, "-/.$\"'")
}

func unquote(w string) string { return strings.Trim(w, `"'`) }

// laneGlobs renders a lane's globs, [] as "off".
func laneGlobs(globs []string) string {
	if len(globs) == 0 {
		return "off"
	}
	return strings.Join(globs, ", ")
}

func renderLanesCheck(b *strings.Builder, c LanesCheck) {
	if !c.Loaded {
		return
	}
	b.WriteString("\nLanes\n")
	human := laneGlobs(c.Human)
	if c.HumanDefault {
		human += " (default)"
	}
	fmt.Fprintf(b, "  human: %s\n", human)
	fmt.Fprintf(b, "  second_opinion: %s\n", laneGlobs(c.SecondOpinion))
	if c.GuardrailsOff {
		fmt.Fprintf(b, "WARNING: guardrails.enabled is false, so no lane applies: an agent's push may change CI workflows and test config without a human. See %s\n", guardrailsGuideURL)
	} else if len(c.Human) == 0 {
		fmt.Fprintf(b, "WARNING: the human lane is off (guardrails.lanes.human: []): an agent's push may change CI workflows, test config and WORKFLOW.md without a human. See %s\n", guardrailsGuideURL)
	}
	for _, d := range c.Editable {
		them := "it"
		if len(d.Files) > 1 {
			them = "them"
		}
		fmt.Fprintf(b, "WARNING: %s: verify check %s runs `%s`, defined by %s, which the agent can change without a human; add %s to guardrails.lanes.human\n",
			d.Profile, d.Check, d.Run, strings.Join(d.Files, ", "), them)
	}
	for _, s := range c.SelfReported {
		fmt.Fprintf(b, "  %s: require_evidence %s agent-reported, not daemon-run (add a verify check of the same name to have the daemon run it)\n",
			s.Profile, strings.Join(s.Checks, ", ")+isAre(len(s.Checks)))
	}
}

func isAre(n int) string {
	if n == 1 {
		return " is"
	}
	return " are"
}
