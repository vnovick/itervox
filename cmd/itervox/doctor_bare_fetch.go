package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/workspace"
)

// bareFetchStaleAfter is how old the last successful fetch of the shared
// bare clone may be before doctor warns that new worktrees branch from a
// stale base. An idle daemon does not fetch (fetches run when a worktree is
// created), so this is a warning, not a failure.
const bareFetchStaleAfter = 24 * time.Hour

// BareFetchCheck is doctor's view of the shared bare clone's fetch (#195):
// a fetch that fails, or a clone still on the pre-#195 refspec, leaves every
// base branch where it was and new worktrees start from old code.
type BareFetchCheck struct {
	// Ran is false when the workflow does not use a shared bare clone
	// (workspace.worktree with a clone_url).
	Ran  bool
	Path string
	// Missing is true when the clone does not exist yet (first dispatch
	// creates it).
	Missing       bool
	LegacyRefspec bool
	// Status is the last recorded fetch; StatusKnown is false when no fetch
	// has been recorded (a clone not used since the upgrade).
	Status      workspace.BareFetchStatus
	StatusKnown bool
	// Failing: the most recent fetch failed. Doctor exits 1.
	Failing bool
	// Stale: the last success is older than bareFetchStaleAfter. Warning.
	Stale bool
	Age   time.Duration
}

// checkBareFetch inspects the shared bare clone the workflow uses.
func checkBareFetch(ctx context.Context, cfg *config.Config, now time.Time) BareFetchCheck {
	var c BareFetchCheck
	if cfg == nil || !cfg.Workspace.Worktree || cfg.Workspace.CloneURL == "" || cfg.Workspace.Root == "" {
		return c
	}
	c.Ran = true
	c.Path = workspace.BarePath(cfg.Workspace.Root)
	if _, err := os.Stat(c.Path); err != nil {
		c.Missing = true
		return c
	}
	c.LegacyRefspec = workspace.BareRefspecNeedsMigration(ctx, c.Path)
	s, err := workspace.ReadBareFetchStatus(c.Path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			c.Status.LastError = err.Error()
		}
		return c
	}
	c.Status, c.StatusKnown = s, true
	c.Failing = s.Failing()
	if !s.LastSuccessAt.IsZero() {
		c.Age = max(now.Sub(s.LastSuccessAt), 0).Round(time.Second)
		c.Stale = !c.Failing && c.Age > bareFetchStaleAfter
	}
	return c
}

// renderBareFetchCheck writes the bare-clone fetch lines.
func renderBareFetchCheck(b *strings.Builder, c BareFetchCheck) {
	if !c.Ran {
		return
	}
	if c.Missing {
		fmt.Fprintf(b, "bare clone: %s (not cloned yet — created on the first dispatch)\n", c.Path)
		return
	}
	last := "unknown (no fetch recorded since the upgrade)"
	if c.StatusKnown {
		last = "never"
		if !c.Status.LastSuccessAt.IsZero() {
			last = fmt.Sprintf("%s (%s ago)", c.Status.LastSuccessAt.UTC().Format(time.RFC3339), c.Age)
		}
	}
	fmt.Fprintf(b, "bare clone: %s · last successful fetch %s\n", c.Path, last)
	if c.LegacyRefspec {
		fmt.Fprintf(b, "WARNING: bare clone %s still fetches with %s: every fetch fails while a worktree has an origin branch checked out, so base branches stop moving and new worktrees start from old code. The daemon migrates it to %s on the next worktree it creates.\n",
			c.Path, workspace.LegacyBareFetchRefspec, workspace.BareFetchRefspec)
	}
	if c.Failing {
		fmt.Fprintf(b, "ERROR: the last fetch of bare clone %s failed at %s: %s — new worktrees branch from a stale base until a fetch succeeds\n",
			c.Path, c.Status.LastErrorAt.UTC().Format(time.RFC3339), or(c.Status.LastError, "unknown error"))
	}
	if c.Stale {
		fmt.Fprintf(b, "WARNING: bare clone %s was last fetched %s ago (past %s) — new worktrees branch from the base as it was then; the next worktree the daemon creates fetches again\n",
			c.Path, c.Age, bareFetchStaleAfter)
	}
	if !c.StatusKnown && c.Status.LastError != "" {
		fmt.Fprintf(b, "WARNING: bare clone fetch status unreadable: %s\n", c.Status.LastError)
	}
}
