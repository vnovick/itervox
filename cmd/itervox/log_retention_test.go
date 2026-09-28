package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDaemonLogRetentionFromEnv (CORE-114, M6-close V2): defaults match the
// old policy; valid values apply; a malformed or out-of-range value fails
// with an error naming the variable and its range — an unbounded
// ITERVOX_LOG_MAX_SIZE_MB overflowed lumberjack's byte limit and made every
// daemon-log write fail.
func TestDaemonLogRetentionFromEnv(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

	d, err := daemonLogRetentionFromEnv(env(nil))
	require.NoError(t, err)
	assert.Equal(t, daemonLogRetention{MaxSizeMB: 10, MaxBackups: 5, MaxAgeDays: 0}, d)

	r, err := daemonLogRetentionFromEnv(env(map[string]string{
		"ITERVOX_LOG_MAX_SIZE_MB": "50", "ITERVOX_LOG_MAX_BACKUPS": "0", "ITERVOX_LOG_MAX_AGE_DAYS": " 14 ",
	}))
	require.NoError(t, err)
	assert.Equal(t, daemonLogRetention{MaxSizeMB: 50, MaxBackups: 0, MaxAgeDays: 14}, r)

	edge, err := daemonLogRetentionFromEnv(env(map[string]string{
		"ITERVOX_LOG_MAX_SIZE_MB": "10240", "ITERVOX_LOG_MAX_BACKUPS": "1000", "ITERVOX_LOG_MAX_AGE_DAYS": "3650",
	}))
	require.NoError(t, err, "the maxima themselves are accepted")
	assert.Equal(t, 10240, edge.MaxSizeMB)

	for name, val := range map[string]string{
		"ITERVOX_LOG_MAX_SIZE_MB":  "9000000000000", // the verifier's overflow probe
		"ITERVOX_LOG_MAX_BACKUPS":  "-1",
		"ITERVOX_LOG_MAX_AGE_DAYS": "a week",
	} {
		_, err := daemonLogRetentionFromEnv(env(map[string]string{name: val}))
		require.Error(t, err, "%s=%s", name, val)
		assert.Contains(t, err.Error(), name)
	}
	_, err = daemonLogRetentionFromEnv(env(map[string]string{"ITERVOX_LOG_MAX_SIZE_MB": "0"}))
	require.Error(t, err)
	_, err = daemonLogRetentionFromEnv(env(map[string]string{"ITERVOX_LOG_MAX_SIZE_MB": "10241"}))
	require.Error(t, err)
}
