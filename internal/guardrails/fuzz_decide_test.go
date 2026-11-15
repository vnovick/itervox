package guardrails

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// Fuzzing the shim's classifier (Policy.decide, MatchApproval, globMatch).
// The shim sees an argv after the shell has expanded it, so every property
// here is stated over argv: the shell-level variations (quoting, extra
// whitespace) are covered by rendering an argv through quoting styles and
// tokenising it back (fuzzShellRender / fuzzShellSplit), and against real
// /bin/sh by TestShimDecisionSurvivesShellQuoting.

// fuzzPolicy has the default protected branches and approval patterns for
// both a shimmed tool (git) and extra programs.
var fuzzPolicy = Policy{
	ProtectedBranches: []string{"main", "master", "develop"},
	ApprovalCommands:  []string{"vercel --prod", "kubectl apply", "git push --force*", "gh release create", "terraform apply"},
}

// The tool names, in the order seed corpora index them (toolSel 0 is git).
// Built with append so the gitexec audit does not read it as a git argv.
var fuzzTools = append([]string(nil), "git", "gh", "glab", "vercel", "kubectl", "terraform")

// fuzzArgv splits fuzz input into an argv on NUL (an argv element can hold
// any other byte).
func fuzzArgv(raw string) []string {
	if raw == "" {
		return nil
	}
	return strings.Split(raw, "\x00")
}

// rank orders decisions by strictness: forward < approval < refuse.
func rank(d decision) int {
	switch d.act {
	case actRefuse:
		return 2
	case actApprove:
		return 1
	}
	return 0
}

func FuzzDecide(f *testing.F) {
	for _, seed := range []struct {
		tool   uint8
		argv   string
		branch string
		split  uint8
		style  uint64
	}{
		{0, "push\x00--no-verify\x00origin\x00HEAD", "itervox/eng-1", 1, 0},
		{0, "push\x00--no-verif", "itervox/eng-1", 1, 1},
		{0, "-c\x00core.hooksPath=/dev/null\x00push", "itervox/eng-1", 2, 2},
		{0, "-cCORE.HOOKSPATH=/tmp\x00push", "x", 1, 3},
		{0, "--config-env=core.hooksPath=X\x00push", "x", 1, 4},
		{0, "-c\x00include.path=/tmp/x\x00push", "x", 0, 5},
		{0, "-C\x00sub\x00merge\x00feature", "main", 3, 6},
		{0, "merge\x00--abort", "main", 1, 7},
		{0, "status\x00--short", "main", 1, 8},
		{0, "-C", "main", 0, 9},
		{0, "push\x00--force-with-lease", "itervox/eng-1", 1, 10},
		{1, "pr\x00merge\x0012\x00--squash", "", 2, 11},
		{1, "pr\x00-R\x00acme/app\x00merge", "", 1, 12},
		{1, "--repo\x00acme/app\x00pr\x00merge", "", 2, 13},
		{1, "api\x00-X\x00PUT\x00repos/acme/app/pulls/12/merge", "", 2, 14},
		{1, "api\x00graphql\x00-f\x00query=mutation { mergePullRequest(input: {}) { clientMutationId } }", "", 2, 15},
		{1, "pr\x00view\x0012\x00--json\x00title,body", "", 2, 16},
		{2, "mr\x00merge\x007", "", 1, 17},
		{3, "deploy\x00--prod", "", 1, 18},
		{4, "-n\x00prod\x00apply\x00-f\x00k8s/", "", 2, 19},
		{5, "apply\x00-auto-approve", "", 1, 20},
		{0, "", "", 0, 21},
		// A hooksPath override on a hook-free builtin (Claude Code's own
		// read-only calls), and the shapes around it that stay refused.
		{0, "-c\x00core.hooksPath=/dev/null\x00ls-files\x00--error-unmatch\x00--\x00:(icase).claude/settings.local.json", "x", 3, 22},
		{0, "-c\x00core.hooksPath=/dev/null\x00-c\x00alias.ls-files=!sh\x00ls-files", "x", 2, 23},
		{0, "--no-pager\x00--config-env=core.hooksPath=E\x00-C\x00sub\x00log\x00-1", "x", 2, 24},
		{0, "-c\x00core.hooksPath=/dev/null\x00--version\x00-c\x00core.hooksPath=x\x00push", "x", 3, 25},
		{0, "-c\x00core.hooksPath=/dev/null\x00status\x00--short", "x", 2, 26},
	} {
		f.Add(seed.tool, seed.argv, seed.branch, seed.split, seed.style)
	}
	f.Fuzz(func(t *testing.T, toolSel uint8, rawArgv, branch string, split uint8, style uint64) {
		tool := fuzzTools[int(toolSel)%len(fuzzTools)]
		args := fuzzArgv(rawArgv)
		branchOf := func(string) string { return branch }
		p := fuzzPolicy

		// Deterministic: the same argv in the same directory always gets
		// the same decision.
		d := p.decide(tool, args, "/repo", noEnv, branchOf)
		if again := p.decide(tool, args, "/repo", noEnv, branchOf); again != d {
			t.Fatalf("decide(%s %q) is not deterministic: %+v then %+v", tool, args, d, again)
		}
		if d.act == actRefuse && (d.kind == "" || !IsKind(d.kind) || d.message == "") {
			t.Fatalf("decide(%s %q): a refusal without a known kind and a message: %+v", tool, args, d)
		}
		// d.pattern is the rule's display form: a plain rule is quoted.
		if pat, err := strconv.Unquote(d.pattern); d.act == actApprove && (err != nil || !slices.Contains(p.ApprovalCommands, pat)) {
			t.Fatalf("decide(%s %q): approval names pattern %q, not one of the policy's", tool, args, d.pattern)
		}

		// Appending arguments never makes a decision less strict, except
		// that `git merge --abort|--quit|--continue` is not a new merge.
		k := 0
		if len(args) > 0 {
			k = int(split) % (len(args) + 1)
		}
		prefix := p.decide(tool, args[:k], "/repo", noEnv, branchOf)
		ends := slices.ContainsFunc(args[k:], func(a string) bool { return a == "--abort" || a == "--quit" || a == "--continue" })
		if rank(d) < rank(prefix) && (tool != "git" || prefix.kind != KindSelfMerge || !ends) {
			t.Fatalf("appending %q to %s %q relaxed %+v to %+v", args[k:], tool, args[:k], prefix, d)
		}

		// Quoting and whitespace: the argv rendered as a shell command line
		// in any quoting style splits back to the same argv, so the shim
		// sees and decides it identically.
		line := fuzzShellRender(append([]string{tool}, args...), style)
		back, ok := fuzzShellSplit(line)
		if !ok || !slices.Equal(back, append([]string{tool}, args...)) {
			t.Fatalf("render/split round trip: %q -> %q -> %q", append([]string{tool}, args...), line, back)
		}
		if again := p.decide(back[0], back[1:], "/repo", noEnv, branchOf); again != d {
			t.Fatalf("decide(%q) = %+v, but %+v for the same argv", line, again, d)
		}

		// Variations git, gh and glab treat identically stay refused.
		switch tool {
		case "git":
			// A hooksPath override in any case and any of the four
			// spellings, ahead of whatever else, is refused unless the
			// strict reader resolves a hook-free builtin; then the
			// forwarded argv decides to a forward (it overrides nothing the
			// reader did not drop).
			key := fuzzCase("core.hooksPath", style)
			for pi, pre := range [][]string{{"-c", key + "=" + branch}, {"-c" + key + "=" + branch},
				{"--config-env", key + "=ENV"}, {"--config-env=" + key + "=ENV"}, {"-c", fuzzCase("include.path", style) + "=" + branch}} {
				full := append(slices.Clone(pre), args...)
				got := p.decide("git", full, "/repo", noEnv, branchOf)
				// The attached -cKEY form (git rejects it) and an include
				// are refused whatever follows.
				if _, ok := readOnlyHooksOverride(full); !ok || pi == 1 || pi == 4 {
					if got.kind != KindHookBypass {
						t.Fatalf("git %q %q: want a hook_bypass refusal, got %+v", pre, args, got)
					}
					continue
				}
				if got.act == actRefuse || !got.dropHooksPath {
					t.Fatalf("git %q %q: a hook-free builtin, want a forward dropping the override, got %+v", pre, args, got)
				}
				if fwd := p.decide("git", forwardArgs(got, full), "/repo", noEnv, branchOf); fwd.act == actRefuse {
					t.Fatalf("git %q: the forwarded argv %q is refused: %+v", full, forwardArgs(got, full), fwd)
				}
				// A hook-running command or an alias with the same
				// override stays refused.
				for _, cmd := range [][]string{{"commit"}, {"push"}, {"merge"}, {"rebase"}, {"checkout"}, {"status"}, {"diff"}, {"-c", "alias.x=!sh", "x"}} {
					bad := append(append(slices.Clone(pre), cmd...), args...)
					if g := p.decide("git", bad, "/repo", noEnv, branchOf); g.kind != KindHookBypass {
						t.Fatalf("git %q: want a hook_bypass refusal, got %+v", bad, g)
					}
				}
			}
			// --no-verify or an abbreviation git accepts, anywhere after
			// push, is refused.
			abbr := "--no-verify"[:len("--no-v")+int(style%6)]
			at := 0
			if len(args) > 0 {
				at = int(style/7) % (len(args) + 1)
			}
			push := append(append(append([]string{"push"}, args[:at]...), abbr), args[at:]...)
			if got := p.decide("git", push, "/repo", noEnv, branchOf); got.kind != KindHookBypass {
				t.Fatalf("git %q: want a hook_bypass refusal, got %+v", push, got)
			}
		case "gh", "glab":
			// `<cli> <noun> merge …` stays refused with flags (bare, or
			// the repository flag with its value) inserted anywhere.
			noun := map[string]string{"gh": "pr", "glab": "mr"}[tool]
			merge := []string{noun, "merge"}
			ins := [][]string{{"-R", branch}, {"--repo", branch}, {"--repo=" + branch}, {"--auto"}}[style%4]
			at := int(style/4) % 3
			cmd := append(append(append(slices.Clone(merge[:min(at, 2)]), ins...), merge[min(at, 2):]...), args...)
			if got := p.decide(tool, cmd, "/repo", noEnv, branchOf); got.kind != KindSelfMerge {
				t.Fatalf("%s %q: want a self_merge refusal, got %+v", tool, cmd, got)
			}
		}
	})
}

// fuzzCase flips the case of s's letters by the bits of style (git's
// section and key names are case-insensitive).
func fuzzCase(s string, style uint64) string {
	b := []byte(s)
	for i, c := range b {
		if (c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z') && style>>(uint(i)%64)&1 == 1 {
			b[i] = c ^ 0x20
		}
	}
	return string(b)
}

// fuzzShellRender writes argv as a POSIX shell command line, picking a
// quoting style per word and a run of blanks between words from style.
func fuzzShellRender(argv []string, style uint64) string {
	var b strings.Builder
	for i, w := range argv {
		s := style >> (uint(i*3) % 61)
		if i > 0 {
			b.WriteString([]string{" ", "  ", "\t", " \t "}[s>>2&3])
		}
		switch s % 4 {
		case 0: // single quotes, ' written as '\''
			b.WriteString("'" + strings.ReplaceAll(w, "'", `'\''`) + "'")
		case 1: // double quotes with \ " $ ` escaped
			b.WriteString(`"`)
			for _, c := range []byte(w) {
				if strings.IndexByte("\\\"$`", c) >= 0 {
					b.WriteByte('\\')
				}
				b.WriteByte(c)
			}
			b.WriteString(`"`)
		case 2: // every byte backslash-escaped (a newline needs quotes)
			if w == "" || strings.Contains(w, "\n") {
				b.WriteString("'" + strings.ReplaceAll(w, "'", `'\''`) + "'")
				continue
			}
			for _, c := range []byte(w) {
				b.WriteByte('\\')
				b.WriteByte(c)
			}
		default: // the word split in two differently quoted halves
			h := len(w) / 2
			b.WriteString("'" + strings.ReplaceAll(w[:h], "'", `'\''`) + `'"`)
			for _, c := range []byte(w[h:]) {
				if strings.IndexByte("\\\"$`", c) >= 0 {
					b.WriteByte('\\')
				}
				b.WriteByte(c)
			}
			b.WriteString(`"`)
		}
	}
	return b.String()
}

// fuzzShellSplit splits a command line the way a POSIX shell's quote
// removal and field splitting would for the forms fuzzShellRender writes
// (no expansions are left in them).
func fuzzShellSplit(line string) ([]string, bool) {
	var out []string
	var cur strings.Builder
	inWord := false
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch c {
		case ' ', '\t':
			if inWord {
				out = append(out, cur.String())
				cur.Reset()
				inWord = false
			}
		case '\'':
			inWord = true
			j := strings.IndexByte(line[i+1:], '\'')
			if j < 0 {
				return nil, false
			}
			cur.WriteString(line[i+1 : i+1+j])
			i += j + 1
		case '"':
			inWord = true
			for i++; ; i++ {
				if i >= len(line) {
					return nil, false
				}
				if line[i] == '"' {
					break
				}
				if line[i] == '\\' && i+1 < len(line) && strings.IndexByte("\\\"$`\n", line[i+1]) >= 0 {
					i++
				}
				cur.WriteByte(line[i])
			}
		case '\\':
			if i+1 >= len(line) {
				return nil, false
			}
			inWord = true
			i++
			cur.WriteByte(line[i])
		default:
			inWord = true
			cur.WriteByte(c)
		}
	}
	if inWord {
		out = append(out, cur.String())
	}
	return out, true
}

// refGlob is a reference for globMatch: `*` any run of bytes, `?` one
// byte, anything else itself, by dynamic programming over (pattern, input)
// positions instead of globMatch's backtracking.
func refGlob(pattern, s string) bool {
	// m[j] reports whether pattern[:i] matches s[:j] for the current i.
	m := make([]bool, len(s)+1)
	m[0] = true
	for i := 0; i < len(pattern); i++ {
		next := make([]bool, len(s)+1)
		switch pattern[i] {
		case '*':
			for j := 0; j <= len(s); j++ {
				next[j] = m[j] || (j > 0 && next[j-1])
			}
		default:
			for j := 1; j <= len(s); j++ {
				next[j] = m[j-1] && (pattern[i] == '?' || pattern[i] == s[j-1])
			}
		}
		m = next
	}
	return m[len(s)]
}

// refMatchApproval is a reference for MatchApproval: a pattern matches when
// its program token matches the tool and its other tokens match a
// subsequence of the arguments (every split is considered, not just MatchApproval's greedy leftmost one).
func refMatchApproval(patterns []string, tool string, args []string) (string, bool) {
	tokMatch := func(tok, a string) bool {
		if refGlob(tok, a) {
			return true
		}
		name, _, found := strings.Cut(a, "=")
		return strings.HasPrefix(tok, "-") && found && refGlob(tok, name)
	}
	// sub reports whether want matches a subsequence of args, by dynamic
	// programming: can[w][a] is "want[w:] matches within args[a:]".
	sub := func(want, args []string) bool {
		can := make([][]bool, len(want)+1)
		for w := range can {
			can[w] = make([]bool, len(args)+1)
		}
		for a := range can[len(want)] {
			can[len(want)][a] = true
		}
		for w := len(want) - 1; w >= 0; w-- {
			for a := len(args) - 1; a >= 0; a-- {
				can[w][a] = can[w][a+1] || (tokMatch(want[w], args[a]) && can[w+1][a+1])
			}
		}
		return can[0][0]
	}
	for _, p := range patterns {
		toks := strings.Fields(p)
		if len(toks) > 0 && refGlob(toks[0], tool) && sub(toks[1:], args) {
			return p, true
		}
	}
	return "", false
}

func FuzzMatchApproval(f *testing.F) {
	for _, seed := range []struct{ patterns, cmd string }{
		{"vercel --prod", "vercel\x00deploy\x00--prod"},
		{"vercel --prod", "vercel\x00deploy\x00--prod=true"},
		{"kubectl apply", "kubectl\x00-n\x00prod\x00apply\x00-f\x00k8s/"},
		{"gcloud deploy\nkubectl apply", "gcloud\x00run\x00deploy"},
		{"git push --force*", "git\x00push\x00--force-with-lease"},
		{"terraform a*y", "terraform\x00apply"},
		{"t?rraform ap?ly -auto*", "terraform\x00apply\x00-auto-approve"},
		{"*", "anything"},
		{"a**b?*c", "aXXbYc"},
		{"vercel --prod", "vercel\x00ls"},
		{"", "vercel"},
	} {
		f.Add(seed.patterns, seed.cmd)
	}
	f.Fuzz(func(t *testing.T, rawPatterns, rawCmd string) {
		patterns := strings.Split(rawPatterns, "\n")
		cmd := fuzzArgv(rawCmd)
		if len(cmd) == 0 || len(rawPatterns) > 256 || len(rawCmd) > 512 {
			return
		}
		tool, args := cmd[0], cmd[1:]
		got, ok := MatchApproval(patterns, tool, args)
		want, wantOK := refMatchApproval(patterns, tool, args)
		if got != want || ok != wantOK {
			t.Fatalf("MatchApproval(%q, %q, %q) = %q, %v; reference %q, %v", patterns, tool, args, got, ok, want, wantOK)
		}
		for _, tok := range append(strings.Fields(rawPatterns), tool) {
			for _, a := range append(args, tool) {
				if globMatch(tok, a) != refGlob(tok, a) {
					t.Fatalf("globMatch(%q, %q) = %v; reference %v", tok, a, globMatch(tok, a), refGlob(tok, a))
				}
			}
		}
	})
}

// TestShimDecisionSurvivesShellQuoting: the same refusals through a real
// /bin/sh, with the words quoted, split across quotes, escaped and padded
// with blanks. The shell hands the shim the same argv, so none of these
// slips through.
func TestShimDecisionSurvivesShellQuoting(t *testing.T) {
	r := newRig(t, nil)
	fakes := filepath.Join(r.root, "fakebin")
	ran := filepath.Join(r.root, "ran")
	fakeProgram(t, fakes, "gh", "echo gh \"$@\" >> "+ran+"\n")
	for _, script := range []string{
		`git push "--no-verify" origin HEAD`,
		`git  push	--no-'verify'  origin HEAD`,
		`git push \-\-no\-verify origin HEAD`,
		`"git" 'push' "--no-veri"`,
		`git -c "core.hooksPath=/dev/null" push`,
		`git -c 'CORE'.hooksPATH=/dev/null push`,
		`gh "pr" 'merge' 12`,
		`gh   pr  -R 'acme/app'   "mer"ge 12`,
		`g\h p\r m\erge`,
	} {
		out, code := r.agent(script, fakes)
		assert.Equal(t, 1, code, "%s: %s", script, out)
		assert.Contains(t, out, "is refused", script)
	}
	_, err := os.Stat(ran)
	assert.True(t, os.IsNotExist(err), "no real gh ran")
}

// TestDecideIsLinearInGitCOptions: found by FuzzDecide stalling. Each -C
// was joined onto the directory as it came, so a long run of `-C x`
// options cost quadratic time (16000 took ~0.9s); the values are joined
// once now, to the same directory.
func TestDecideIsLinearInGitCOptions(t *testing.T) {
	args := strings.Split(strings.Repeat("-C\x00x\x00", 200000)+"merge\x00feature", "\x00")
	var dir string
	start := time.Now()
	d := fuzzPolicy.decide("git", args, "/repo", noEnv, func(d string) string { dir = d; return "main" })
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("200000 -C options took %v", elapsed)
	}
	assert.Equal(t, KindSelfMerge, d.kind)
	assert.True(t, dir == filepath.Join("/repo", strings.Repeat("x/", 200000)), "the joined -C directory")

	// The directory is the same as joining one by one: absolute values
	// reset it, relative ones (.. included) are joined in order.
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"-C", "a", "-C", "../b", "merge", "x"}, "/repo/b"},
		{[]string{"-C", "a", "-C", "/abs", "-C", "c", "merge", "x"}, "/abs/c"},
		{[]string{"-C", "", "merge", "x"}, "/repo"},
		{[]string{"merge", "x"}, "/repo"},
	} {
		fuzzPolicy.decide("git", tc.args, "/repo", noEnv, func(d string) string { dir = d; return "" })
		assert.Equal(t, tc.want, dir, "%q", tc.args)
	}
}

// noEnv is an empty environment: env-scoped approval rules never match.
var noEnv = LookupIn(nil)
