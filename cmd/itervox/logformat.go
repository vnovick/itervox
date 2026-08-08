package main

import (
	"io"
	"log/slog"

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

// postStartupHandler builds the slog handler installed as the default right
// before statusui.Run is called — the point where, historically, slog was
// unconditionally redirected to the file sink only, on the theory that the
// TUI is about to take the alt-screen and concurrent stderr writes would
// corrupt it.
//
// That theory only holds when the TUI actually starts. ttyAvailable is the
// caller's statusui.TerminalAvailable() result: it mirrors (via the same
// underlying TTY-ownership check) whether statusui.Run will start the TUI or
// refuse immediately for lack of a controlling terminal (systemd, container,
// CI, detached stdio — issue #49-2).
//
//   - ttyAvailable == true: the TUI is about to start. File-only, exactly as
//     before this seam existed — the TUI log pane reads from logBuf, not
//     stderr, so nothing is lost.
//   - ttyAvailable == false: statusui.Run will refuse to start. Keep the
//     startup stderr+file fanout alive so post-startup logs still reach
//     stderr (journalctl et al.) instead of going dark for nothing.
//
// stderrHandler is reused from the caller's existing stderr handler (main's
// stderrOnly logger) rather than constructed fresh, so the headless fanout's
// stderr leg is identical to the one used for the one-time dashboard-URL
// line — no risk of a second, divergently-configured charmlog instance.
//
// Both branches are wrapped in logging.NewRedactingHandler: the file sink
// must never receive raw secrets, and in headless mode neither does stderr.
func postStartupHandler(fileWriter io.Writer, logLevel slog.Level, logFormat string, stderrHandler slog.Handler, ttyAvailable bool) slog.Handler {
	fileHandler := newFileLogHandler(fileWriter, logLevel, logFormat)
	if !ttyAvailable {
		return logging.NewRedactingHandler(logging.NewFanoutHandler(stderrHandler, fileHandler))
	}
	return logging.NewRedactingHandler(fileHandler)
}
