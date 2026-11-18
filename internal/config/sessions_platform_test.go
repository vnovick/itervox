package config

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// sessions.terminal_write on Windows (no tmux, no live terminal) is
// reported at config load instead of silently ignored; elsewhere, or with
// the setting off, nothing is said.
func TestTerminalWriteOnWindowsIsReportedAtLoad(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(prev)
	oldGOOS := sessionsGOOS
	defer func() { sessionsGOOS = oldGOOS }()

	for _, tc := range []struct {
		goos  string
		write bool
		warn  bool
	}{
		{"windows", true, true},
		{"windows", false, false},
		{"linux", true, false},
		{"darwin", true, false},
	} {
		buf.Reset()
		sessionsGOOS = tc.goos
		c := parseSessionsConfig(map[string]any{"terminal_write": tc.write})
		assert.Equal(t, tc.write, c.TerminalWrite, "the value is kept as written (%s)", tc.goos)
		logged := strings.Contains(buf.String(), "sessions.terminal_write has no effect on Windows")
		assert.Equal(t, tc.warn, logged, "%s terminal_write=%v: %s", tc.goos, tc.write, buf.String())
		if tc.warn {
			assert.Contains(t, buf.String(), "level=WARN")
		}
		assert.Equal(t, tc.warn, TerminalWriteIgnored(c, tc.goos) != "")
	}
}
