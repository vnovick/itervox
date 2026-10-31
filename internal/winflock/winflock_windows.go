//go:build windows

package winflock

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// ErrLocked is returned by TryLock when another handle holds a conflicting
// lock (ERROR_LOCK_VIOLATION).
var ErrLocked = errors.New("winflock: locked by another handle")

// The locked byte: offset 2^63-2, far beyond any real file's end. Locking
// beyond EOF is allowed and does not block reads or writes of the content.
const (
	lockOffsetLow  = 0xFFFFFFFE
	lockOffsetHigh = 0x7FFFFFFF
)

// TryLock takes a non-blocking lock on f: exclusive when exclusive is true,
// else shared. It returns ErrLocked when the lock is held elsewhere.
func TryLock(f *os.File, exclusive bool) error {
	flags := uint32(windows.LOCKFILE_FAIL_IMMEDIATELY)
	if exclusive {
		flags |= windows.LOCKFILE_EXCLUSIVE_LOCK
	}
	ol := windows.Overlapped{Offset: lockOffsetLow, OffsetHigh: lockOffsetHigh}
	err := windows.LockFileEx(windows.Handle(f.Fd()), flags, 0, 1, 0, &ol)
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, windows.ERROR_IO_PENDING) {
		return ErrLocked
	}
	return err
}

// Unlock releases the lock TryLock took on f. Closing f releases it too.
func Unlock(f *os.File) error {
	ol := windows.Overlapped{Offset: lockOffsetLow, OffsetHigh: lockOffsetHigh}
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &ol)
}
