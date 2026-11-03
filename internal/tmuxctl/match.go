package tmuxctl

import (
	"path/filepath"
	"slices"
)

// MatchPane returns the pane a CLI process runs in: pid is the pane's own
// process or a descendant of it (ancestors is pid's parent chain, nearest
// first), and the pane's current directory is cwd. Both must hold — a pane
// whose process tree contains pid but whose foreground directory differs
// is not matched, and neither is a pane merely sitting in cwd.
func MatchPane(panes []Pane, pid int, ancestors []int, cwd string) (Pane, bool) {
	if pid <= 0 || cwd == "" {
		return Pane{}, false
	}
	want := canonical(cwd)
	for _, p := range panes {
		if HostsProcess(p, pid, ancestors) && canonical(p.Cwd) == want {
			return p, true
		}
	}
	return Pane{}, false
}

// HostsProcess reports whether pid is the pane's own process or one of its
// descendants (ancestors is pid's parent chain, nearest first).
func HostsProcess(p Pane, pid int, ancestors []int) bool {
	if p.PID <= 0 || pid <= 0 {
		return false
	}
	return p.PID == pid || slices.Contains(ancestors, p.PID)
}

// canonical cleans p and resolves its symlinks when it can.
func canonical(p string) string {
	if p == "" {
		return ""
	}
	if real, err := filepath.EvalSymlinks(p); err == nil {
		return filepath.Clean(real)
	}
	return filepath.Clean(p)
}
