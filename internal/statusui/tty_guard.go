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
