//go:build !(darwin || linux || freebsd || openbsd || netbsd || dragonfly)

package workflow

import "time"

// lockFile is a documented no-op on platforms without flock(2) — notably
// Windows, which is not a release target (.goreleaser.yaml builds linux and
// darwin only). On these platforms WORKFLOW.md edits are serialized within
// one process by editMu but NOT across processes, i.e. the pre-CORE-149
// behaviour: run `itervox init --update` / `itervox models refresh` while the
// daemon is stopped. A LockFileEx implementation can replace this if Windows
// ever becomes a release target.
func lockFile(string, time.Time) (func(), error) {
	return func() {}, nil
}
