package statusui

import (
	"fmt"
	"time"
)

var foregroundTTYProcessGroups = currentForegroundTTYProcessGroups
var foregroundTTYProcessGroupExists = currentForegroundTTYProcessGroupExists
var setForegroundTTYProcessGroup = currentSetForegroundTTYProcessGroup
var foregroundTTYRetryAttempts = 20
var foregroundTTYRetryDelay = 25 * time.Millisecond
var sleepForegroundTTYRetry = time.Sleep

func checkForegroundTTYOwnership() error {
	foreground, current, err := foregroundTTYProcessGroups()
	if err != nil {
		return err
	}
	if foreground == 0 || current == 0 || foreground == current {
		return nil
	}
	exists, err := foregroundTTYProcessGroupExists(foreground)
	if err != nil {
		return err
	}
	if !exists {
		if err := setForegroundTTYProcessGroup(current); err != nil {
			return err
		}
		return nil
	}
	return fmt.Errorf(
		"statusui: process group %d does not own the foreground tty (foreground=%d); resume with `fg` or close stale itervox jobs",
		current,
		foreground,
	)
}

// TerminalAvailable reports whether Run would actually start the TUI: true
// when this process currently owns (or can silently reclaim) the foreground
// TTY, false when there is no controlling terminal at all — headless
// (systemd, container, CI, detached stdio) — or foreground ownership cannot
// be resolved.
//
// This is a single side-effect-free check: no retry loop, no sleep, no log
// line. It exists so callers (main.go's pre-TUI slog redirect) can decide
// ahead of time whether the TUI is about to take the alt-screen, without
// paying for or triggering the interactive retry/backoff that
// checkForegroundTTYOwnershipWithRetry performs when stdin is a terminal.
//
// It calls the same checkForegroundTTYOwnership core that
// checkForegroundTTYOwnershipWithRetry uses (that function's first
// iteration, before any retry), so this probe and Run's internal check
// cannot drift on what counts as "has a TTY".
func TerminalAvailable() bool {
	return checkForegroundTTYOwnership() == nil
}

func checkForegroundTTYOwnershipWithRetry() error {
	attempts := 1
	if stdinIsTerminal() {
		attempts = foregroundTTYRetryAttempts
	}

	var err error
	for i := 0; i < attempts; i++ {
		err = checkForegroundTTYOwnership()
		if err == nil {
			return nil
		}
		if i < attempts-1 {
			sleepForegroundTTYRetry(foregroundTTYRetryDelay)
		}
	}

	return err
}
