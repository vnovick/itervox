//go:build !(darwin || linux || freebsd || openbsd || netbsd || dragonfly || windows)

package hub

import "errors"

// lockSupported reports whether the hub election works on this platform.
// Without flock(2) or LockFileEx no daemon becomes the hub.
const lockSupported = false

// Lock is the hub lock; never acquired on this platform.
type Lock struct{}

var errNoLock = errors.New("hub: the hub needs flock(2), which this platform lacks")

func tryLock(string) (*Lock, bool, error) { return nil, false, errNoLock }

func lockHeld(string) (bool, error) { return false, errNoLock }

func (l *Lock) publish([]byte) error { return errNoLock }

// Release is a no-op on this platform.
func (l *Lock) Release() {}
