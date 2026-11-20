package referee

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/vnovick/itervox/internal/referee/adapters"
)

const envcheckUsage = `usage: referee envcheck --base <ref> [--head HEAD] [--policy .referee.yml] [--json] [--dir .]

Fails when an installed tool mentions a deploy-target environment variable
that the tool's guard file does not classify. A dependency bump can make a
deploy CLI start reading a new variable without touching any CI file; this
catches it before the deploy step runs.

  tools:                         # in .referee.yml, read from the BASE ref
    convex:                      # a built-in adapter (convex, inngest, supabase, vercel)
      guard: scripts/deploy-guard.mjs
    mycli:                       # a generic adapter
      guard: scripts/deploy-guard.mjs
      env_scan: node_modules/mycli/dist/**/*.js
      deploy_vars: [MYCLI_*]

The policy comes from the base ref. The installed tool is read from the
working tree (run envcheck after installing dependencies). The guard is read
from git at both the base ref and the head: a variable is classified only
when both versions mention it, so a pull request can neither weaken the guard
nor classify a variable in the same change that starts reading it.

A scan finds the variable names a tool's files mention, not every way it can
read one (a name built at run time is not seen). Without tools: on the base
ref there is nothing to check (exit 0). --base defaults to
origin/$GITHUB_BASE_REF in a pull_request workflow.

Exit codes: 0 every variable classified (or no tools:), 20 an unclassified
variable, 2 usage or error (a tool's install root or guard that cannot be read).`

type envReport struct {
	Version    int          `json:"version"`
	ExitCode   int          `json:"exit_code"`
	Base       string       `json:"base"`
	BaseCommit string       `json:"base_commit"`
	Head       string       `json:"head"`
	HeadCommit string       `json:"head_commit"`
	Policy     string       `json:"policy"`
	Tools      []envTool    `json:"tools"`
	Findings   []envFinding `json:"findings"`
	Notes      []string     `json:"notes"`
}

type envTool struct {
	Name      string           `json:"name"`
	Guard     string           `json:"guard"`
	Install   string           `json:"install"`
	Variables []string         `json:"variables"` // deploy-target variables the install mentions
	Probes    []adapters.Probe `json:"probes"`
}

type envFinding struct {
	Tool     string `json:"tool"`
	Variable string `json:"variable"`
	Path     string `json:"path"` // repository-relative
	Line     int    `json:"line"`
	Guard    string `json:"guard"`
	Message  string `json:"message"`
}

func envcheckCmd(ctx context.Context, args []string, env envFunc, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("referee envcheck", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { _, _ = fmt.Fprintln(stderr, envcheckUsage) }
	base := fs.String("base", "", "base ref the change merges into (its policy and guard are the ones used)")
	head := fs.String("head", "HEAD", "head ref under review (its guard must still classify)")
	policyPath := fs.String("policy", defaultPolicyPath, "policy file path, read from the base ref")
	asJSON := fs.Bool("json", false, "print a JSON report")
	dir := fs.String("dir", ".", "repository root, with the head's dependencies installed")
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	if fs.NArg() > 0 {
		return usageError(stderr, envcheckUsage, "unexpected argument %q", fs.Arg(0))
	}
	baseRef, err := resolveBaseFlag(*base, env)
	if err != nil {
		return usageError(stderr, envcheckUsage, "%v", err)
	}
	rep, err := envcheck(ctx, *dir, baseRef, *head, *policyPath)
	if err != nil {
		return failf(stderr, "envcheck", "%v", err)
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			return failf(stderr, "envcheck", "%v", err)
		}
	} else {
		printEnvcheck(stdout, rep)
	}
	if summary := env("GITHUB_STEP_SUMMARY"); summary != "" {
		if err := appendFile(summary, envcheckMarkdown(rep)); err != nil {
			return failf(stderr, "envcheck", "write the step summary: %v", err)
		}
	}
	return rep.ExitCode
}

func envcheck(ctx context.Context, dir, base, head, policyPath string) (envReport, error) {
	policyPath, err := cleanPolicyPath(policyPath)
	if err != nil {
		return envReport{}, err
	}
	baseCommit, err := resolveCommit(ctx, dir, base)
	if err != nil {
		return envReport{}, err
	}
	headCommit, err := resolveCommit(ctx, dir, head)
	if err != nil {
		return envReport{}, err
	}
	rep := envReport{
		Version: 1, Base: base, BaseCommit: baseCommit, Head: head, HeadCommit: headCommit,
		Policy: fmt.Sprintf("none (%s not on the base ref)", policyPath),
		Tools:  []envTool{}, Findings: []envFinding{}, Notes: []string{},
	}
	raw, found, err := showFile(ctx, dir, baseCommit, policyPath)
	if err != nil {
		return envReport{}, fmt.Errorf("read %s from the base ref: %w", policyPath, err)
	}
	var f policyFile
	if found {
		// The whole policy must be valid, lanes included: a broken policy
		// file is an error for every command, never a partial read.
		if _, err = parsePolicy(raw); err == nil {
			f, err = decodePolicy(raw)
		}
		if err != nil {
			return envReport{}, fmt.Errorf("%s at the base ref: %w", policyPath, err)
		}
		rep.Policy = policyPath + "@" + baseCommit[:min(len(baseCommit), 12)]
	}
	names := make([]string, 0, len(f.Tools))
	for n := range f.Tools {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, name := range names {
		tool, err := adapters.Load(name, f.Tools[name])
		if err != nil {
			return envReport{}, fmt.Errorf("%s at the base ref: tools.%s: %w", policyPath, name, err)
		}
		t, findings, err := checkTool(ctx, dir, baseCommit, headCommit, tool)
		if err != nil {
			return envReport{}, fmt.Errorf("tools.%s: %w", name, err)
		}
		rep.Tools = append(rep.Tools, t)
		rep.Findings = append(rep.Findings, findings...)
	}
	for _, a := range adapters.Builtins() {
		if _, ok := f.Tools[a.Name()]; !ok && a.Detect(dir) {
			rep.Notes = append(rep.Notes, fmt.Sprintf("%s looks used here but is not under tools: in %s on the base ref, so it is not checked", a.Name(), policyPath))
		}
	}
	if len(rep.Findings) > 0 {
		rep.ExitCode = exitHuman
	}
	return rep, nil
}

// checkTool scans one tool's install and compares what it mentions with
// what its guard classifies at the base ref and at the head.
func checkTool(ctx context.Context, dir, baseCommit, headCommit string, tool adapters.Tool) (envTool, []envFinding, error) {
	baseGuard, err := readGuard(ctx, dir, baseCommit, tool.Guard, "the base ref")
	if err != nil {
		return envTool{}, nil, err
	}
	headGuard, err := readGuard(ctx, dir, headCommit, tool.Guard, "the head")
	if err != nil {
		return envTool{}, nil, err
	}
	reads, err := tool.EnvReads(filepath.Join(dir, filepath.FromSlash(tool.InstallRoot())))
	if err != nil {
		return envTool{}, nil, err
	}
	t := envTool{Name: tool.Name(), Guard: tool.Guard, Install: tool.InstallRoot(), Variables: []string{}, Probes: tool.Probes()}
	var out []envFinding
	for _, r := range reads {
		t.Variables = append(t.Variables, r.Name)
		var msg string
		switch {
		case baseGuard[r.Name] && headGuard[r.Name]:
			continue
		case baseGuard[r.Name]:
			msg = fmt.Sprintf("%s mentions %s; the guard classifies it on the base ref but no longer at the head", tool.Name(), r.Name)
		default:
			msg = fmt.Sprintf("%s mentions %s, a deploy-target variable the guard on the base ref does not classify", tool.Name(), r.Name)
		}
		out = append(out, envFinding{
			Tool: tool.Name(), Variable: r.Name, Path: tool.InstallRoot() + "/" + r.Path, Line: r.Line,
			Guard: tool.Guard, Message: msg,
		})
	}
	return t, out, nil
}

// readGuard is the set of words in the guard at commit. A guard that is not
// there is an error: a check with nothing to compare against must not pass.
func readGuard(ctx context.Context, dir, commit, guard, where string) (map[string]bool, error) {
	raw, found, err := showFile(ctx, dir, commit, guard)
	if err != nil {
		return nil, fmt.Errorf("read guard %s at %s: %w", guard, where, err)
	}
	if !found {
		return nil, fmt.Errorf("guard %s is not in %s", guard, where)
	}
	return adapters.Mentions(raw), nil
}

func printEnvcheck(w io.Writer, rep envReport) {
	if len(rep.Tools) == 0 {
		_, _ = fmt.Fprintf(w, "envcheck: no tools: configured (policy: %s); nothing to check\n", rep.Policy)
	} else if len(rep.Findings) == 0 {
		_, _ = fmt.Fprintf(w, "envcheck: every deploy-target variable is classified (policy: %s)\n", rep.Policy)
	} else {
		_, _ = fmt.Fprintf(w, "envcheck: %d unclassified deploy-target variable(s) (policy: %s)\n", len(rep.Findings), rep.Policy)
	}
	for _, f := range rep.Findings {
		_, _ = fmt.Fprintf(w, "  %s:%d  [%s] %s\n      add %s to %s (on the base branch first)\n",
			displayPath(f.Path), f.Line, f.Tool, displayPath(f.Message), f.Variable, displayPath(f.Guard))
	}
	for _, t := range rep.Tools {
		_, _ = fmt.Fprintf(w, "  %s: %d deploy-target variable(s) in %s, guard %s\n", t.Name, len(t.Variables), displayPath(t.Install), displayPath(t.Guard))
	}
	for _, n := range rep.Notes {
		_, _ = fmt.Fprintf(w, "note: %s\n", displayPath(n))
	}
}

func envcheckMarkdown(rep envReport) string {
	var b strings.Builder
	b.WriteString("### referee envcheck\n\n")
	switch {
	case len(rep.Tools) == 0:
		fmt.Fprintf(&b, "No `tools:` in the policy (`%s`); nothing to check.\n\n", mdCode(rep.Policy))
	case len(rep.Findings) == 0:
		b.WriteString("Every deploy-target variable the installed tools mention is classified by their guard.\n\n")
	default:
		fmt.Fprintf(&b, "%d unclassified deploy-target variable(s):\n\n", len(rep.Findings))
		for _, f := range rep.Findings {
			fmt.Fprintf(&b, "- `%s`:%s **%s** `%s`: %s (guard `%s`)\n", mdCode(f.Path), strconv.Itoa(f.Line), mdCode(f.Tool), mdCode(f.Variable), mdCode(f.Message), mdCode(f.Guard))
		}
		b.WriteString("\n")
	}
	for _, n := range rep.Notes {
		fmt.Fprintf(&b, "Note: %s\n\n", mdCode(n))
	}
	return b.String()
}
