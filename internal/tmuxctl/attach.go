package tmuxctl

import (
	"errors"
	"os"
	"slices"
	"strings"
)

// The live terminal (#298): `tmux attach-session` run in a pseudo-terminal
// the daemon owns, its bytes streamed to the dashboard. The attach itself is
// in attach_unix.go (creack/pty); this file holds what every platform
// shares: the exact argv, the target rules and the errors.
//
// An attach is the one tmux client of this package that outlives a call:
// the daemon starts it, owns its PTY, and ends it on detach (the session
// keeps running). It drives nothing but `tmux attach-session`, with `-r`
// for a read-only attach, and the structural guard
// TestNoWritesToProcessesItervoxDidNotStart holds the PTY to this package.

// Errors of an attach.
var (
	// ErrAttachUnsupported: no PTY attach on this platform (Windows).
	ErrAttachUnsupported = errors.New("tmuxctl: a live terminal is not supported on this platform")
	// ErrAttachClosed: the attachment was already closed.
	ErrAttachClosed = errors.New("tmuxctl: the attachment is closed")
)

// Terminal size bounds of an attach (columns, rows). Out-of-range sizes are
// clamped, never refused: a browser reports whatever fits its panel.
const (
	MinTermCols     = 20
	MaxTermCols     = 500
	MinTermRows     = 5
	MaxTermRows     = 200
	DefaultTermCols = 120
	DefaultTermRows = 32
)

// AttachTarget is how an attach names its target: the exact session name
// (`=name`, so tmux never prefix-matches another session), or a pane id
// ("%12", which also selects that pane's window).
func AttachTarget(session, paneID string) (string, error) {
	switch {
	case session != "" && ValidSessionName(session):
		return "=" + session, nil
	case paneID != "" && ValidPaneID(paneID):
		return paneID, nil
	}
	return "", ErrBadTarget
}

// validAttachTarget accepts only what AttachTarget builds.
func validAttachTarget(target string) bool {
	if name, ok := strings.CutPrefix(target, "="); ok {
		return ValidSessionName(name)
	}
	return ValidPaneID(target)
}

// AttachArgs is the argv after "tmux" of an attach to target: exactly
// `attach-session [-r] -t <target>`, preceded by `-S <socket>` for a
// private server. Nothing else: no shell, no other command, no -d (which
// would detach the operator's own terminal from the session).
func (c Client) AttachArgs(target string, readOnly bool) ([]string, error) {
	if !validAttachTarget(target) {
		return nil, ErrBadTarget
	}
	args := []string{"attach-session"}
	if readOnly {
		args = append(args, "-r")
	}
	args = append(args, "-t", target)
	if c.Socket != "" {
		args = append([]string{"-S", c.Socket}, args...)
	}
	return args, nil
}

// clampSize bounds a terminal size; 0 picks the default.
func clampSize(cols, rows int) (uint16, uint16) {
	if cols <= 0 {
		cols = DefaultTermCols
	}
	if rows <= 0 {
		rows = DefaultTermRows
	}
	return uint16(min(max(cols, MinTermCols), MaxTermCols)), uint16(min(max(rows, MinTermRows), MaxTermRows))
}

// attachEnv is the environment of the attach client: the daemon's, without
// TMUX (tmux refuses to attach from "inside" a session, which a daemon
// started in tmux would otherwise look like), with a TERM xterm.js renders.
func attachEnv() []string {
	env := slices.DeleteFunc(os.Environ(), func(kv string) bool {
		return strings.HasPrefix(kv, "TMUX=") || strings.HasPrefix(kv, "TMUX_PANE=") || strings.HasPrefix(kv, "TERM=")
	})
	return append(env, "TERM=xterm-256color")
}
