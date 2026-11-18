package tmuxctl

import (
	"errors"
	"os"
	"slices"
	"strconv"
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
// (plus `-f ignore-size` where tmux has it) for a read-only attach, and the structural guard
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

// AttachMode is how an attach joins the session.
type AttachMode struct {
	// ReadOnly attaches with -r: tmux ignores the client's keys (except
	// its own detach and switch-client keys), and the daemon types nothing.
	ReadOnly bool
	// IgnoreSize adds the client flag ignore-size (`-f ignore-size`, tmux
	// 3.2+; see ClientFlagsSupported) to a read-only attach, so a small
	// browser panel never shrinks the window everyone else sees. Ignored
	// for a read-write attach, which sizes the window like any client.
	// (tmux 3.2+ already gives a -r client this flag; naming it keeps the
	// behaviour explicit rather than resting on that default.)
	IgnoreSize bool
}

// AttachArgs is the argv after "tmux" of an attach to target: exactly
// `attach-session [-r [-f ignore-size]] -t <target>`, preceded by
// `-S <socket>` for a private server. Nothing else: no shell, no other
// command, no -d (which would detach the operator's own terminal from the
// session).
func (c Client) AttachArgs(target string, mode AttachMode) ([]string, error) {
	if !validAttachTarget(target) {
		return nil, ErrBadTarget
	}
	args := []string{"attach-session"}
	if mode.ReadOnly {
		args = append(args, "-r")
		if mode.IgnoreSize {
			args = append(args, "-f", "ignore-size")
		}
	}
	args = append(args, "-t", target)
	if c.Socket != "" {
		args = append([]string{"-S", c.Socket}, args...)
	}
	return args, nil
}

// ClientFlagsSupported reports whether the tmux of version (the `tmux -V`
// answer: "tmux 3.4", "tmux 3.2a", "tmux next-3.6", "tmux master",
// "tmux openbsd-7.5") takes attach-session's client flags (`-f`, tmux
// 3.2; OpenBSD 6.9 shipped it). An older tmux refuses -f and the client
// exits, so anything this cannot read is "no": the attach then runs with
// plain -r, which on tmux before 3.2 may size the window to the panel.
func ClientFlagsSupported(version string) bool {
	v := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(version), "tmux"))
	switch {
	case v == "master":
		return true
	case strings.HasPrefix(v, "openbsd-"):
		return versionAtLeast(strings.TrimPrefix(v, "openbsd-"), 6, 9)
	}
	return versionAtLeast(strings.TrimPrefix(v, "next-"), 3, 2)
}

// versionAtLeast reports whether v ("3.2a", "3.10") is at least major.minor.
func versionAtLeast(v string, major, minor int) bool {
	maj, rest, ok := strings.Cut(v, ".")
	if !ok {
		return false
	}
	n := 0
	for n < len(rest) && rest[n] >= '0' && rest[n] <= '9' {
		n++
	}
	ma, err1 := strconv.Atoi(maj)
	mi, err2 := strconv.Atoi(rest[:n])
	if err1 != nil || err2 != nil {
		return false
	}
	return ma > major || ma == major && mi >= minor
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
