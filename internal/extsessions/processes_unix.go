//go:build unix

package extsessions

import (
	"os"
	"syscall"
)

// sameUID reports whether st (a /proc/<pid> directory) belongs to uid.
// Unknown ownership counts as the same user (it is inspected, not skipped).
func sameUID(st os.FileInfo, uid int) bool {
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok {
		return true
	}
	return int(sys.Uid) == uid
}

// processInspectionUnavailable is nil on Unix: /proc or ps + lsof list the
// agent processes.
func processInspectionUnavailable() error { return nil }
