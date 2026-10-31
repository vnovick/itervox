//go:build !(darwin || linux || freebsd || openbsd || netbsd || dragonfly || windows)

package main

// tryLockPIDFile is a documented always-acquired no-op on platforms without
// flock(2) or LockFileEx (pidlock_flock.go and pidlock_windows.go cover every
// release target). There the pid guard falls back to the
// pre-CORE-039 pid probe for every record, so a same-pid restart is refused
// exactly as before.
func tryLockPIDFile(string) (release func(), acquired bool, err error) {
	return func() {}, true, nil
}

// pidLockSupported reports whether tryLockPIDFile is a real lock.
const pidLockSupported = false
