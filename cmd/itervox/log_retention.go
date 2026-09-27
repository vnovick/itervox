package main

import (
	"fmt"
	"strconv"
	"strings"
)

// daemonLogRetention is the rotation policy of the daemon log file
// <logs-dir>/itervox.log (CORE-114). Per-issue log files have their own
// size cap and rotation (logbuffer, CORE-036) and are not affected.
type daemonLogRetention struct {
	MaxSizeMB  int // rotate when the file reaches this size
	MaxBackups int // rotated files kept (0 = keep all, subject to MaxAgeDays)
	MaxAgeDays int // delete rotated files older than this (0 = no age limit)
}

// Defaults match the pre-CORE-114 hard-coded policy: 10 MB, 5 backups, no
// age limit. The maxima (M6-close V2) keep lumberjack's byte limit
// (MaxSize MB × 2^20) far from int overflow and are well past any real use.
const (
	defaultLogMaxSizeMB  = 10
	defaultLogMaxBackups = 5
	defaultLogMaxAgeDays = 0

	maxLogMaxSizeMB  = 10240 // 10 GiB per file
	maxLogMaxBackups = 1000
	maxLogMaxAgeDays = 3650 // 10 years
)

// daemonLogRetentionFromEnv reads ITERVOX_LOG_MAX_SIZE_MB (1..10240),
// ITERVOX_LOG_MAX_BACKUPS (0..1000) and ITERVOX_LOG_MAX_AGE_DAYS (0..3650).
// An unset variable keeps its default. A malformed or out-of-range value is
// an error naming the variable and its range: the caller refuses to start
// rather than run with a policy the operator did not ask for (an unbounded
// size used to overflow lumberjack and fail every daemon-log write).
func daemonLogRetentionFromEnv(getenv func(string) string) (daemonLogRetention, error) {
	r := daemonLogRetention{MaxSizeMB: defaultLogMaxSizeMB, MaxBackups: defaultLogMaxBackups, MaxAgeDays: defaultLogMaxAgeDays}
	var errs []string
	read := func(name string, lo, hi int, dst *int) {
		raw := strings.TrimSpace(getenv(name))
		if raw == "" {
			return
		}
		n, err := strconv.Atoi(raw)
		if err != nil || n < lo || n > hi {
			errs = append(errs, fmt.Sprintf("%s=%q must be a whole number from %d to %d", name, raw, lo, hi))
			return
		}
		*dst = n
	}
	read("ITERVOX_LOG_MAX_SIZE_MB", 1, maxLogMaxSizeMB, &r.MaxSizeMB)
	read("ITERVOX_LOG_MAX_BACKUPS", 0, maxLogMaxBackups, &r.MaxBackups)
	read("ITERVOX_LOG_MAX_AGE_DAYS", 0, maxLogMaxAgeDays, &r.MaxAgeDays)
	if len(errs) > 0 {
		return r, fmt.Errorf("invalid log retention setting: %s", strings.Join(errs, "; "))
	}
	return r, nil
}
