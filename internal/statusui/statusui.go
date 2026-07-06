package statusui

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vnovick/itervox/internal/logbuffer"
	"github.com/vnovick/itervox/internal/server"
)

// Run starts the bubbletea TUI, writing to stderr. It blocks until ctx is
// cancelled or the user presses q. buf may be nil (log pane disabled).
// cancelFn is called when the user presses x to kill the selected session;
// nil disables it.
//
// TTY signal policy — important:
//
//   - SIGTTOU (background process writes to TTY): IGNORED. Without this,
//     launching the daemon when a sibling job or stale TUI instance holds
//     the foreground pgrp causes the shell to suspend us immediately on
//     our first altscreen render (`[2]  + 88582 suspended (tty output)`).
//     Ignoring SIGTTOU lets bubbletea's write go through; the kernel
//     returns EIO / consumes it silently based on termios, which is much
//     better UX than an unexplained-looking suspend.
//
//   - SIGTTIN (background process reads from TTY): NOT ignored. An earlier
//     revision ignored this too — that turned out to be the root cause of
//     the `w`-key crash reported as:
//
//     "program was killed: error reading input: read /dev/stdin: input/output error"
//
//     When SIGTTIN is ignored, a background process reading from the TTY
//     gets an immediate EIO from `read(0)` instead of being suspended
//     until it regains foreground. bubbletea treats that EIO as a fatal
//     input-loop error and dies. Default SIGTTIN handling (briefly
//     suspend, resume when TTY ownership settles) is the correct
//     behaviour for an interactive TUI.
//
// Run starts the bubbletea TUI and returns a channel that is closed once the
// TUI has fully shut down and the terminal has been restored to cooked mode.
// Callers MUST wait on the returned channel before exiting the process;
// otherwise the process may terminate while the terminal is still in raw mode,
// leaving the user's shell broken (no Ctrl-C, no echo, arrow keys print
// escape sequences).
func Run(ctx context.Context, snap func() server.StateSnapshot, buf *logbuffer.Buffer, cfg Config, cancelFn func(string) bool) <-chan struct{} {
	done := make(chan struct{})

	signal.Ignore(syscall.SIGTTOU)

	if err := checkForegroundTTYOwnershipWithRetry(); err != nil {
		msg := fmt.Sprintf("statusui: not starting TUI: %v", err)
		_, _ = fmt.Fprintln(os.Stderr, msg)
		slog.Warn("statusui: refusing to start TUI", "error", err)
		close(done)
		return done
	}

	m := New(snap, buf, cfg, cancelFn)
	p := tea.NewProgram(m,
		tea.WithOutput(os.Stderr),
		tea.WithAltScreen(),
	)

	// Stop the program when the context is cancelled.
	go func() {
		<-ctx.Done()
		p.Quit()
	}()

	go func() {
		// Always release the terminal AND forcibly restore canonical mode on
		// return — including error-path returns. Without this, if bubbletea's
		// input reader fails (e.g. stdin EIO), the terminal stays in raw mode:
		// Ctrl+C doesn't send SIGINT anymore, arrow keys get echoed as `^[[AC`
		// literals, and the shell becomes nearly unusable until the user runs
		// `stty sane` by hand.
		//
		// Two-step defense:
		//  1. p.ReleaseTerminal() asks bubbletea to give the TTY back. Usually
		//     enough when bubbletea shut down cleanly.
		//  2. `stty sane </dev/tty` is a belt-and-braces reset that the shell
		//     uses for exactly this "terminal got wedged by a crashed TUI"
		//     scenario. It restores the termios flags (ECHO, ICANON, ISIG) and
		//     reinstates line discipline. If stty isn't on PATH (Windows,
		//     stripped containers) we silently skip.
		defer close(done)
		defer func() {
			_ = p.ReleaseTerminal()
			restoreTerminal()
		}()

		if _, err := p.Run(); err != nil {
			// When ctx has already been cancelled (SIGTERM / `itervox stop` /
			// reload), bubbletea's stdin reader routinely returns EIO as the
			// TTY tears down — surfacing as:
			//
			//   "program was killed: error reading input: read /dev/stdin: input/output error"
			//
			// That's expected shutdown behaviour for any TUI whose stdin goroutine
			// is blocked on read when the process gets killed. Demoting to Debug
			// avoids polluting normal shutdown logs; truly unexpected errors
			// (bubbletea crashing mid-session) still get WARN because ctx is
			// not yet done in that case.
			if ctx.Err() != nil {
				slog.Debug("statusui: bubbletea exited during shutdown (expected)", "error", err)
			} else {
				slog.Warn("statusui: bubbletea exited with error", "error", err)
			}
		}
	}()

	return done
}

// restoreTerminal runs `stty sane` against the controlling TTY to forcibly
// reset termios flags (ECHO, ICANON, ISIG) after bubbletea exits. This is
// the belt-and-braces complement to p.ReleaseTerminal().
func restoreTerminal() {
	if runtime.GOOS == "windows" {
		return
	}
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return
	}
	cmd := exec.Command("stty", "sane")
	cmd.Stdin = tty
	cmd.Stdout = tty
	cmd.Stderr = tty
	_ = cmd.Run()
	_ = tty.Close()
}
