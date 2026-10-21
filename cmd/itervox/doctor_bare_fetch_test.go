package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/gitexec"
	"github.com/vnovick/itervox/internal/workspace"
)

// bareFetchFixture makes <root>/.bare with the given fetch refspec and
// fetch status, and a config that uses it.
func bareFetchFixture(t *testing.T, refspec string, status *workspace.BareFetchStatus) *config.Config {
	t.Helper()
	root := t.TempDir()
	bare := workspace.BarePath(root)
	for _, args := range [][]string{
		{"init", "-q", "--bare", bare},
		{"-C", bare, "config", "remote.origin.url", "https://example.invalid/o/r.git"},
		{"-C", bare, "config", "remote.origin.fetch", refspec},
	} {
		if out, err := gitexec.Command(context.Background(), root, args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	if status != nil {
		data, err := json.Marshal(status)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(bare, workspace.BareFetchStatusFile), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &config.Config{}
	cfg.Workspace.Root = root
	cfg.Workspace.Worktree = true
	cfg.Workspace.CloneURL = "https://example.invalid/o/r.git"
	return cfg
}

// TestDoctorReportsStaleBareFetch (#195): doctor reports a bare clone whose
// fetch is failing (ERROR, exit 1), stale (WARNING), or still on the
// pre-#195 refspec (WARNING), and stays quiet for a healthy one.
func TestDoctorReportsStaleBareFetch(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	report := func(c BareFetchCheck) (string, int) {
		r := DoctorReport{Workflow: "WORKFLOW.md", SchemaPassed: true, BareFetch: c}
		return renderDoctorReport(r), doctorExitCode(r)
	}

	// Stale success on the legacy refspec.
	c := checkBareFetch(ctx, bareFetchFixture(t, workspace.LegacyBareFetchRefspec,
		&workspace.BareFetchStatus{LastSuccessAt: now.Add(-72 * time.Hour)}), now)
	out, code := report(c)
	if !c.Ran || !c.LegacyRefspec || !c.Stale || c.Failing {
		t.Fatalf("stale legacy clone: got %+v", c)
	}
	for _, want := range []string{"still fetches with " + workspace.LegacyBareFetchRefspec, "was last fetched 72h0m0s ago"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if code != 0 {
		t.Errorf("stale is a warning, exit = %d", code)
	}

	// Failing fetch.
	c = checkBareFetch(ctx, bareFetchFixture(t, workspace.BareFetchRefspec, &workspace.BareFetchStatus{
		LastSuccessAt: now.Add(-2 * time.Hour), LastErrorAt: now.Add(-time.Hour),
		LastError: "fatal: refusing to fetch into branch 'refs/heads/x'",
	}), now)
	out, code = report(c)
	if !c.Failing || c.LegacyRefspec {
		t.Fatalf("failing clone: got %+v", c)
	}
	if !strings.Contains(out, "ERROR: the last fetch of bare clone") || !strings.Contains(out, "refusing to fetch") {
		t.Errorf("failing fetch not reported:\n%s", out)
	}
	if code != 1 {
		t.Errorf("a failing fetch fails doctor, exit = %d", code)
	}

	// Healthy.
	c = checkBareFetch(ctx, bareFetchFixture(t, workspace.BareFetchRefspec,
		&workspace.BareFetchStatus{LastSuccessAt: now.Add(-time.Minute)}), now)
	out, code = report(c)
	if c.Failing || c.Stale || c.LegacyRefspec || code != 0 || strings.Contains(out, "WARNING") || strings.Contains(out, "ERROR") {
		t.Errorf("healthy clone reported a problem (exit %d):\n%s", code, out)
	}
	if !strings.Contains(out, "last successful fetch 2026-10-21T11:59:00Z (1m0s ago)") {
		t.Errorf("missing last-fetch line:\n%s", out)
	}

	// Not a bare-clone workflow: nothing reported.
	if c := checkBareFetch(ctx, &config.Config{}, now); c.Ran {
		t.Errorf("non-worktree workflow ran the check: %+v", c)
	}
}
