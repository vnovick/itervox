// Package extsessions discovers agent CLI sessions started by hand on the
// daemon's host (#178 step 3): Claude Code transcripts under
// ~/.claude/projects/<encoded-cwd>/<session-id>.jsonl and Codex rollouts
// under ~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl. It reads them
// read-only, tails their transcripts for the Sessions panel and decides,
// without touching the process, whether the CLI that wrote one may still be
// running.
//
// Privacy: discovery is limited to a recency window and, by default, to
// sessions whose working directory is inside the daemon's project root(s).
// A transcript is read only for a session discovery returned; no caller
// passes an arbitrary path.
//
// The record formats are the CLIs' private on-disk formats, not APIs. The
// parsers are tolerant: unknown record types are skipped, malformed lines
// are counted and skipped, and each session reports the format it was read
// as (Format) so a future layout change is visible instead of silent.
//
// The package imports only the standard library and internal/logging.
package extsessions

import (
	"cmp"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Backends of a discovered session.
const (
	BackendClaude = "claude"
	BackendCodex  = "codex"
)

// Formats a transcript was read as.
const (
	// FormatClaudeJSONL is Claude Code's per-session JSONL: one record per
	// line with top-level type, sessionId, cwd and message fields.
	FormatClaudeJSONL = "claude-jsonl/1"
	// FormatCodexRollout is the Codex rollout with an envelope per line:
	// {"timestamp","type":"session_meta"|"response_item"|"event_msg"|…,"payload"}.
	FormatCodexRollout = "codex-rollout/2"
	// FormatCodexLegacy is the earlier Codex rollout: a bare header object
	// ({"id","timestamp","instructions"}) followed by bare response items.
	// It records no working directory.
	FormatCodexLegacy = "codex-rollout/1"
)

// Scope values of Options.Scope.
const (
	ScopeProject = "project" // only sessions whose cwd is inside Options.Roots
	ScopeAll     = "all"     // every session in the window
)

// Session is one discovered session. It holds no reference fields so it can
// be copied by value (it lives in orchestrator.State).
type Session struct {
	// ID is the CLI's own session ID (what `claude --resume` and
	// `codex exec resume` take).
	ID      string
	Backend string
	// Cwd is the working directory the session ran in; "" when the
	// transcript does not record one (FormatCodexLegacy).
	Cwd string
	// Path is the transcript file. Never sent to clients.
	Path   string
	Format string
	// Title is the CLI's own title for the session when it records one.
	Title string
	// CLIVersion is the version of the CLI that wrote the transcript.
	CLIVersion string
	StartedAt  time.Time
	// UpdatedAt is the transcript's last modification.
	UpdatedAt time.Time
	// Running is whether the CLI that wrote the session may still be
	// running (see Liveness); RunningReason says why.
	Running       bool
	RunningReason string
	// PID is the claude/codex process liveness tied to the session (0:
	// none). Used to find the tmux pane the session runs in.
	PID int
	// TmuxPane is the tmux pane hosting PID, when one was matched (#178
	// tmux control); zero otherwise. Filled outside this package.
	TmuxPane TmuxPane
}

// TmuxPane names the tmux pane a session runs in.
type TmuxPane struct {
	ID      string // "%12"
	Label   string // "session:window.pane"
	Session string // the tmux session name
}

// Options configure Discover.
type Options struct {
	// ClaudeProjectsDir is Claude Code's projects directory; "" skips Claude.
	ClaudeProjectsDir string
	// CodexSessionsDir is Codex's sessions directory; "" skips Codex.
	CodexSessionsDir string
	// Window is how far back a session's last write may be.
	Window time.Duration
	// Scope is ScopeProject (default) or ScopeAll.
	Scope string
	// Roots are the directories a session's cwd must be inside under
	// ScopeProject. Empty under ScopeProject discovers nothing.
	Roots []string
	// Exclude are directories whose sessions are never listed (itervox's
	// own workspace root), whatever the scope.
	Exclude []string
	// Limit caps the sessions returned, most recent first (0: DefaultLimit).
	Limit int
}

// DefaultLimit caps a discovery pass.
const DefaultLimit = 50

// DefaultClaudeProjectsDir is $CLAUDE_CONFIG_DIR/projects, else
// ~/.claude/projects; "" when no home directory is known.
func DefaultClaudeProjectsDir() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return filepath.Join(d, "projects")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "projects")
}

// DefaultCodexSessionsDir is $CODEX_HOME/sessions, else ~/.codex/sessions;
// "" when no home directory is known.
func DefaultCodexSessionsDir() string {
	if d := os.Getenv("CODEX_HOME"); d != "" {
		return filepath.Join(d, "sessions")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".codex", "sessions")
}

// Discover lists the sessions written to within opts.Window before now,
// in scope, most recently updated first. A missing CLI directory is not an
// error; an unreadable transcript is skipped.
func Discover(opts Options, now time.Time) ([]Session, error) {
	since := now.Add(-opts.Window)
	roots := cleanDirs(opts.Roots)
	exclude := cleanDirs(opts.Exclude)
	projectScope := opts.Scope != ScopeAll
	if projectScope && len(roots) == 0 {
		return nil, nil
	}
	var out []Session
	var errs []error
	if opts.ClaudeProjectsDir != "" {
		var prefixes []string
		if projectScope {
			for _, r := range roots {
				prefixes = append(prefixes, EncodeClaudeProjectDir(r))
			}
		}
		s, err := discoverClaude(opts.ClaudeProjectsDir, since, prefixes)
		out = append(out, s...)
		errs = append(errs, err)
	}
	if opts.CodexSessionsDir != "" {
		s, err := discoverCodex(opts.CodexSessionsDir, since)
		out = append(out, s...)
		errs = append(errs, err)
	}
	kept := out[:0]
	for _, s := range out {
		if s.ID == "" || insideAny(s.Cwd, exclude) {
			continue
		}
		if projectScope && !insideAny(s.Cwd, roots) {
			continue
		}
		kept = append(kept, s)
	}
	slices.SortStableFunc(kept, func(a, b Session) int {
		if c := b.UpdatedAt.Compare(a.UpdatedAt); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
	if limit := cmp.Or(opts.Limit, DefaultLimit); len(kept) > limit {
		kept = kept[:limit]
	}
	return kept, errors.Join(errs...)
}

// EncodeClaudeProjectDir is how Claude Code names a project directory under
// projects/: every character that is not an ASCII letter or digit becomes
// '-'. Discovery only uses it as a prefilter (a session under root lives in
// a directory whose name starts with root's encoding); the cwd recorded in
// the transcript decides.
func EncodeClaudeProjectDir(dir string) string {
	b := []byte(dir)
	for i, c := range b {
		if !isASCIIAlnum(c) {
			b[i] = '-'
		}
	}
	return string(b)
}

func isASCIIAlnum(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// cleanDirs absolutises, cleans and resolves symlinks of dirs, keeping both
// spellings when they differ (macOS /var → /private/var).
func cleanDirs(dirs []string) []string {
	var out []string
	for _, d := range dirs {
		if strings.TrimSpace(d) == "" {
			continue
		}
		abs, err := filepath.Abs(d)
		if err != nil {
			continue
		}
		out = append(out, filepath.Clean(abs))
		if real, err := filepath.EvalSymlinks(abs); err == nil && real != abs {
			out = append(out, filepath.Clean(real))
		}
	}
	return out
}

// insideAny reports whether dir is one of roots or below one of them. An
// empty dir is inside nothing.
func insideAny(dir string, roots []string) bool {
	if dir == "" || !filepath.IsAbs(dir) {
		return false
	}
	candidates := []string{filepath.Clean(dir)}
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		candidates = append(candidates, filepath.Clean(real))
	}
	for _, c := range candidates {
		for _, r := range roots {
			rel, err := filepath.Rel(r, c)
			if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return true
			}
		}
	}
	return false
}
