package main

import (
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tempErr is a net.Error that reports itself as temporary, the same shape
// syscall.EMFILE/ENFILE present when the process runs out of file
// descriptors.
type tempErr struct{}

func (tempErr) Error() string   { return "temporary accept failure" }
func (tempErr) Timeout() bool   { return false }
func (tempErr) Temporary() bool { return true }

// flakyListener fails the first n Accept calls with a temporary error, then
// serves one real connection.
type flakyListener struct {
	remaining atomic.Int32
	accepts   atomic.Int32
	inner     net.Listener
}

func (f *flakyListener) Accept() (net.Conn, error) {
	f.accepts.Add(1)
	if f.remaining.Add(-1) >= 0 {
		return nil, tempErr{}
	}
	return f.inner.Accept()
}
func (f *flakyListener) Close() error   { return f.inner.Close() }
func (f *flakyListener) Addr() net.Addr { return f.inner.Addr() }

// TestPersistentListenerRetriesTemporaryAcceptErrors pins that a transient
// accept failure does not permanently wedge the HTTP listener.
//
// Moving the accept loop out of http.Server.Serve into pump() dropped
// Serve's temporary-error backoff. pump returned on ANY error, so a single
// fd-exhaustion blip (EMFILE/ENFILE, which report Temporary() == true) ended
// accepts for the life of the process — while the socket stayed bound. The
// port still showed LISTEN to lsof and to `itervox doctor`, clients just
// hung, and a config reload could not recover it because the resolved bind
// address was unchanged and so no rebind was attempted.
func TestPersistentListenerRetriesTemporaryAcceptErrors(t *testing.T) {
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	fl := &flakyListener{inner: inner}
	fl.remaining.Store(3) // three temporary failures before the real accept

	p := newPersistentListener(fl)
	defer p.Close() //nolint:errcheck

	gen := p.generation()
	defer gen.Close() //nolint:errcheck

	accepted := make(chan net.Conn, 1)
	go func() {
		c, aerr := gen.Accept()
		if aerr == nil {
			accepted <- c
		}
	}()

	dialed, err := net.Dial("tcp", inner.Addr().String())
	require.NoError(t, err)
	defer dialed.Close() //nolint:errcheck

	select {
	case c := <-accepted:
		_ = c.Close()
	case <-time.After(3 * time.Second):
		t.Fatal("connection never accepted: pump gave up on a temporary error")
	}

	assert.GreaterOrEqual(t, int(fl.accepts.Load()), 4,
		"pump must retry past the temporary failures rather than exit on the first")
}
