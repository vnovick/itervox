package main

import (
	"context"
	"runtime"
	"sort"
	"sync"
	"time"

	"github.com/vnovick/itervox/internal/extsessions"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/tmuxctl"
)

// Discovery of the Sessions panel (#178), run off the event loop by the
// orchestrator every 15 s: hand-started sessions (extsessions, when
// sessions.external is on), tmux availability, and the tmux pane each
// session runs in.

// tmuxStatusTTL is how long a tmux availability answer is reused by reads
// that do not re-detect (GET /api/v1/sessions between discovery passes).
const tmuxStatusTTL = 15 * time.Second

// tmuxStatus is one detection of tmux.
type tmuxStatus struct {
	Available bool
	Version   string
}

// tmuxDetector caches whether tmux is installed. Once tmux is found its
// `tmux -V` answer is kept for the daemon's lifetime (#210): it was a
// fork/exec on every 15 s discovery pass and every write, for a version
// that does not change under a running daemon. Until tmux is found, every
// pass and write re-detects — a PATH lookup that starts no process while
// tmux is missing — so installing tmux lifts the warning without a restart.
type tmuxDetector struct {
	client tmuxctl.Client
	// probeMu serializes detections, so concurrent callers share one probe.
	probeMu sync.Mutex
	mu      sync.Mutex
	st      tmuxStatus
	at      time.Time
}

// refresh returns the lifetime answer once tmux was found, else detects
// tmux now.
func (d *tmuxDetector) refresh() tmuxStatus {
	if st, ok := d.found(); ok {
		return st
	}
	d.probeMu.Lock()
	defer d.probeMu.Unlock()
	if st, ok := d.found(); ok { // another caller's probe found it meanwhile
		return st
	}
	ctx, cancel := context.WithTimeout(context.Background(), tmuxctl.DefaultTimeout)
	defer cancel()
	v, err := d.client.Version(ctx)
	st := tmuxStatus{Available: err == nil, Version: v}
	d.mu.Lock()
	d.st, d.at = st, time.Now()
	d.mu.Unlock()
	return st
}

// found returns the cached detection when it found tmux.
func (d *tmuxDetector) found() (tmuxStatus, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.st, d.st.Available
}

// current returns the last detection, re-detecting a "not installed"
// answer once it is stale.
func (d *tmuxDetector) current() tmuxStatus {
	d.mu.Lock()
	st, at := d.st, d.at
	d.mu.Unlock()
	if !st.Available && (at.IsZero() || time.Since(at) > tmuxStatusTTL) {
		return d.refresh()
	}
	return st
}

// tmuxInstallHint says how to install tmux on the daemon's platform.
func tmuxInstallHint() string {
	if runtime.GOOS == "darwin" {
		return "brew install tmux"
	}
	return "sudo apt install tmux (or your distribution's package manager)"
}

// sessionSource is the orchestrator's ExternalSessionSource.
type sessionSource struct {
	// scanner discovers hand-started sessions; nil when sessions.external
	// is off (tmux is still detected and listed for itervox's own).
	scanner *extsessions.Scanner
	detect  *tmuxDetector
}

// Discover runs one pass. Reads only: transcript files, the process list,
// `tmux -V` (until tmux is found) and `tmux list-panes` — skipped when no
// tmux server socket exists, since there are no panes to list (#210).
func (s sessionSource) Discover(now time.Time) (orchestrator.ExternalScan, error) {
	scan := orchestrator.ExternalScan{ScannedAt: time.Now()}
	var panes []tmuxctl.Pane
	switch {
	case !s.detect.refresh().Available:
	case !s.detect.client.ServerMayRun():
		// No server: the listing is known to be empty, as list-panes
		// reports it (TmuxListed lets a vanished session end).
		scan.TmuxListed = true
	default:
		ctx, cancel := context.WithTimeout(context.Background(), tmuxctl.DefaultTimeout)
		list, err := s.detect.client.ListPanes(ctx)
		cancel()
		if err == nil {
			panes, scan.TmuxListed = list, true
			for _, p := range panes {
				scan.TmuxPanes = append(scan.TmuxPanes, paneRef(p))
			}
		}
	}
	if s.scanner == nil {
		return scan, nil
	}
	sessions, procs, err := s.scanner.Scan(now)
	matchSessionPanes(sessions, procs, panes)
	scan.Sessions = sessions
	return scan, err
}

func paneRef(p tmuxctl.Pane) extsessions.TmuxPane {
	return extsessions.TmuxPane{ID: p.ID, Label: p.Label(), Session: p.Session}
}

// matchSessionPanes attaches to each session the tmux pane its CLI process
// runs in: the process is the pane's own or a descendant of it, and the
// pane's directory is the session's. Claude Code does not keep its
// transcript open, so several sessions of one directory can point at the
// same process; a pane then goes to the most recently written of them
// only, the one that CLI is most likely writing now.
func matchSessionPanes(sessions []extsessions.Session, procs []extsessions.Process, panes []tmuxctl.Pane) {
	if len(panes) == 0 || len(procs) == 0 {
		return
	}
	owner := map[string]int{} // pane id -> session index
	for i := range sessions {
		s := &sessions[i]
		if s.PID <= 0 {
			continue
		}
		pane, ok := tmuxctl.MatchPane(panes, s.PID, extsessions.Ancestors(procs, s.PID), s.Cwd)
		if !ok {
			continue
		}
		if j, taken := owner[pane.ID]; taken && !s.UpdatedAt.After(sessions[j].UpdatedAt) {
			continue
		} else if taken {
			sessions[j].TmuxPane = extsessions.TmuxPane{}
		}
		owner[pane.ID] = i
		s.TmuxPane = paneRef(pane)
	}
}

// sessionPane returns the first pane (lowest window, then pane index) of
// tmux session name.
func sessionPane(panes []tmuxctl.Pane, name string) (tmuxctl.Pane, bool) {
	var mine []tmuxctl.Pane
	for _, p := range panes {
		if p.Session == name {
			mine = append(mine, p)
		}
	}
	if len(mine) == 0 {
		return tmuxctl.Pane{}, false
	}
	sort.Slice(mine, func(i, j int) bool {
		if mine[i].Window != mine[j].Window {
			return mine[i].Window < mine[j].Window
		}
		return mine[i].Index < mine[j].Index
	})
	return mine[0], true
}
