package guardrails

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/logging"
)

// Shim decisions.
type action int

const (
	actForward action = iota
	actRefuse
	actApprove
)

type decision struct {
	act     action
	kind    string
	message string
	pattern string
}

// gitGlobalsWithValue are git's global options that take the next argument
// as their value.
var gitGlobalsWithValue = []string{"-C", "-c", "--git-dir", "--work-tree", "--namespace",
	"--super-prefix", "--config-env", "--attr-source", "--exec-path", "--list-cmds"}

// mergeAPIRe matches REST paths that merge a pull or merge request, and the
// GraphQL mutations that do.
var mergeAPIRe = regexp.MustCompile(`(^|/)(pulls/[0-9]+/merge|merge_requests/[0-9]+/merge)/?(\?|$)|mergePullRequest|enablePullRequestAutoMerge`)

// decide classifies tool args… run in cwd. branchOf returns the branch
// checked out in a directory.
func (p Policy) decide(tool string, args []string, cwd string, branchOf func(dir string) string) decision {
	var d decision
	switch tool {
	case "git":
		d = p.decideGit(args, cwd, branchOf)
	case "gh":
		d = decideMergeCLI(args, "pr", "gh pr merge")
	case "glab":
		d = decideMergeCLI(args, "mr", "glab mr merge")
	}
	if d.act != actForward {
		return d
	}
	if pattern, ok := MatchApproval(p.ApprovalCommands, tool, args); ok {
		return decision{act: actApprove, pattern: pattern}
	}
	return decision{}
}

func isHooksPathAssignment(kv string) bool {
	key, _, _ := strings.Cut(kv, "=")
	return strings.EqualFold(strings.TrimSpace(key), "core.hooksPath")
}

func (p Policy) decideGit(args []string, cwd string, branchOf func(dir string) string) decision {
	bypass := decision{act: actRefuse, kind: KindHookBypass, message: "guardrails: replacing or skipping Itervox's git hooks is refused (core.hooksPath, --no-verify on push). Push normally; the guardrail hook chains to the repository's own hooks."}
	dir := cwd
	i := 0
	for i < len(args) {
		a := args[i]
		if !strings.HasPrefix(a, "-") {
			break
		}
		name, value, hasValue := strings.Cut(a, "=")
		switch {
		case a == "-c" || a == "--config-env":
			if i+1 < len(args) && isHooksPathAssignment(args[i+1]) {
				return bypass
			}
		case strings.HasPrefix(a, "-c") && len(a) > 2 && !strings.HasPrefix(a, "--"):
			if isHooksPathAssignment(a[2:]) {
				return bypass
			}
		case name == "--config-env" && hasValue:
			if isHooksPathAssignment(value) {
				return bypass
			}
		}
		if a == "-C" && i+1 < len(args) {
			if next := args[i+1]; filepath.IsAbs(next) {
				dir = next
			} else {
				dir = filepath.Join(dir, next)
			}
		}
		if slices.Contains(gitGlobalsWithValue, a) {
			i++
		}
		i++
	}
	if i >= len(args) {
		return decision{}
	}
	sub, rest := args[i], args[i+1:]
	switch sub {
	case "push":
		if slices.Contains(rest, "--no-verify") {
			return bypass
		}
	case "merge":
		if slices.ContainsFunc(rest, func(a string) bool { return a == "--abort" || a == "--quit" || a == "--continue" }) {
			return decision{}
		}
		if b := branchOf(dir); b != "" && p.protected(b) {
			return decision{act: actRefuse, kind: KindSelfMerge, message: fmt.Sprintf(
				"guardrails: merging into %s is refused: agents do not merge into the default branch. Merge the base into your issue branch instead, and leave merging the pull request to Itervox's merge policy or a human.", b)}
		}
	}
	return decision{}
}

// decideMergeCLI refuses `<cli> <noun> merge` (gh pr merge, glab mr merge)
// and API calls that merge.
func decideMergeCLI(args []string, noun, what string) decision {
	var pos []string
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			pos = append(pos, a)
		}
	}
	selfMerge := decision{act: actRefuse, kind: KindSelfMerge, message: "guardrails: " + what +
		" is refused: agents do not merge their own pull requests. Leave merging to Itervox's merge policy or a human."}
	for i := 0; i+1 < len(pos) && i < 2; i++ {
		if pos[i] == noun && pos[i+1] == "merge" {
			return selfMerge
		}
	}
	if slices.Contains(pos[:min(len(pos), 2)], "api") && slices.ContainsFunc(args, mergeAPIRe.MatchString) {
		return selfMerge
	}
	return decision{}
}

// maxDisplayBytes bounds a command's display form.
const maxDisplayBytes = 300

// displayCommand is a command's redacted, bounded display form: what logs,
// reports and approval questions show.
func displayCommand(tool string, args []string) string {
	s := logging.RedactString(strings.TrimSpace(tool + " " + strings.Join(args, " ")))
	if len(s) > maxDisplayBytes {
		cut := maxDisplayBytes
		for cut > 0 && (s[cut]&0xC0) == 0x80 {
			cut--
		}
		s = s[:cut] + "…"
	}
	return s
}

// commandKey identifies an exact command in a directory, for approval
// requests (a repeat joins the pending request).
func commandKey(cwd, tool string, args []string) string {
	h := sha256.New()
	h.Write([]byte(cwd))
	for _, a := range append([]string{tool}, args...) {
		h.Write([]byte{0})
		h.Write([]byte(a))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// execFn replaces the process with the real program (syscall.Exec);
// tests substitute it.
var execFn = execProgram

// runShim is a shim: decide, then refuse, wait for an approval, or exec
// the real program with the original arguments and environment.
func runShim(p Policy, tool string, args, env []string, realPATH string, stderr io.Writer) int {
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}
	d := p.decide(tool, args, cwd, currentBranch)
	display := displayCommand(tool, args)
	switch d.act {
	case actRefuse:
		_, _ = fmt.Fprintln(stderr, d.message)
		newClient(p).report(Event{Kind: d.kind, Tool: tool, Command: display})
		return 1
	case actApprove:
		if code, ok := waitForApproval(p, d.pattern, display, commandKey(cwd, tool, args), stderr); !ok {
			return code
		}
	}
	real, err := resolveReal(tool, realPATH)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "guardrails: %s: %v\n", tool, err)
		return 127
	}
	if tool == "git" {
		env = withHooksPath(env, filepath.Join(p.Dir, "hooks"))
	}
	if err := execFn(real, append([]string{tool}, args...), env); err != nil {
		_, _ = fmt.Fprintf(stderr, "guardrails: run %s: %v\n", tool, err)
		return 126
	}
	return 0
}

// waitForApproval asks the daemon for an approval and waits for the
// answer. ok means approved (run the command once); otherwise code is the
// shim's exit status.
func waitForApproval(p Policy, pattern, display, key string, stderr io.Writer) (code int, ok bool) {
	c := newClient(p)
	if !p.ApprovalsEnabled {
		_, _ = fmt.Fprintf(stderr, "guardrails: `%s` needs a human approval (guardrails.approval_commands: %q), and approvals are not available here. Ask the operator to run it.\n", display, pattern)
		c.report(Event{Kind: KindApprovalUnavailable, Tool: firstWord(display), Command: display})
		return 1, false
	}
	_, _ = fmt.Fprintf(stderr, "guardrails: `%s` needs a human approval (guardrails.approval_commands: %q). Waiting for the operator: the request is shown as input required on the dashboard and the tracker. If this command times out, run it again later: an approval is kept for this exact command and runs it once.\n", display, pattern)
	st, err := c.awaitDecision(ApprovalRequest{Key: key, Command: display})
	switch {
	case err != nil:
		_, _ = fmt.Fprintf(stderr, "guardrails: approval channel unavailable: %v. Ask the operator to run `%s`.\n", err, display)
		c.report(Event{Kind: KindApprovalUnavailable, Tool: firstWord(display), Command: display})
		return 1, false
	case st.Status == StatusApproved:
		_, _ = fmt.Fprintf(stderr, "guardrails: approved; running `%s` once.\n", display)
		return 0, true
	case st.Status == StatusRejected:
		reason := strings.TrimSpace(st.Message)
		if reason == "" {
			reason = "no reason given"
		}
		_, _ = fmt.Fprintf(stderr, "guardrails: the operator rejected `%s`: %s\n", display, reason)
		return 1, false
	default:
		_, _ = fmt.Fprintf(stderr, "guardrails: the approval request for `%s` ended without an answer (the run ended or the daemon restarted). Do not retry it without asking the operator.\n", display)
		return 1, false
	}
}

func firstWord(s string) string {
	w, _, _ := strings.Cut(s, " ")
	return w
}

// currentBranch is the branch checked out in dir ("" when detached or not
// a repository).
func currentBranch(dir string) string {
	return gitRepo{dir: dir}.CurrentBranch()
}

// withHooksPath makes sure git sees core.hooksPath=hooks last in
// GIT_CONFIG_PARAMETERS (the agent's environment normally already has it;
// a tool that filtered the variable gets it back).
func withHooksPath(env []string, hooks string) []string {
	param := gitConfigParam("core.hooksPath", hooks)
	out := slices.Clone(env)
	for i, kv := range out {
		name, value, _ := strings.Cut(kv, "=")
		if name != "GIT_CONFIG_PARAMETERS" {
			continue
		}
		if strings.HasSuffix(value, param) {
			return out
		}
		out[i] = name + "=" + strings.TrimSpace(value+" "+param)
		return out
	}
	return append(out, "GIT_CONFIG_PARAMETERS="+param)
}

// stripDir returns pathList without dir (compared after cleaning and
// resolving symlinks) and without empty entries.
func stripDir(pathList, dir string) string {
	resolve := func(d string) string {
		if r, err := filepath.EvalSymlinks(d); err == nil {
			return r
		}
		return filepath.Clean(d)
	}
	target := resolve(dir)
	var keep []string
	for _, d := range filepath.SplitList(pathList) {
		if d == "" || resolve(d) == target {
			continue
		}
		keep = append(keep, d)
	}
	return strings.Join(keep, string(os.PathListSeparator))
}

// resolveReal finds tool on pathList, skipping any guardrail shim (this
// run's or a nested one's), so a forward can never recurse.
func resolveReal(tool, pathList string) (string, error) {
	for _, d := range filepath.SplitList(pathList) {
		if d == "" {
			continue
		}
		cand := filepath.Join(d, tool)
		info, err := os.Stat(cand)
		if err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
			continue
		}
		if isShim(cand) {
			continue
		}
		return cand, nil
	}
	return "", fmt.Errorf("command not found on PATH (outside the guardrail shims)")
}

func isShim(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	head := make([]byte, 256)
	n, _ := io.ReadFull(f, head)
	return strings.Contains(string(head[:n]), shimMarker)
}

// approvalPoll is how often a waiting shim asks for the decision;
// approvalMaxFailures how many consecutive failed calls end the wait.
var (
	approvalPoll        = time.Second
	approvalMaxFailures = 60
)
