package main

import (
	"context"
	"strings"

	"github.com/vnovick/itervox/internal/logbuffer"
	"github.com/vnovick/itervox/internal/server"
	"github.com/vnovick/itervox/internal/statusui"
)

// headlessFlag is --headless (#120): no terminal UI even on a terminal, logs
// on stderr (text, or JSON with --log-format json), and the tokenised
// dashboard URL is never printed; an auto-generated token stays readable at
// <logs-dir>/api-token. Set by flag.Parse.
var headlessFlag bool

// argsHeadless reports a --headless flag in args, for the logger set up
// before flag.Parse runs.
func argsHeadless(args []string) bool {
	on := false
	for _, a := range args[1:] {
		if a == "--" {
			break
		}
		name, val, hasVal := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if !strings.HasPrefix(a, "-") || name != "headless" {
			continue
		}
		on = !hasVal || val == "true" || val == "1"
	}
	return on
}

// terminalUI reports whether the terminal UI starts: a terminal is attached
// and --headless was not given.
func terminalUI() bool {
	return !headlessFlag && statusui.TerminalAvailable()
}

// tuiRetirementNotice is shown in the terminal UI's header (#120).
const tuiRetirementNotice = "The terminal UI is being retired: try `itervox watch` (in another shell) and `itervox --headless`"

// startTUI runs the terminal UI, or nothing with --headless; the returned
// channel closes when the terminal is restored.
func startTUI(ctx context.Context, snap func() server.StateSnapshot, buf *logbuffer.Buffer, cfg statusui.Config, cancel func(string) bool) <-chan struct{} {
	if headlessFlag {
		done := make(chan struct{})
		close(done)
		return done
	}
	return statusui.Run(ctx, snap, buf, cfg, cancel)
}
