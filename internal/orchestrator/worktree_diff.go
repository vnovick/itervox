package orchestrator

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/logging"
	"github.com/vnovick/itervox/internal/workspace"
)

// worktree_diff.go — #215: an issue's worktree diff and its handoffs, for
// the diff and run-context endpoints. These run in the CALLER's goroutine
// (an HTTP handler): they read the published snapshot for the run's base
// branch and run git through workspace (gitexec) under worktreeDiffTimeout.
// They never send an event or wait for the event loop, so a busy loop
// cannot stall them and they cannot stall it.

// worktreeDiffTimeout bounds every diff request's git calls.
const worktreeDiffTimeout = 15 * time.Second

// FileDiffMaxBytes caps one file's unified diff.
const FileDiffMaxBytes = 256 * 1024

// handoffTitleScanBytes is how much of a handoff is read for its title.
const handoffTitleScanBytes = 4096

// maxHandoffRefs bounds the handoffs listed (newest kept).
const maxHandoffRefs = 50

// Diff errors.
var (
	// ErrNoWorktree: the issue has no workspace on disk (never dispatched,
	// or cleared), or it is not a git work tree.
	ErrNoWorktree = errors.New("orchestrator: issue has no worktree")
	// ErrBadDiffBase: the base parameter is not a plausible ref name.
	ErrBadDiffBase = errors.New("orchestrator: invalid base ref")
)

// diffBaseRef is what a caller-supplied base may look like: a branch or
// remote-tracking ref name, never an option or a revision expression.
var diffBaseRef = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,199}$`)

// worktreePath returns identifier's workspace directory when it exists.
func (o *Orchestrator) worktreePath(identifier string) (string, error) {
	if o.workspace == nil || identifier == "" {
		return "", ErrNoWorktree
	}
	p := o.workspace.ResolvePath(identifier)
	if p == "" {
		return "", ErrNoWorktree
	}
	if info, err := os.Stat(p); err != nil || !info.IsDir() {
		return "", ErrNoWorktree
	}
	return p, nil
}

// diffBases lists the refs to diff identifier's worktree against, best
// first: the caller's base, else the run's pull-request base (the blocker
// branch of a stacked worktree, from its run context), then the configured
// base branches. workspace.base_branch and agent.base_branch are read-only
// after startup, so no cfgMu.
func (o *Orchestrator) diffBases(identifier, base string) ([]string, error) {
	if base != "" {
		if !diffBaseRef.MatchString(base) || strings.Contains(base, "..") {
			return nil, ErrBadDiffBase
		}
		if strings.HasPrefix(base, "origin/") {
			return []string{base}, nil
		}
		return []string{"origin/" + base, base}, nil
	}
	prBase, agentBase := "", ""
	if o.cfg != nil {
		prBase, agentBase = o.cfg.Workspace.BaseBranch, o.cfg.Agent.BaseBranch
	}
	if v, ok := o.RunContext(identifier); ok && v.BaseBranch != "" {
		prBase = v.BaseBranch
	}
	return reviewBaseCandidates(prBase, agentBase), nil
}

// WorktreeDiff lists identifier's changed files against the merge base of
// its worktree's HEAD with the base branch (base overrides it). Works for
// a running issue and for a finished one whose worktree still exists.
// Off the event loop; see the file comment.
func (o *Orchestrator) WorktreeDiff(ctx context.Context, identifier, base string) (workspace.DiffSummary, error) {
	ctx, cancel := context.WithTimeout(ctx, worktreeDiffTimeout)
	defer cancel()
	return o.worktreeDiff(ctx, identifier, base)
}

func (o *Orchestrator) worktreeDiff(ctx context.Context, identifier, base string) (workspace.DiffSummary, error) {
	path, err := o.worktreePath(identifier)
	if err != nil {
		return workspace.DiffSummary{}, err
	}
	bases, err := o.diffBases(identifier, base)
	if err != nil {
		return workspace.DiffSummary{}, err
	}
	sum, err := workspace.DiffAgainstBase(ctx, path, bases)
	if errors.Is(err, workspace.ErrNotWorkTree) {
		return workspace.DiffSummary{}, ErrNoWorktree
	}
	return sum, err
}

// WorktreeFileDiff returns one changed file's unified diff, capped at
// FileDiffMaxBytes, with binary files detected. file must be in the diff
// (workspace.ErrDiffFileNotChanged otherwise). Off the event loop.
func (o *Orchestrator) WorktreeFileDiff(ctx context.Context, identifier, base, file string) (workspace.FileDiff, error) {
	ctx, cancel := context.WithTimeout(ctx, worktreeDiffTimeout)
	defer cancel()
	sum, err := o.worktreeDiff(ctx, identifier, base)
	if err != nil {
		return workspace.FileDiff{}, err
	}
	path, err := o.worktreePath(identifier)
	if err != nil {
		return workspace.FileDiff{}, err
	}
	return workspace.FileDiffAgainst(ctx, path, sum, file, FileDiffMaxBytes)
}

// HandoffRef is one handoff file in an issue's worktree.
type HandoffRef struct {
	// Path is relative to the worktree (.itervox/handoff/<file>).
	Path string
	// Title is the file's first Markdown heading, redacted and bounded, or
	// its name when it has none.
	Title string
	// Partial marks a run that did not finish (<name>.partial.md).
	Partial bool
}

// Handoffs lists the handoffs in identifier's worktree, oldest first (the
// ISO8601 file-name prefix orders them). Nil when there is no worktree.
// Reads files only; off the event loop.
func (o *Orchestrator) Handoffs(identifier string) []HandoffRef {
	path, err := o.worktreePath(identifier)
	if err != nil {
		return nil
	}
	dir := filepath.Join(path, HandoffDirRelPath)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.Type().IsRegular() && strings.HasSuffix(e.Name(), ".md") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	names = names[max(0, len(names)-maxHandoffRefs):]
	refs := make([]HandoffRef, 0, len(names))
	for _, n := range names {
		refs = append(refs, HandoffRef{
			Path:    HandoffDirRelPath + "/" + n,
			Title:   handoffTitle(filepath.Join(dir, n), n),
			Partial: strings.HasSuffix(n, ".partial.md"),
		})
	}
	return refs
}

// handoffTitle returns the first Markdown heading of the file's head.
func handoffTitle(path, fallback string) string {
	f, err := os.Open(path)
	if err != nil {
		return fallback
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(io.LimitReader(f, handoffTitleScanBytes))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "#") {
			if t := strings.TrimSpace(strings.TrimLeft(line, "#")); t != "" {
				return boundUTF8(strings.TrimSpace(logging.RedactString(t)), 200)
			}
		}
	}
	return fallback
}
