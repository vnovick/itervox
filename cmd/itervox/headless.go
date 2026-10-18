package main

import (
	"context"
	"log/slog"
	"strconv"
	"strings"

	"github.com/vnovick/itervox/internal/logbuffer"
	"github.com/vnovick/itervox/internal/server"
	"github.com/vnovick/itervox/internal/statusui"
)

// The daemon runs headless by default (#134): no terminal UI, logs on
// stderr (text, or JSON with --log-format json), and the dashboard URL
// printed there (with its token only when stderr is a terminal). --tui
// starts the deprecated terminal UI instead, on a terminal.

// tuiFlag is --tui (#134): start the terminal UI. Set by flag.Parse.
var tuiFlag bool

// headlessFlag is --headless (#120): no terminal UI even with --tui, and the
// tokenised dashboard URL is never printed, even on a terminal; an
// auto-generated token stays readable at <logs-dir>/api-token. Set by
// flag.Parse.
var headlessFlag bool

// argsBool reports the boolean flag name in args, for the logger set up
// before flag.Parse runs.
func argsBool(args []string, name string) bool {
	on := false
	for _, a := range args[1:] {
		if a == "--" {
			break
		}
		n, val, hasVal := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if !strings.HasPrefix(a, "-") || n != name {
			continue
		}
		on = true
		if hasVal {
			on, _ = strconv.ParseBool(val) // as flag.Parse reads it
		}
	}
	return on
}

// argsTerminalUI is terminalUI from args, before flag.Parse runs.
func argsTerminalUI(args []string) bool {
	return argsBool(args, "tui") && !argsBool(args, "headless") && statusui.TerminalAvailable()
}

// terminalUI reports whether the terminal UI starts: --tui was given,
// --headless was not, and a terminal is attached.
func terminalUI() bool {
	return tuiFlag && !headlessFlag && statusui.TerminalAvailable()
}

// tuiDeprecationNotice is shown in the terminal UI's header (#120, #134).
const tuiDeprecationNotice = "The terminal UI is deprecated and will be removed in v0.3: use the dashboard and `itervox watch`"

// startTUI runs the terminal UI when terminalUI holds; otherwise the
// returned channel is already closed. It closes when the terminal is
// restored.
func startTUI(ctx context.Context, snap func() server.StateSnapshot, buf *logbuffer.Buffer, cfg statusui.Config, cancel func(string) bool) <-chan struct{} {
	if !terminalUI() {
		if tuiFlag && !headlessFlag {
			slog.Warn("--tui: no terminal is attached, so the terminal UI does not start")
		}
		done := make(chan struct{})
		close(done)
		return done
	}
	slog.Warn("the terminal UI is deprecated and will be removed in v0.3; itervox runs headless without --tui")
	return statusui.Run(ctx, snap, buf, cfg, cancel)
}
