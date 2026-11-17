//go:build unix

package tmuxctl

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
)

// AttachSupported reports whether this platform can run a live terminal.
const AttachSupported = true

// detachGrace is how long Close lets the attach client exit on its hangup
// before it is killed. Either way the tmux session keeps running.
const detachGrace = 2 * time.Second

// Attachment is one running `tmux attach-session` client inside a PTY the
// daemon owns. Read returns what tmux draws; Write types into the session
// (refused for a read-only attach); Resize sets the PTY's window size
// (TIOCSWINSZ), which tmux follows. Close detaches: the client ends, the
// session does not. Safe for one reader and one writer at a time, plus
// Resize and Close from any goroutine.
type Attachment struct {
	readOnly bool
	ptmx     *os.File
	cmd      *exec.Cmd

	mu     sync.Mutex
	closed bool
	once   sync.Once
	err    error
}

// Attach starts `tmux attach-session [-r] -t target` (see AttachArgs) in a
// new PTY of cols x rows (clamped). ctx bounds only the start, not the
// attachment's life: Close ends it.
func (c Client) Attach(ctx context.Context, target string, readOnly bool, cols, rows int) (*Attachment, error) {
	args, err := c.AttachArgs(target, readOnly)
	if err != nil {
		return nil, err
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		return nil, ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	w, h := clampSize(cols, rows)
	cmd := exec.Command("tmux", args...)
	cmd.Env = attachEnv()
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: w, Rows: h})
	if err != nil {
		return nil, fmt.Errorf("tmuxctl: attach: %w", err)
	}
	return &Attachment{readOnly: readOnly, ptmx: ptmx, cmd: cmd}, nil
}

// ReadOnly reports whether this is a read-only (-r) attach.
func (a *Attachment) ReadOnly() bool { return a.readOnly }

// ErrReadOnly: Write on a read-only attachment. The daemon never types into
// a read-only attach (tmux would still honour a read-only client's
// switch-client and detach keys, which could reach another session).
var ErrReadOnly = errors.New("tmuxctl: the attachment is read-only")

// Read reads what the tmux client draws. It returns an error (io.EOF or
// EIO) once the client has exited.
func (a *Attachment) Read(p []byte) (int, error) { return a.ptmx.Read(p) }

// Write types p into the session through the client's terminal.
func (a *Attachment) Write(p []byte) (int, error) {
	if a.readOnly {
		return 0, ErrReadOnly
	}
	return a.ptmx.Write(p)
}

// Resize sets the terminal size (clamped) with TIOCSWINSZ; the kernel
// signals SIGWINCH to the client, which tells tmux.
func (a *Attachment) Resize(cols, rows int) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return ErrAttachClosed
	}
	w, h := clampSize(cols, rows)
	return pty.Setsize(a.ptmx, &pty.Winsize{Cols: w, Rows: h})
}

// Size reports the PTY's current size (columns, rows).
func (a *Attachment) Size() (cols, rows int, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return 0, 0, ErrAttachClosed
	}
	h, w, err := pty.Getsize(a.ptmx)
	return w, h, err
}

// Close detaches: it closes the PTY and hangs up the client, kills it if it
// is still there after detachGrace, and reaps it. The session keeps
// running. Idempotent; returns the client's exit error, if any, other than
// the hangup it was sent.
func (a *Attachment) Close() error {
	a.once.Do(func() {
		a.mu.Lock()
		a.closed = true
		a.mu.Unlock()
		_ = a.ptmx.Close()
		if a.cmd.Process == nil {
			return
		}
		_ = a.cmd.Process.Signal(syscall.SIGHUP)
		kill := time.AfterFunc(detachGrace, func() { _ = a.cmd.Process.Kill() })
		err := a.cmd.Wait()
		kill.Stop()
		var exit *exec.ExitError
		if err != nil && !errors.As(err, &exit) {
			a.err = err
		}
	})
	return a.err
}
