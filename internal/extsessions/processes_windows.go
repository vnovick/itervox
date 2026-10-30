//go:build windows

package extsessions

import (
	"errors"
	"os"
)

// ErrProcessInspectionUnavailable is what SystemProcesses returns on
// Windows: it reads /proc or runs ps and lsof, none of which exist there.
// Liveness checks treat it like any other listing failure (the session's
// liveness stays unknown).
var ErrProcessInspectionUnavailable = errors.New("extsessions: process inspection is not available on Windows")

// sameUID has no Windows meaning (/proc does not exist there); every entry
// counts as this user's.
func sameUID(os.FileInfo, int) bool { return true }

func processInspectionUnavailable() error { return ErrProcessInspectionUnavailable }
