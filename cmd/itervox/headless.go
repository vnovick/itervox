package main

import (
	"fmt"
	"os"
)

// The daemon always runs headless (#134, #240): no terminal UI, logs on
// stderr (text, or JSON with --log-format json), and the dashboard URL
// printed there (with its token only when stderr is a terminal). The
// terminal UI was removed in v0.3; `itervox watch` is the terminal view.

// tuiFlag is --tui, kept only so the flag is recognised and answered with
// tuiRemovedNotice instead of a generic "flag provided but not defined".
// Set by flag.Parse.
var tuiFlag bool

// headlessFlag is --headless (#120): the tokenised dashboard URL is never
// printed, even on a terminal; an auto-generated token stays readable at
// <logs-dir>/api-token. Set by flag.Parse.
var headlessFlag bool

// tuiRemovedNotice is the one line --tui prints (#240).
const tuiRemovedNotice = "itervox: --tui was removed in v0.3; start the daemon without it and watch it with `itervox watch` (or open the dashboard)"

// tuiRemovedExitCode is the exit code of `itervox --tui`: a usage error.
const tuiRemovedExitCode = 2

// exitIfTUIRequested answers --tui with tuiRemovedNotice and exits non-zero
// before the daemon claims any runtime file. Called right after flag.Parse.
func exitIfTUIRequested() {
	if !tuiFlag {
		return
	}
	_, _ = fmt.Fprintln(os.Stderr, tuiRemovedNotice)
	fatalExit(tuiRemovedExitCode)
}
