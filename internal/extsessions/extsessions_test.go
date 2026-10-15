package extsessions

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

const (
	idClaudeApp     = "11111111-1111-4111-8111-111111111111"
	idClaudeElse    = "22222222-2222-4222-8222-222222222222"
	idClaudeWeb     = "33333333-3333-4333-8333-333333333333"
	idCodexApp      = "44444444-4444-4444-8444-444444444444"
	idCodexLegacy   = "55555555-5555-4555-8555-555555555555"
	idClaudeSibling = "66666666-6666-4666-8666-666666666666"
	idClaudeWS      = "77777777-7777-4777-8777-777777777777"
	idClaudeOld     = "88888888-8888-4888-8888-888888888888"
	idClaudeMismatc = "99999999-9999-4999-8999-999999999999"
	idCodexOldDir   = "abababab-abab-4bab-8bab-abababababab"
)

// fixtures copies testdata into a temp dir and stamps every transcript as
// written an hour before now, except idClaudeOld (three days).
func fixtures(t *testing.T, now time.Time) (claudeDir, codexDir string) {
	t.Helper()
	root := t.TempDir()
	err := filepath.WalkDir("testdata", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel("testdata", path)
		dst := filepath.Join(root, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.WriteFile(dst, b, 0o644); err != nil {
			return err
		}
		mod := now.Add(-time.Hour)
		if strings.Contains(path, idClaudeOld) {
			mod = now.Add(-72 * time.Hour)
		}
		return os.Chtimes(dst, mod, mod)
	})
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(root, "claude", "projects"), filepath.Join(root, "codex", "sessions")
}

func ids(ss []Session) []string {
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		out = append(out, s.ID)
	}
	slices.Sort(out)
	return out
}

func find(t *testing.T, ss []Session, id string) Session {
	t.Helper()
	for _, s := range ss {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("session %s not discovered (got %v)", id, ids(ss))
	return Session{}
}

// #178 step 3: hand-started sessions of both CLIs are discovered from
// their on-disk layouts, with their metadata, within the project root.
func TestDiscoverFindsClaudeAndCodexSessionsInProjectScope(t *testing.T) {
	now := time.Now()
	claude, codex := fixtures(t, now)
	got, err := Discover(Options{
		ClaudeProjectsDir: claude, CodexSessionsDir: codex,
		Window: 24 * time.Hour, Roots: []string{"/work/app"}, Exclude: []string{"/work/ws"},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{idClaudeApp, idClaudeWeb, idCodexApp}
	if g := ids(got); !slices.Equal(g, want) {
		t.Fatalf("discovered %v, want %v", g, want)
	}
	c := find(t, got, idClaudeApp)
	if c.Backend != BackendClaude || c.Cwd != "/work/app" || c.Format != FormatClaudeJSONL ||
		c.CLIVersion != "2.1.0" || c.Title != "Add health check" || c.StartedAt.IsZero() {
		t.Fatalf("claude session metadata = %+v", c)
	}
	x := find(t, got, idCodexApp)
	if x.Backend != BackendCodex || x.Cwd != "/work/app" || x.Format != FormatCodexRollout || x.CLIVersion != "0.50.0" {
		t.Fatalf("codex session metadata = %+v", x)
	}
}

// Privacy: by default only sessions whose cwd is inside the project root
// are listed — not a sibling directory sharing its prefix, not an unrelated
// directory, not a legacy rollout that records no cwd — and nothing at all
// without a root.
func TestDiscoverExcludesSessionsOutsideTheProjectRootByDefault(t *testing.T) {
	now := time.Now()
	claude, codex := fixtures(t, now)
	got, err := Discover(Options{ClaudeProjectsDir: claude, CodexSessionsDir: codex,
		Window: 24 * time.Hour, Roots: []string{"/work/app"}}, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range got {
		if s.ID == idClaudeElse || s.ID == idClaudeSibling || s.ID == idCodexLegacy {
			t.Fatalf("session %s (cwd %q) is outside the project root but was listed", s.ID, s.Cwd)
		}
	}
	none, err := Discover(Options{ClaudeProjectsDir: claude, CodexSessionsDir: codex, Window: 24 * time.Hour}, now)
	if err != nil || len(none) != 0 {
		t.Fatalf("project scope without a root listed %v (err %v)", ids(none), err)
	}
}

// Scope "all" lists other directories (and cwd-less legacy rollouts) but
// never the itervox workspace root, and a file whose records name another
// session or a sub-agent transcript is never a session.
func TestDiscoverScopeAllIncludesOtherDirectoriesButNotTheWorkspaceRoot(t *testing.T) {
	now := time.Now()
	claude, codex := fixtures(t, now)
	got, err := Discover(Options{ClaudeProjectsDir: claude, CodexSessionsDir: codex,
		Window: 24 * time.Hour, Scope: ScopeAll, Exclude: []string{"/work/ws"}}, now)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{idClaudeApp, idClaudeElse, idClaudeWeb, idCodexApp, idCodexLegacy, idClaudeSibling}
	slices.Sort(want)
	if g := ids(got); !slices.Equal(g, want) {
		t.Fatalf("discovered %v, want %v", g, want)
	}
	if l := find(t, got, idCodexLegacy); l.Format != FormatCodexLegacy || l.Cwd != "" {
		t.Fatalf("legacy rollout = %+v", l)
	}
}

// Only sessions written to within the window are listed; Codex date
// directories older than the window are not even walked.
func TestDiscoverHonoursTheRecencyWindow(t *testing.T) {
	now := time.Now()
	claude, codex := fixtures(t, now)
	got, _ := Discover(Options{ClaudeProjectsDir: claude, CodexSessionsDir: codex,
		Window: 24 * time.Hour, Scope: ScopeAll}, now)
	for _, s := range got {
		if s.ID == idClaudeOld {
			t.Fatal("a session last written 72h ago is listed with a 24h window")
		}
		if s.ID == idCodexOldDir {
			t.Fatal("a rollout in a 2025 date directory is listed with a 24h window")
		}
	}
	wide, _ := Discover(Options{ClaudeProjectsDir: claude, Window: 96 * time.Hour, Scope: ScopeAll}, now)
	find(t, wide, idClaudeOld)
	// Newest first.
	if len(got) > 1 && got[0].UpdatedAt.Before(got[len(got)-1].UpdatedAt) {
		t.Fatal("sessions are not ordered newest first")
	}
}

func TestEncodeClaudeProjectDir(t *testing.T) {
	if g := EncodeClaudeProjectDir("/Users/a.b/my_repo"); g != "-Users-a-b-my-repo" {
		t.Fatalf("encoded %q", g)
	}
}
