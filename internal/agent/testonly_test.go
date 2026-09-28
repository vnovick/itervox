package agent

import (
	"time"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
)

// ParseResetTime extracts a limit reset time from Claude or Codex limit text.
// Times printed without a date resolve to the next occurrence after now;
// dates without a year resolve to the next occurrence too. A Claude "(Zone)"
// suffix is honoured when it names a loadable IANA zone; otherwise, and for
// Codex (which prints local time with no zone), loc is used — the agent
// host's time zone (nil means time.Local).
func ParseResetTime(text string, now time.Time, loc *time.Location) (time.Time, bool) {
	t, ok, _ := parseResetTimeZone(text, now, loc)
	return t, ok
}

// streamLineToEntry converts one stream-json line to an IssueLogEntry.
// sessionID is stamped on every returned entry.
// Returns (entry, false) when the line should be skipped.
func streamLineToEntry(line []byte, sessionID string) (domain.IssueLogEntry, bool) {
	entries := streamLineToEntriesWith(line, ParseLine, sessionID)
	if len(entries) == 0 {
		return domain.IssueLogEntry{}, false
	}
	return entries[0], true
}

func firstCommandToken(command string) string {
	return config.FirstCommandToken(command)
}
