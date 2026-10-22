//go:build !(darwin || linux || freebsd || openbsd || netbsd || dragonfly)

package apitokens

import "time"

// lockFile is a documented no-op on platforms without flock(2) (Windows is
// not a release target). Mutations are still serialized within one process
// by Store.writeMu; run `itervox token` while the daemon is stopped there.
func lockFile(string, time.Time) (func(), error) {
	return func() {}, nil
}
