package statusui

import (
	"errors"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type nopCloser struct{}

func (nopCloser) Close() error { return nil }

func TestCheckForegroundTTYOwnership_AllowsForegroundOwner(t *testing.T) {
	orig := foregroundTTYProcessGroups
	t.Cleanup(func() { foregroundTTYProcessGroups = orig })

	foregroundTTYProcessGroups = func() (int, int, error) {
		return 44123, 44123, nil
	}

	require.NoError(t, checkForegroundTTYOwnership())
}

func TestCheckForegroundTTYOwnership_ReturnsClearErrorWhenBackgrounded(t *testing.T) {
	orig := foregroundTTYProcessGroups
	origExists := foregroundTTYProcessGroupExists
	t.Cleanup(func() {
		foregroundTTYProcessGroups = orig
		foregroundTTYProcessGroupExists = origExists
	})

	foregroundTTYProcessGroups = func() (int, int, error) {
		return 44123, 44216, nil
	}
	foregroundTTYProcessGroupExists = func(int) (bool, error) { return true, nil }

	err := checkForegroundTTYOwnership()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "foreground tty")
	assert.Contains(t, err.Error(), "process group 44216")
	assert.Contains(t, err.Error(), "resume with `fg`")
}

func TestCheckForegroundTTYOwnership_ReclaimsStaleForegroundProcessGroup(t *testing.T) {
	orig := foregroundTTYProcessGroups
	origExists := foregroundTTYProcessGroupExists
	origSet := setForegroundTTYProcessGroup
	t.Cleanup(func() {
		foregroundTTYProcessGroups = orig
		foregroundTTYProcessGroupExists = origExists
		setForegroundTTYProcessGroup = origSet
	})

	foregroundTTYProcessGroups = func() (int, int, error) {
		return 44123, 44216, nil
	}
	foregroundTTYProcessGroupExists = func(int) (bool, error) { return false, nil }

	reclaimed := 0
	setForegroundTTYProcessGroup = func(pgid int) error {
		reclaimed = pgid
		return nil
	}

	require.NoError(t, checkForegroundTTYOwnership())
	assert.Equal(t, 44216, reclaimed)
}

func TestCheckForegroundTTYOwnership_PropagatesReclaimErrors(t *testing.T) {
	orig := foregroundTTYProcessGroups
	origExists := foregroundTTYProcessGroupExists
	origSet := setForegroundTTYProcessGroup
	t.Cleanup(func() {
		foregroundTTYProcessGroups = orig
		foregroundTTYProcessGroupExists = origExists
		setForegroundTTYProcessGroup = origSet
	})

	foregroundTTYProcessGroups = func() (int, int, error) {
		return 44123, 44216, nil
	}
	foregroundTTYProcessGroupExists = func(int) (bool, error) { return false, nil }
	setForegroundTTYProcessGroup = func(int) error {
		return errors.New("reclaim failed")
	}

	err := checkForegroundTTYOwnership()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reclaim failed")
}

func TestCheckForegroundTTYOwnership_PropagatesLookupErrors(t *testing.T) {
	orig := foregroundTTYProcessGroups
	t.Cleanup(func() { foregroundTTYProcessGroups = orig })

	foregroundTTYProcessGroups = func() (int, int, error) {
		return 0, 0, errors.New("boom")
	}

	err := checkForegroundTTYOwnership()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "boom")
}

func TestForegroundTTYFD_UsesStdinWhenItIsATerminal(t *testing.T) {
	origIsTTY := stdinIsTerminal
	origOpenTTY := openControllingTTY
	t.Cleanup(func() {
		stdinIsTerminal = origIsTTY
		openControllingTTY = origOpenTTY
	})

	stdinIsTerminal = func() bool { return true }
	openCalled := false
	openControllingTTY = func() (int, io.Closer, error) {
		openCalled = true
		return 99, nopCloser{}, nil
	}

	fd, closer, err := foregroundTTYFD()
	require.NoError(t, err)
	assert.Equal(t, 0, fd)
	assert.Nil(t, closer)
	assert.False(t, openCalled)
}

func TestForegroundTTYFD_FallsBackToControllingTTYWhenStdinIsNotATerminal(t *testing.T) {
	origIsTTY := stdinIsTerminal
	origOpenTTY := openControllingTTY
	t.Cleanup(func() {
		stdinIsTerminal = origIsTTY
		openControllingTTY = origOpenTTY
	})

	stdinIsTerminal = func() bool { return false }
	openControllingTTY = func() (int, io.Closer, error) {
		return 99, nopCloser{}, nil
	}

	fd, closer, err := foregroundTTYFD()
	require.NoError(t, err)
	assert.Equal(t, 99, fd)
	require.NotNil(t, closer)
}

func TestCheckForegroundTTYOwnershipWithRetry_AllowsTransientInteractiveMismatch(t *testing.T) {
	origCheck := foregroundTTYProcessGroups
	origExists := foregroundTTYProcessGroupExists
	origIsTTY := stdinIsTerminal
	origAttempts := foregroundTTYRetryAttempts
	origDelay := foregroundTTYRetryDelay
	origSleep := sleepForegroundTTYRetry
	t.Cleanup(func() {
		foregroundTTYProcessGroups = origCheck
		foregroundTTYProcessGroupExists = origExists
		stdinIsTerminal = origIsTTY
		foregroundTTYRetryAttempts = origAttempts
		foregroundTTYRetryDelay = origDelay
		sleepForegroundTTYRetry = origSleep
	})

	stdinIsTerminal = func() bool { return true }
	foregroundTTYProcessGroupExists = func(int) (bool, error) { return true, nil }
	foregroundTTYRetryAttempts = 3
	foregroundTTYRetryDelay = time.Millisecond
	sleepForegroundTTYRetry = func(time.Duration) {}

	calls := 0
	foregroundTTYProcessGroups = func() (int, int, error) {
		calls++
		if calls == 1 {
			return 44123, 44216, nil
		}
		return 44216, 44216, nil
	}

	require.NoError(t, checkForegroundTTYOwnershipWithRetry())
	assert.Equal(t, 2, calls)
}

func TestCheckForegroundTTYOwnershipWithRetry_FailsAfterInteractiveRetriesExhausted(t *testing.T) {
	origCheck := foregroundTTYProcessGroups
	origExists := foregroundTTYProcessGroupExists
	origIsTTY := stdinIsTerminal
	origAttempts := foregroundTTYRetryAttempts
	origDelay := foregroundTTYRetryDelay
	origSleep := sleepForegroundTTYRetry
	t.Cleanup(func() {
		foregroundTTYProcessGroups = origCheck
		foregroundTTYProcessGroupExists = origExists
		stdinIsTerminal = origIsTTY
		foregroundTTYRetryAttempts = origAttempts
		foregroundTTYRetryDelay = origDelay
		sleepForegroundTTYRetry = origSleep
	})

	stdinIsTerminal = func() bool { return true }
	foregroundTTYProcessGroupExists = func(int) (bool, error) { return true, nil }
	foregroundTTYRetryAttempts = 3
	foregroundTTYRetryDelay = time.Millisecond
	sleepForegroundTTYRetry = func(time.Duration) {}

	calls := 0
	foregroundTTYProcessGroups = func() (int, int, error) {
		calls++
		return 44123, 44216, nil
	}

	err := checkForegroundTTYOwnershipWithRetry()
	require.Error(t, err)
	assert.Equal(t, 3, calls)
	assert.Contains(t, err.Error(), "process group 44216")
}

func TestCheckForegroundTTYOwnershipWithRetry_DoesNotRetryWhenStdinIsNotTerminal(t *testing.T) {
	origCheck := foregroundTTYProcessGroups
	origExists := foregroundTTYProcessGroupExists
	origIsTTY := stdinIsTerminal
	origAttempts := foregroundTTYRetryAttempts
	origDelay := foregroundTTYRetryDelay
	origSleep := sleepForegroundTTYRetry
	t.Cleanup(func() {
		foregroundTTYProcessGroups = origCheck
		foregroundTTYProcessGroupExists = origExists
		stdinIsTerminal = origIsTTY
		foregroundTTYRetryAttempts = origAttempts
		foregroundTTYRetryDelay = origDelay
		sleepForegroundTTYRetry = origSleep
	})

	stdinIsTerminal = func() bool { return false }
	foregroundTTYProcessGroupExists = func(int) (bool, error) { return true, nil }
	foregroundTTYRetryAttempts = 5
	foregroundTTYRetryDelay = time.Millisecond
	sleepForegroundTTYRetry = func(time.Duration) {}

	calls := 0
	foregroundTTYProcessGroups = func() (int, int, error) {
		calls++
		return 44123, 44216, nil
	}

	err := checkForegroundTTYOwnershipWithRetry()
	require.Error(t, err)
	assert.Equal(t, 1, calls)
}
