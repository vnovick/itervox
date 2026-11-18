//go:build !unix

package tmuxctl

import (
	"context"
	"errors"
)

// AttachSupported reports whether this platform can run a live terminal:
// there is no tmux (and no PTY) on native Windows.
const AttachSupported = false

// ErrReadOnly: Write on a read-only attachment.
var ErrReadOnly = errors.New("tmuxctl: the attachment is read-only")

// Attachment is never created on this platform.
type Attachment struct{}

// Attach always fails with ErrAttachUnsupported here.
func (c Client) Attach(context.Context, string, bool, int, int) (*Attachment, error) {
	return nil, ErrAttachUnsupported
}

// ReadOnly is never reached here.
func (a *Attachment) ReadOnly() bool { return true }

// Read is never reached here.
func (a *Attachment) Read([]byte) (int, error) { return 0, ErrAttachUnsupported }

// Write is never reached here.
func (a *Attachment) Write([]byte) (int, error) { return 0, ErrAttachUnsupported }

// Resize is never reached here.
func (a *Attachment) Resize(int, int) error { return ErrAttachUnsupported }

// Size is never reached here.
func (a *Attachment) Size() (int, int, error) { return 0, 0, ErrAttachUnsupported }

// Close is never reached here.
func (a *Attachment) Close() error { return nil }
