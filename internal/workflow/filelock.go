package workflow

import (
	"errors"
	"fmt"
	"path/filepath"
	"time"
)

// lockFilePath returns the sidecar lock file for a WORKFLOW.md path:
// ".<base>.lock" next to it, e.g. ".WORKFLOW.md.lock". It is transient
// runtime state — `itervox init` adds it to the root .gitignore.
func lockFilePath(path string) string {
	return filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".lock")
}

// LockFilePath exposes lockFilePath for `itervox init`'s .gitignore patcher.
func LockFilePath(path string) string { return lockFilePath(path) }

// editLockTimeout bounds how long lockForPath waits for the WORKFLOW.md edit
// lock — the in-process mutex and the sidecar flock together (BH4). Every
// legitimate holder (a settings patcher, `itervox models refresh`'s write,
// `itervox init --update`'s migration) holds it for a local read-modify-write
// of one small file: milliseconds, well under a second even on a slow disk.
// Ten seconds is therefore only ever reached by a holder that is not coming
// back soon — a suspended (Ctrl-Z) `itervox init --update`, or another local
// user holding LOCK_EX on the sidecar — and it is short enough that a
// dashboard save fails with a readable error instead of hanging the request
// and, through the in-process mutex, every later save.
//
// A var so tests can shrink it.
var editLockTimeout = 10 * time.Second

// ErrEditLockBusy is returned (wrapped) when the WORKFLOW.md edit lock could
// not be acquired within editLockTimeout. Nothing was written.
var ErrEditLockBusy = errors.New("workflow lock: WORKFLOW.md is locked by another edit")

// editLockBusyError is the one error text for a timed-out edit lock, naming
// the usual culprits so the operator seeing it in the dashboard or TUI knows
// what to look for.
func editLockBusyError(lockPath, holder string) error {
	return fmt.Errorf("%w: %s held it for over %s (%s) — is an `itervox init --update` or "+
		"`itervox models refresh` running or suspended? Nothing was saved; retry once it finishes",
		ErrEditLockBusy, holder, editLockTimeout, lockPath)
}
