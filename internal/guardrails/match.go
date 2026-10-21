package guardrails

import (
	"slices"
	"strings"
)

// MatchApproval returns the first approval pattern that matches the command
// tool args…, and whether one did. A pattern is a sequence of glob tokens
// (`*` matches any run of characters, `/` included; `?` one character): the
// first matches the program name, the rest must each match an argument, in
// order, with any other arguments allowed between them. So "kubectl apply"
// matches `kubectl -n prod apply -f k8s/`, and "gcloud deploy" matches
// `gcloud run deploy`. A flag token also matches the flag with a value
// attached ("--prod" matches "--prod=true").
func MatchApproval(patterns []string, tool string, args []string) (string, bool) {
	for _, p := range patterns {
		toks := strings.Fields(p)
		if len(toks) == 0 || !globMatch(toks[0], tool) {
			continue
		}
		want := toks[1:]
		for _, a := range args {
			if len(want) == 0 {
				break
			}
			if matchArg(want[0], a) {
				want = want[1:]
			}
		}
		if len(want) == 0 {
			return p, true
		}
	}
	return "", false
}

func matchArg(tok, arg string) bool {
	if globMatch(tok, arg) {
		return true
	}
	if strings.HasPrefix(tok, "-") {
		if name, _, found := strings.Cut(arg, "="); found {
			return globMatch(tok, name)
		}
	}
	return false
}

// globMatch matches s against pattern, where `*` matches any run of
// characters (including `/`, unlike path.Match) and `?` any one character.
func globMatch(pattern, s string) bool {
	p, i := 0, 0
	star, mark := -1, 0
	for i < len(s) {
		switch {
		case p < len(pattern) && (pattern[p] == '?' || pattern[p] == s[i]):
			p++
			i++
		case p < len(pattern) && pattern[p] == '*':
			star, mark = p, i
			p++
		case star >= 0:
			p = star + 1
			mark++
			i = mark
		default:
			return false
		}
	}
	for p < len(pattern) && pattern[p] == '*' {
		p++
	}
	return p == len(pattern)
}

// approvalWords are the first words of a reply that approve a command.
var approvalWords = []string{"approve", "approved", "yes", "y", "ok", "okay", "allow"}

// IsApproval reports whether a human reply approves a pending command: its
// first word is one of approve, approved, yes, y, ok, okay or allow (any
// case, surrounding punctuation ignored). Anything else rejects it, and the
// reply is passed back to the agent as the reason.
func IsApproval(reply string) bool {
	fields := strings.Fields(strings.ToLower(reply))
	if len(fields) == 0 {
		return false
	}
	return slices.Contains(approvalWords, strings.Trim(fields[0], ".,!:;\"'`*_()"))
}

// shimTools are the programs always shimmed: gh and glab (self-merge) and
// git (merges into the default branch, hook bypasses).
var shimTools = []string{"gh", "glab", "git"}

// ShimNames returns the program names a run shims: gh, glab, git and the
// program token of every approval pattern that names a bare program.
func ShimNames(approvalCommands []string) []string {
	names := slices.Clone(shimTools)
	for _, p := range approvalCommands {
		program, _, _ := strings.Cut(strings.TrimSpace(p), " ")
		if program == "" || program == "." || program == ".." || strings.ContainsAny(program, "/*?[\\") || slices.Contains(names, program) {
			continue
		}
		names = append(names, program)
	}
	return names
}
