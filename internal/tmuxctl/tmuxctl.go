// Package tmuxctl drives the tmux CLI for the Sessions panel (#178): it lists
// panes, captures a pane's screen, pastes text, sends a short allowlist of
// keys, and starts and kills itervox's own tmux sessions.
//
// It is the ONLY package that may write to a process itervox did not start:
// an operator's agent CLI running in a tmux pane, reached through tmux's own
// paste buffer and key events (never a terminal device, never ptrace). The
// structural guard TestNoWritesToProcessesItervoxDidNotStart
// (internal/agent/no_foreign_writes_test.go) holds every other package to
// "processes itervox started only", and holds this one to running `tmux`
// and nothing else, with explicit arguments (never a shell string of ours).
//
// Stdlib only. Every call is a short-lived `tmux` client process bounded by
// Client.Timeout; nothing here runs on the orchestrator's event loop.
package tmuxctl

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultTimeout bounds one tmux call.
const DefaultTimeout = 5 * time.Second

// Capture bounds (lines of scrollback).
const (
	DefaultCaptureLines = 200
	MaxCaptureLines     = 2000
)

// MaxTextBytes caps one paste.
const MaxTextBytes = 10 * 1024

// submitDelay separates a paste from the Enter that submits it, so the CLI
// has finished reading the bracketed paste before the key arrives.
const submitDelay = 150 * time.Millisecond

// Errors.
var (
	// ErrUnavailable: no tmux binary on PATH.
	ErrUnavailable = errors.New("tmuxctl: tmux is not installed")
	// ErrKeyNotAllowed: SendKey was asked for a key outside AllowedKeys.
	ErrKeyNotAllowed = errors.New("tmuxctl: key not allowed")
	// ErrBadTarget: a pane id or session name that tmux could misread.
	ErrBadTarget = errors.New("tmuxctl: invalid pane or session")
	// ErrNoSuchPane: the pane (or session) no longer exists.
	ErrNoSuchPane = errors.New("tmuxctl: no such pane")
)

// AllowedKeys are the only keys SendKey sends: navigation, submit, cancel,
// and the digits a CLI's numbered permission prompt takes.
var AllowedKeys = []string{"Enter", "Escape", "Up", "Down", "Left", "Right", "Tab", "C-c",
	"1", "2", "3", "4", "5", "6", "7", "8", "9"}

// AllowedKey reports whether key is in AllowedKeys.
func AllowedKey(key string) bool { return slices.Contains(AllowedKeys, key) }

var (
	paneIDRe      = regexp.MustCompile(`^%[0-9]+$`)
	sessionNameRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
)

// ValidPaneID reports whether id is a tmux pane id ("%12").
func ValidPaneID(id string) bool { return paneIDRe.MatchString(id) }

// ValidSessionName reports whether name is a session name this package
// creates or targets (letters, digits, '-' and '_').
func ValidSessionName(name string) bool { return sessionNameRe.MatchString(name) }

// Pane is one tmux pane.
type Pane struct {
	ID      string // "%12"
	PID     int    // the pane's first process (its shell, or the command it runs)
	Cwd     string // pane_current_path: the foreground process's directory
	Command string // pane_current_command
	Session string
	Window  int
	Index   int
}

// Label is "session:window.pane", what `tmux attach -t` and the panel show.
func (p Pane) Label() string {
	return p.Session + ":" + strconv.Itoa(p.Window) + "." + strconv.Itoa(p.Index)
}

// Client runs tmux. The zero value uses the default server ($TMUX_TMPDIR /
// the user's default socket, as tmux itself resolves it).
type Client struct {
	// Socket is a server socket path (tmux -S); "" = the default server.
	Socket string
	// Timeout bounds each call; <= 0 means DefaultTimeout.
	Timeout time.Duration
}

var missingOnce sync.Once

// run executes one tmux command with explicit arguments and returns stdout.
func (c Client) run(ctx context.Context, stdin string, args ...string) ([]byte, error) {
	if _, err := exec.LookPath("tmux"); err != nil {
		missingOnce.Do(func() { slog.Debug("tmuxctl: tmux is not on PATH; tmux control is unavailable") })
		return nil, ErrUnavailable
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	full := args
	if c.Socket != "" {
		full = append([]string{"-S", c.Socket}, args...)
	}
	cmd := exec.CommandContext(ctx, "tmux", full...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if noServer(msg) || strings.Contains(msg, "can't find") || strings.Contains(msg, "no such") {
			return nil, fmt.Errorf("%w: %s", ErrNoSuchPane, msg)
		}
		return nil, fmt.Errorf("tmuxctl: tmux %s: %w: %s", args[0], err, msg)
	}
	return stdout.Bytes(), nil
}

// noServer reports a tmux error meaning no server is running.
func noServer(msg string) bool {
	return strings.Contains(msg, "no server running") || strings.Contains(msg, "error connecting to") ||
		strings.Contains(msg, "No such file or directory")
}

// Version returns tmux's version ("tmux 3.4"), or ErrUnavailable.
func (c Client) Version(ctx context.Context) (string, error) {
	out, err := c.run(ctx, "", "-V")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// paneSep separates list-panes fields: tmux prints control characters
// such as tabs as '_', so the separator is printable but implausible in a
// session name or command. The path, the only free-form field that may
// contain anything, comes last.
const paneSep = "<<itervox>>"

const paneFormat = "#{pane_id}" + paneSep + "#{pane_pid}" + paneSep + "#{session_name}" + paneSep +
	"#{window_index}" + paneSep + "#{pane_index}" + paneSep + "#{pane_current_command}" + paneSep + "#{pane_current_path}"

// ListPanes lists every pane of the server. No tmux binary or no running
// server is an empty list, not an error.
func (c Client) ListPanes(ctx context.Context) ([]Pane, error) {
	out, err := c.run(ctx, "", "list-panes", "-a", "-F", paneFormat)
	if errors.Is(err, ErrUnavailable) || errors.Is(err, ErrNoSuchPane) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return parsePanes(out), nil
}

func parsePanes(out []byte) []Pane {
	var panes []Pane
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.SplitN(line, paneSep, 7)
		if len(f) != 7 || !ValidPaneID(f[0]) {
			continue
		}
		pid, _ := strconv.Atoi(f[1])
		win, _ := strconv.Atoi(f[3])
		idx, _ := strconv.Atoi(f[4])
		panes = append(panes, Pane{ID: f[0], PID: pid, Session: f[2], Window: win, Index: idx, Command: f[5], Cwd: f[6]})
	}
	return panes
}

// Capture returns the last lines of the pane's screen and scrollback as
// plain text (no escape sequences), wrapped lines joined.
func (c Client) Capture(ctx context.Context, paneID string, lines int) (string, error) {
	if !ValidPaneID(paneID) {
		return "", ErrBadTarget
	}
	if lines <= 0 {
		lines = DefaultCaptureLines
	}
	lines = min(lines, MaxCaptureLines)
	out, err := c.run(ctx, "", "capture-pane", "-p", "-J", "-t", paneID, "-S", "-"+strconv.Itoa(lines))
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// SendText pastes text into the pane as a bracketed paste through a
// private, single-use buffer, then presses Enter when submit is set.
func (c Client) SendText(ctx context.Context, paneID, text string, submit bool) error {
	if !ValidPaneID(paneID) {
		return ErrBadTarget
	}
	if len(text) > MaxTextBytes {
		return fmt.Errorf("tmuxctl: text exceeds %d bytes", MaxTextBytes)
	}
	if text != "" {
		buf := bufferName()
		if _, err := c.run(ctx, text, "load-buffer", "-b", buf, "-"); err != nil {
			return err
		}
		if _, err := c.run(ctx, "", "paste-buffer", "-p", "-d", "-b", buf, "-t", paneID); err != nil {
			_, _ = c.run(ctx, "", "delete-buffer", "-b", buf)
			return err
		}
	}
	if !submit {
		return nil
	}
	if text != "" {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(submitDelay):
		}
	}
	_, err := c.run(ctx, "", "send-keys", "-t", paneID, "Enter")
	return err
}

// SendKey presses one allowlisted key in the pane.
func (c Client) SendKey(ctx context.Context, paneID, key string) error {
	if !AllowedKey(key) {
		return ErrKeyNotAllowed
	}
	if !ValidPaneID(paneID) {
		return ErrBadTarget
	}
	_, err := c.run(ctx, "", "send-keys", "-t", paneID, key)
	return err
}

// NewSession starts a detached session named name in cwd running command,
// a POSIX shell command line (its caller quotes what it adds). tmux gets
// /bin/sh -c command as separate arguments, so it execs them directly
// rather than through the user's default shell and its startup files.
func (c Client) NewSession(ctx context.Context, name, cwd, command string) error {
	if !ValidSessionName(name) || cwd == "" || command == "" {
		return ErrBadTarget
	}
	_, err := c.run(ctx, "", "new-session", "-d", "-s", name, "-c", cwd, "-x", "200", "-y", "50",
		"/bin/sh", "-c", command)
	return err
}

// KillSession kills the session named exactly name.
func (c Client) KillSession(ctx context.Context, name string) error {
	if !ValidSessionName(name) {
		return ErrBadTarget
	}
	_, err := c.run(ctx, "", "kill-session", "-t", "="+name)
	return err
}

// bufferName is a single-use paste buffer name.
func bufferName() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "itervox-" + hex.EncodeToString(b)
}
