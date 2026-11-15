//go:build !(darwin || linux || freebsd || openbsd || netbsd || dragonfly || windows)

package workflow

import "time"

// lockFile is a documented no-op on platforms without flock(2) or LockFileEx
// (filelock_flock.go and filelock_windows.go cover every release target). On
// these platforms WORKFLOW.md edits are serialized within
// one process by editMu but NOT across processes, i.e. the pre-CORE-149
// behaviour: run `itervox init --update` / `itervox models refresh` while the
// daemon is stopped.
func lockFile(string, time.Time) (func(), error) {
	return func() {}, nil
}
