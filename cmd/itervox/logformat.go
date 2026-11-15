package main

import (
	"io"
	"log/slog"
	"strings"
	"time"

	charmlog "github.com/charmbracelet/log"

	"github.com/vnovick/itervox/internal/logging"
)

// defaultLogFormat is used whenever neither --log-format nor
// ITERVOX_LOG_FORMAT resolve to a recognized value.
const defaultLogFormat = "text"

// resolveLogFormat picks the effective file-sink log format from the
// --log-format flag and the ITERVOX_LOG_FORMAT env var. flagVal wins when
// both are set (i.e. non-empty); an empty flagVal falls through to envVal.
// Any value other than "json" or "text" — including both being empty —
// resolves to defaultLogFormat ("text"). This function is pure so format
// selection can be tested without touching flag.Parse or os.Getenv; callers
// are responsible for warning when the requested (pre-resolution) value was
// invalid.
func resolveLogFormat(flagVal, envVal string) string {
	v := flagVal
	if v == "" {
		v = envVal
	}
	switch v {
	case "json", "text":
		return v
	default:
		return defaultLogFormat
	}
}

// newFileLogHandler constructs the slog.Handler used for the rotating file
// sink. format == "json" selects slog.NewJSONHandler; anything else
// (including the empty string) falls back to slog.NewTextHandler (logfmt),
// mirroring resolveLogFormat's default. Callers MUST wrap the returned
// handler in logging.NewRedactingHandler before wiring it into slog —
// this function does not redact.
func newFileLogHandler(w io.Writer, level slog.Level, format string) slog.Handler {
	opts := &slog.HandlerOptions{Level: level}
	if format == "json" {
		return slog.NewJSONHandler(w, opts)
	}
	return slog.NewTextHandler(w, opts)
}

// postStartupHandler builds the slog handler run() installs once the
// daemon has started: stderr (stderrHandler, main's stderrOnly leg, so it is
// configured exactly like the one-time dashboard-URL line) fanned out with
// the file sink. The terminal UI that used to send logs to the file only
// is gone (#240), so stderr keeps every record (journalctl et al.).
//
// Wrapped in logging.NewRedactingHandler: neither the file sink nor stderr
// ever receives raw secrets.
func postStartupHandler(fileWriter io.Writer, logLevel slog.Level, logFormat string, stderrHandler slog.Handler) slog.Handler {
	fileHandler := newFileLogHandler(fileWriter, logLevel, logFormat)
	return logging.NewRedactingHandler(logging.NewFanoutHandler(stderrHandler, fileHandler))
}

// bootLogHandlers builds main's startup logging (CORE-059):
//
//   - boot is the slog default until run() takes over: stderr + the file
//     sink, behind logging.NewRedactingHandler.
//   - stderrOnly is the bare stderr leg. main wraps it in the stderr-only
//     logger (the one explicit secret-display path, CORE-012) and
//     postStartupHandler reuses it.
//
// stderr honours format exactly like the file sink — "json" gives one JSON
// object per slog record, which is what container platforms and journald
// ingest, on a terminal too. Output written with fmt.Fprint* before or
// outside slog (usage, fatal startup errors) is never JSON.
func newStderrLogHandler(w io.Writer, level slog.Level, format string) slog.Handler {
	if format == "json" {
		return slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level})
	}
	charmLevel := charmlog.InfoLevel
	if level == slog.LevelDebug {
		charmLevel = charmlog.DebugLevel
	}
	return charmlog.NewWithOptions(w, charmlog.Options{
		ReportTimestamp: true,
		TimeFormat:      time.TimeOnly,
		Level:           charmLevel,
	})
}

func bootLogHandlers(stderr, file io.Writer, level slog.Level, format string) (boot, stderrOnly slog.Handler) {
	stderrOnly = newStderrLogHandler(stderr, level, format)
	fileHandler := newFileLogHandler(file, level, format)
	return logging.NewRedactingHandler(logging.NewFanoutHandler(stderrOnly, fileHandler)), stderrOnly
}

// earlyLogHandler is the slog default from the first line of main until
// bootLogHandlers takes over (M4-close D8). Before it, records emitted that
// early — dotenv load failures, ITERVOX_BIN warnings, the invalid
// --log-format warning, claimPIDFile's stale-record reclaim — went through
// Go's default text logger: never JSON on a headless daemon and never
// redacted. It writes to w (stderr) only: nothing may touch the log file the
// live daemon owns before claimPIDFile has refused a second start. The
// format is pre-scanned from args (the --log-format flag, which wins) and
// env (ITERVOX_LOG_FORMAT), because flag.Parse has not run yet; the level is
// INFO (--verbose is not known yet either).
func earlyLogHandler(w io.Writer, args []string, env string) slog.Handler {
	format := resolveLogFormat(scanLogFormatFlag(args), env)
	return logging.NewRedactingHandler(newStderrLogHandler(w, slog.LevelInfo, format))
}

// scanLogFormatFlag returns the value of the last -log-format / --log-format
// flag in args ("--log-format json" or "--log-format=json"), or "".
func scanLogFormatFlag(args []string) string {
	v := ""
	for i := 1; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			break
		}
		name, val, hasVal := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if !strings.HasPrefix(a, "-") || name != "log-format" {
			continue
		}
		if hasVal {
			v = val
		} else if i+1 < len(args) {
			v = args[i+1]
			i++
		}
	}
	return v
}
