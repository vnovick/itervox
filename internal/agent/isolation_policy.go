package agent

import (
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/vnovick/itervox/internal/config"
)

// The sandbox lists (#258), in one place. `itervox doctor` prints them and
// docs/guides/isolation describes them; change all three together.
//
// Paths are relative to the directory named in each list's comment and may
// contain filepath.Match patterns. A path that does not exist when a run
// starts is left out (a write path is not created, a deny path cannot be
// created afterwards because its parent is read-only unless writable by
// another rule).
var (
	// IsolationHomeWrite is the agent CLIs' own state, relative to $HOME:
	// sessions, settings, credentials refresh, caches.
	IsolationHomeWrite = []string{".claude", ".claude.json", ".codex", ".cache"}

	// IsolationHomeDeny is never readable, relative to $HOME: credentials
	// and the daemon's own per-user token stores and dashboard tokens.
	IsolationHomeDeny = []string{
		".ssh", ".aws", ".gnupg", ".config/gh", ".netrc", ".docker/config.json", ".kube",
		".itervox/hub", ".itervox/logs/*/*/api-token",
	}

	// IsolationProjectDeny is never readable, relative to the directory
	// holding WORKFLOW.md: the daemon's .env and its token files.
	IsolationProjectDeny = []string{".itervox/.env", ".itervox/*token*"}
)

// IsolationInputs is what one run's sandbox policy is built from.
type IsolationInputs struct {
	// Workspace is the issue's workspace: writable.
	Workspace string
	// WorkspaceRoot holds every workspace; all of it but Workspace is
	// hidden, so a run cannot read or change another issue's worktree.
	WorkspaceRoot string
	// ProjectDir is the directory holding WORKFLOW.md (IsolationProjectDeny).
	ProjectDir string
	// Home is $HOME (IsolationHomeWrite, IsolationHomeDeny).
	Home string
	// TempDir is the system temp directory: writable.
	TempDir string
	// ExtraWrite / ExtraDeny are the profile's isolation_extra_write and
	// isolation_extra_deny (absolute paths).
	ExtraWrite []string
	ExtraDeny  []string
}

// IsolationPolicy is what a sandbox enforces. Every path is absolute,
// symlink-resolved and exists. Everything not listed is readable and not
// writable. Where a path is in more than one list, or one list's path lies
// inside another's, the more specific (deeper) rule wins, and on the same
// path Deny wins over Read over Write.
type IsolationPolicy struct {
	// Write is readable and writable.
	Write []string
	// Read is readable even though it lies inside a denied path (the git
	// directory a worktree shares, under a hidden workspace root).
	Read []string
	// Deny is neither readable nor writable.
	Deny []string
}

// RunIsolationInputs fills IsolationInputs for a run of profile in
// workspace, with this process's $HOME and temp directory. workspaceRoot
// and projectDir are config.Workspace.Root and the WORKFLOW.md directory.
func RunIsolationInputs(workspace, workspaceRoot, projectDir string, profile config.AgentProfile) IsolationInputs {
	home, _ := os.UserHomeDir()
	return IsolationInputs{
		Workspace:     absOrEmpty(workspace),
		WorkspaceRoot: absOrEmpty(workspaceRoot),
		ProjectDir:    absOrEmpty(projectDir),
		Home:          home,
		TempDir:       os.TempDir(),
		ExtraWrite:    profile.IsolationExtraWrite,
		ExtraDeny:     profile.IsolationExtraDeny,
	}
}

// absOrEmpty makes p absolute against the daemon's working directory (a
// relative --workflow path), keeping "" as "".
func absOrEmpty(p string) string {
	if p == "" {
		return ""
	}
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}

// BuildIsolationPolicy builds a run's policy from in. The workspace's git
// directories are read from its .git file (no git subprocess).
func BuildIsolationPolicy(in IsolationInputs) IsolationPolicy {
	var write, read, deny []string
	if in.Workspace != "" {
		write = append(write, in.Workspace)
		gw, gr := worktreeGitDirs(in.Workspace)
		write = append(write, gw...)
		read = append(read, gr...)
	}
	if in.TempDir != "" {
		write = append(write, in.TempDir)
	}
	write = append(write, joinAll(in.Home, IsolationHomeWrite)...)
	write = append(write, in.ExtraWrite...)

	deny = append(deny, joinAll(in.Home, IsolationHomeDeny)...)
	deny = append(deny, joinAll(in.ProjectDir, IsolationProjectDeny)...)
	if in.WorkspaceRoot != "" && in.WorkspaceRoot != in.Workspace {
		deny = append(deny, in.WorkspaceRoot)
	}
	deny = append(deny, in.ExtraDeny...)

	return IsolationPolicy{
		Write: resolveExisting(write),
		Read:  resolveExisting(read),
		Deny:  resolveExisting(expandGlobs(deny)),
	}
}

// joinAll joins each rel onto base; nothing when base is empty.
func joinAll(base string, rels []string) []string {
	if base == "" {
		return nil
	}
	out := make([]string, 0, len(rels))
	for _, r := range rels {
		out = append(out, filepath.Join(base, r))
	}
	return out
}

// expandGlobs replaces each pattern with its matches (a plain path stays).
func expandGlobs(paths []string) []string {
	var out []string
	for _, p := range paths {
		if !strings.ContainsAny(p, "*?[") {
			out = append(out, p)
			continue
		}
		matches, _ := filepath.Glob(p)
		out = append(out, matches...)
	}
	return out
}

// resolveExisting returns the existing paths, absolute and
// symlink-resolved, without duplicates. A sandbox matches the real path, so
// a symlinked ~/.ssh must be denied where it points.
func resolveExisting(paths []string) []string {
	var out []string
	for _, p := range paths {
		if p == "" || !filepath.IsAbs(p) {
			continue
		}
		r, err := filepath.EvalSymlinks(p)
		if err != nil {
			continue
		}
		out = append(out, filepath.Clean(r))
	}
	return dedupe(out)
}

// dedupe drops repeated entries, keeping the first.
func dedupe(paths []string) []string {
	var out []string
	for _, p := range paths {
		if !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	return out
}

// worktreeGitDirs returns what a git worktree at ws needs outside itself:
// its private git dir (.git/worktrees/<name>) and the shared objects, refs
// and reflogs as writable, and the shared git dir (config, hooks,
// packed-refs) as readable. A workspace whose .git is a directory (a full
// clone) needs nothing extra.
func worktreeGitDirs(ws string) (write, read []string) {
	data, err := os.ReadFile(filepath.Join(ws, ".git"))
	if err != nil {
		return nil, nil
	}
	line, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir:")
	if !ok {
		return nil, nil
	}
	gitDir := strings.TrimSpace(line)
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(ws, gitDir)
	}
	write = append(write, gitDir)
	common := gitDir
	if c, err := os.ReadFile(filepath.Join(gitDir, "commondir")); err == nil {
		common = strings.TrimSpace(string(c))
		if !filepath.IsAbs(common) {
			common = filepath.Join(gitDir, common)
		}
	}
	read = append(read, common)
	write = append(write, filepath.Join(common, "objects"), filepath.Join(common, "refs"), filepath.Join(common, "logs"))
	return write, read
}

// isolationRuleKind is what a rule grants. The order is the tie-break on
// the same path: later kinds are applied later and win.
type isolationRuleKind int

const (
	ruleWrite isolationRuleKind = iota
	ruleRead
	ruleDeny
)

// isolationRule is one path of a policy, for the mechanisms that apply
// rules in order (bubblewrap mounts, sandbox-exec's last-match-wins).
type isolationRule struct {
	kind isolationRuleKind
	path string
	dir  bool
}

// orderedRules returns p's paths shallowest first (then write, read, deny
// on the same depth), so applying them in order lets the more specific
// rule win: a workspace re-exposed under the hidden workspace root, a
// ~/.ssh hidden inside a writable home.
func (p IsolationPolicy) orderedRules() []isolationRule {
	var rules []isolationRule
	add := func(kind isolationRuleKind, paths []string) {
		for _, path := range paths {
			fi, err := os.Stat(path)
			if err != nil {
				continue
			}
			rules = append(rules, isolationRule{kind: kind, path: path, dir: fi.IsDir()})
		}
	}
	add(ruleWrite, p.Write)
	add(ruleRead, p.Read)
	add(ruleDeny, p.Deny)
	sort.SliceStable(rules, func(i, j int) bool {
		di, dj := pathDepth(rules[i].path), pathDepth(rules[j].path)
		if di != dj {
			return di < dj
		}
		return rules[i].kind < rules[j].kind
	})
	return rules
}

// pathDepth is the number of components in a clean absolute path.
func pathDepth(p string) int {
	if p == "/" {
		return 0
	}
	return strings.Count(p, string(filepath.Separator))
}

// within reports whether p is root or lies under it.
func within(p, root string) bool {
	if root == "/" {
		return true
	}
	return p == root || strings.HasPrefix(p, root+string(filepath.Separator))
}
