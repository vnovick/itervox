package referee

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/logging"
	"github.com/vnovick/itervox/internal/referee/adapters"
)

const diffUsage = `usage: referee diff --base <ref> --base-dir <path> [--head HEAD] [--policy .referee.yml]
                    [--dir .] [--timeout 30s] [--idle 3s] [--json]

Runs each configured tool's probes (tools: in .referee.yml, read from the
BASE ref) against two installs and reports how the tool's behaviour
changed: exit code, output, and new confirmation prompts. A dependency bump
can make a deploy CLI ask a new question, take a new flag or change what a
dry run says without touching any CI file.

  head install  the working tree under --dir (install the head's
                dependencies first)
  base install  --base-dir: a second checkout of the merge base of <base>
                and <head> (or of <base> itself) with ITS dependencies
                installed, e.g.
                  git worktree add --detach ../base "$(git merge-base origin/main HEAD)"
                  (cd ../base && npm ci --ignore-scripts)

Each probe runs without a shell, in its checkout, with stdin an open pipe
nothing is ever written to (a prompt is never answered and never sees end
of input), a fresh empty HOME, only PATH kept from the environment (no
credentials, no deploy-target variable), and its first 64 KiB of output
compared. A probe still running that printed something and then nothing
for --idle (3s) is waiting for input: its whole process group is killed and
its last line reported as the prompt. The idle window starts at the first
byte of output, so a slow start is not a wait; a probe that prints nothing
at all is seen waiting only at the per-probe --timeout, which also kills
the group of one that keeps printing.
Versions, timestamps, durations, temp paths, the checkout path and ANSI
colour are normalised away first.

There is NO network sandbox: a probe can reach the network. Run diff in a
job that holds no deploy credentials.

Without tools: on the base ref there is nothing to compare (exit 0).
--base defaults to origin/$GITHUB_BASE_REF in a pull_request workflow.

Exit codes: 0 no behaviour change (or no tools:), 10 a change a person
should look at, 2 usage or error (a missing install on either side, a
--base-dir that is not a checkout of the base, a probe that cannot start or
cannot run on either side: exit 126 or 127, e.g. a missing interpreter or a
native binary an install script did not download).`

type diffReport struct {
	Version       int        `json:"version"`
	ExitCode      int        `json:"exit_code"`
	Base          string     `json:"base"`
	BaseCommit    string     `json:"base_commit"`
	MergeBase     string     `json:"merge_base"`
	Head          string     `json:"head"`
	HeadCommit    string     `json:"head_commit"`
	Policy        string     `json:"policy"`
	BaseDir       string     `json:"base_dir"`
	BaseDirCommit string     `json:"base_dir_commit"`
	TimeoutMs     int64      `json:"timeout_ms"`
	IdleMs        int64      `json:"idle_ms"`
	Env           []string   `json:"env"` // the variable names a probe gets
	Tools         []diffTool `json:"tools"`
	Notes         []string   `json:"notes"`
}

type diffTool struct {
	Name    string      `json:"name"`
	Install string      `json:"install"`
	Changed bool        `json:"changed"`
	Probes  []diffProbe `json:"probes"`
}

type diffProbe struct {
	Probe        string   `json:"probe"`
	Argv         []string `json:"argv"`
	Base         probeRun `json:"base"`
	Head         probeRun `json:"head"`
	Changed      bool     `json:"changed"`
	Changes      []string `json:"changes"` // exit_code, timed_out, waiting_for_input, left_running, output, prompt
	Added        []string `json:"added"`   // normalised lines only head printed (bounded)
	AddedCount   int      `json:"added_count"`
	Removed      []string `json:"removed"` // normalised lines only base printed (bounded)
	RemovedCount int      `json:"removed_count"`
	Prompts      []string `json:"prompts"` // confirmation prompts new on head (bounded)
}

// diffOptions is what diffCmd parsed.
type diffOptions struct {
	dir, baseDir, base, head, policy string
	timeout, idle                    time.Duration
}

func diffCmd(ctx context.Context, args []string, env envFunc, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("referee diff", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { _, _ = fmt.Fprintln(stderr, diffUsage) }
	base := fs.String("base", "", "base ref the change merges into (its policy is the one used)")
	head := fs.String("head", "HEAD", "head ref under review")
	baseDir := fs.String("base-dir", "", "a checkout of the merge base with its dependencies installed")
	policyPath := fs.String("policy", defaultPolicyPath, "policy file path, read from the base ref")
	asJSON := fs.Bool("json", false, "print a JSON report")
	dir := fs.String("dir", ".", "repository root, with the head's dependencies installed")
	timeout := fs.Duration("timeout", defaultProbeTimeout, "kill a probe's process group after this long")
	idle := fs.Duration("idle", defaultProbeIdle, "a probe that printed something and then nothing for this long is waiting for input")
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	if fs.NArg() > 0 {
		return usageError(stderr, diffUsage, "unexpected argument %q", fs.Arg(0))
	}
	if *timeout <= 0 {
		return usageError(stderr, diffUsage, "--timeout must be positive")
	}
	if *idle <= 0 {
		return usageError(stderr, diffUsage, "--idle must be positive")
	}
	baseRef, err := resolveBaseFlag(*base, env)
	if err != nil {
		return usageError(stderr, diffUsage, "%v", err)
	}
	rep, err := behaviourDiff(ctx, diffOptions{
		dir: *dir, baseDir: *baseDir, base: baseRef, head: *head, policy: *policyPath, timeout: *timeout, idle: *idle,
	})
	if err != nil {
		return failf(stderr, "diff", "%v", err)
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			return failf(stderr, "diff", "%v", err)
		}
	} else {
		printDiff(stdout, rep)
	}
	if summary := env("GITHUB_STEP_SUMMARY"); summary != "" {
		if err := appendFile(summary, diffMarkdown(rep)); err != nil {
			return failf(stderr, "diff", "write the step summary: %v", err)
		}
	}
	return rep.ExitCode
}

// diffInstall is one side's checkout.
type diffInstall struct {
	dir   string // absolute
	paths []pathPlaceholder
}

func newDiffInstall(dir string) (diffInstall, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return diffInstall{}, err
	}
	return diffInstall{dir: abs, paths: withRealPath(abs, "<checkout>")}, nil
}

func behaviourDiff(ctx context.Context, o diffOptions) (diffReport, error) {
	r, err := resolveRange(ctx, o.dir, o.base, o.head)
	if err != nil {
		return diffReport{}, err
	}
	rep := diffReport{
		Version: 1, Base: o.base, BaseCommit: r.Base, MergeBase: r.MergeBase, Head: o.head, HeadCommit: r.Head,
		TimeoutMs: o.timeout.Milliseconds(), IdleMs: o.idle.Milliseconds(), Env: []string{}, Tools: []diffTool{}, Notes: []string{},
	}
	tools, source, err := loadTools(ctx, o.dir, r.Base, o.policy)
	if err != nil {
		return diffReport{}, err
	}
	rep.Policy = source
	if len(tools) == 0 {
		return rep, nil
	}
	if runtime.GOOS == "windows" {
		return diffReport{}, errors.New("diff runs on Linux and macOS (probes need Unix process groups and executables)")
	}
	if strings.TrimSpace(o.baseDir) == "" {
		return diffReport{}, errors.New("--base-dir is required: a checkout of the merge base with its dependencies installed (see -h)")
	}
	head, err := newDiffInstall(o.dir)
	if err != nil {
		return diffReport{}, err
	}
	baseInst, err := newDiffInstall(o.baseDir)
	if err != nil {
		return diffReport{}, err
	}
	if rep.BaseDirCommit, err = checkBaseDir(ctx, baseInst.dir, head.dir, r); err != nil {
		return diffReport{}, err
	}
	rep.BaseDir = o.baseDir

	// Every install is resolved before anything runs: a missing one on
	// either side is an error, never a partial comparison.
	type plan struct {
		tool             adapters.Tool
		baseExe, headExe map[string][2]string // command → abs, rel
	}
	plans := make([]plan, 0, len(tools))
	for _, t := range tools {
		p := plan{tool: t, baseExe: map[string][2]string{}, headExe: map[string][2]string{}}
		if len(t.Probes()) == 0 {
			rep.Notes = append(rep.Notes, fmt.Sprintf("%s has no probes, so its behaviour is not compared", t.Name()))
		}
		for _, pr := range t.Probes() {
			cmd := pr.Argv[0]
			if _, done := p.headExe[cmd]; done {
				continue
			}
			for _, side := range []struct {
				name string
				inst diffInstall
				into map[string][2]string
			}{{"base", baseInst, p.baseExe}, {"head", head, p.headExe}} {
				abs, rel, err := resolveExecutable(side.inst.dir, t.InstallRoot(), cmd)
				if err != nil {
					return diffReport{}, fmt.Errorf("tools.%s: the %s install: %w", t.Name(), side.name, err)
				}
				side.into[cmd] = [2]string{abs, rel}
			}
			if same(p.baseExe[cmd][0], p.headExe[cmd][0]) {
				return diffReport{}, fmt.Errorf("tools.%s: the base and head installs run the same file (%s): --base-dir needs its own install", t.Name(), p.headExe[cmd][0])
			}
		}
		plans = append(plans, p)
	}
	rep.Env = envNames(probeEnv("", "", func(string) bool { return false }))

	for _, p := range plans {
		dt := diffTool{Name: p.tool.Name(), Install: p.tool.InstallRoot(), Probes: []diffProbe{}}
		for _, pr := range p.tool.Probes() {
			cmd := pr.Argv[0]
			baseRun, err := runProbe(ctx, baseInst.dir, p.baseExe[cmd][0], pr.Argv, o.timeout, o.idle, p.tool.DeployVar, baseInst.paths)
			if err != nil {
				// The base is the reference: a base that cannot run the
				// tool leaves nothing to compare the head with.
				return diffReport{}, fmt.Errorf("tools.%s: the base install cannot run the tool, so there is nothing to compare: %w", p.tool.Name(), err)
			}
			headRun, err := runProbe(ctx, head.dir, p.headExe[cmd][0], pr.Argv, o.timeout, o.idle, p.tool.DeployVar, head.paths)
			if err != nil {
				return diffReport{}, fmt.Errorf("tools.%s: head install: %w", p.tool.Name(), err)
			}
			baseRun.Executable, headRun.Executable = p.baseExe[cmd][1], p.headExe[cmd][1]
			d := compareRuns(pr, baseRun, headRun)
			dt.Changed = dt.Changed || d.Changed
			dt.Probes = append(dt.Probes, d)
		}
		if dt.Changed {
			rep.ExitCode = exitSecondOpinion
		}
		rep.Tools = append(rep.Tools, dt)
	}
	return rep, nil
}

// loadTools reads tools: from the policy at the base commit, sorted by
// name, and the policy's source for the report.
func loadTools(ctx context.Context, dir, baseCommit, policyPath string) ([]adapters.Tool, string, error) {
	policyPath, err := cleanPolicyPath(policyPath)
	if err != nil {
		return nil, "", err
	}
	raw, found, err := showFile(ctx, dir, baseCommit, policyPath)
	if err != nil {
		return nil, "", fmt.Errorf("read %s from the base ref: %w", policyPath, err)
	}
	if !found {
		return nil, fmt.Sprintf("none (%s not on the base ref)", policyPath), nil
	}
	// The whole policy must be valid, lanes included.
	var f policyFile
	if _, err = parsePolicy(raw); err == nil {
		f, err = decodePolicy(raw)
	}
	if err != nil {
		return nil, "", fmt.Errorf("%s at the base ref: %w", policyPath, err)
	}
	names := make([]string, 0, len(f.Tools))
	for n := range f.Tools {
		names = append(names, n)
	}
	slices.Sort(names)
	tools := make([]adapters.Tool, 0, len(names))
	for _, name := range names {
		t, err := adapters.Load(name, f.Tools[name])
		if err != nil {
			return nil, "", fmt.Errorf("%s at the base ref: tools.%s: %w", policyPath, name, err)
		}
		tools = append(tools, t)
	}
	return tools, policyPath + "@" + baseCommit[:min(len(baseCommit), 12)], nil
}

// checkBaseDir makes sure --base-dir is a checkout of the merge base (or of
// the base commit itself) and not the head's own directory: comparing the
// head with itself, or with an unrelated commit, would pass for the wrong
// reason. It returns the base checkout's commit.
func checkBaseDir(ctx context.Context, baseDir, headDir string, r diffRange) (string, error) {
	if same(baseDir, headDir) {
		return "", fmt.Errorf("--base-dir %s is the head's own checkout: give a separate checkout of the merge base", baseDir)
	}
	out, err := git(ctx, baseDir, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return "", fmt.Errorf("--base-dir %s is not a git checkout: %w", baseDir, err)
	}
	commit := strings.TrimSpace(string(out))
	if commit != r.MergeBase && commit != r.Base {
		return "", fmt.Errorf("--base-dir %s is at %s, not the merge base %s or the base %s (git worktree add --detach <dir> %s)",
			baseDir, short(commit), short(r.MergeBase), short(r.Base), short(r.MergeBase))
	}
	return commit, nil
}

func short(commit string) string { return commit[:min(len(commit), 12)] }

// same reports whether two paths name the same file or directory.
func same(a, b string) bool {
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	if errA != nil || errB != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return ra == rb
}

// compareRuns compares one probe's two runs.
func compareRuns(p adapters.Probe, base, head probeRun) diffProbe {
	d := diffProbe{
		Probe: p.Text, Argv: p.Argv, Base: base, Head: head,
		Changes: []string{}, Added: []string{}, Removed: []string{}, Prompts: []string{},
	}
	if base.ExitCode != head.ExitCode {
		d.Changes = append(d.Changes, "exit_code")
	}
	if base.TimedOut != head.TimedOut {
		d.Changes = append(d.Changes, "timed_out")
	}
	if base.WaitingForInput != head.WaitingForInput {
		d.Changes = append(d.Changes, "waiting_for_input")
	}
	// A run that exited but left a process holding its output (a daemon,
	// a watcher, a background upload) on one side only behaves differently
	// even when the output and exit code match.
	if base.LeftRunning != head.LeftRunning {
		d.Changes = append(d.Changes, "left_running")
	}
	if !slices.Equal(base.lines, head.lines) {
		d.Changes = append(d.Changes, "output")
		added, removed := lineDiff(base.lines, head.lines)
		d.AddedCount, d.RemovedCount = len(added), len(removed)
		d.Added, d.Removed = redactAll(bounded(added, maxDiffLines)), redactAll(bounded(removed, maxDiffLines))
	}
	prompts := newPrompts(base.lines, head.lines)
	// A probe that timed out on head only, with a question as the last
	// thing it printed, was waiting for an answer.
	if head.TimedOut && !base.TimedOut && len(head.lines) > 0 {
		if last := head.lines[len(head.lines)-1]; looksLikeQuestion(last) && !slices.Contains(prompts, last) {
			prompts = append(prompts, last)
		}
	}
	// A head run seen waiting for input where the base did not wait was
	// asking something: its last line is the question, whatever its
	// wording.
	if head.WaitingForInput && !base.WaitingForInput && head.Prompt != "" && !slices.Contains(prompts, head.Prompt) {
		prompts = append(prompts, head.Prompt)
	}
	if len(prompts) > 0 {
		d.Changes = append(d.Changes, "prompt")
		d.Prompts = redactAll(bounded(prompts, maxPromptLines))
	}
	d.Changed = len(d.Changes) > 0
	return d
}

// redactAll redacts secrets a tool may print (a token in a help example,
// a key in an error message) before they reach a report or a summary.
func redactAll(lines []string) []string {
	for i, l := range lines {
		lines[i] = logging.RedactString(l)
	}
	return lines
}
