//go:build windows

package atomicfs

import (
	"errors"
	"os"
	"syscall"
	"time"
)

// renameRetryFor bounds how long renameReplace waits for another handle to
// let go of newpath.
const renameRetryFor = 2 * time.Second

// Windows errors a replacing rename gets while another handle has the
// target open without FILE_SHARE_DELETE (os.Open opens files that way).
const (
	errAccessDenied     = syscall.Errno(5)
	errSharingViolation = syscall.Errno(32)
)

// renameReplace renames oldpath over newpath (MoveFileEx with
// MOVEFILE_REPLACE_EXISTING, atomic on NTFS). Unlike rename(2) it fails
// while any other handle has newpath open, so it retries, with back-off,
// until renameRetryFor has passed: readers hold these files only briefly.
func renameReplace(oldpath, newpath string) error {
	deadline := time.Now().Add(renameRetryFor)
	wait := time.Millisecond
	for {
		err := os.Rename(oldpath, newpath)
		busy := errors.Is(err, errAccessDenied) || errors.Is(err, errSharingViolation)
		if !busy || time.Now().After(deadline) {
			return err
		}
		time.Sleep(wait)
		wait = min(2*wait, 50*time.Millisecond)
	}
}
