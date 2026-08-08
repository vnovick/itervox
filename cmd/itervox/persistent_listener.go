package main

import (
	"net"
	"sync"
)

// persistentListener owns a bound TCP socket for the daemon's lifetime and
// hands out per-run views of it via generation().
//
// Why the indirection: http.Server.Shutdown closes the listener it serves on.
// The daemon restarts run() on every config reload, so serving straight on the
// bound socket would tear it down and rebind each time — re-rolling the port
// when the OS picked it, and racing EADDRINUSE against the dying server's
// 200ms shutdown window when the port is fixed. With this wrapper, Shutdown
// closes only its generation; the socket stays bound, the port never changes,
// and a connection arriving in the reload gap waits for the next generation
// instead of being refused.
type persistentListener struct {
	raw   net.Listener
	conns chan net.Conn

	closeOnce sync.Once
	closed    chan struct{} // Close() called; pump and generations shut down
	pumpDone  chan struct{} // pump exited; acceptErr is set

	mu        sync.Mutex
	acceptErr error
}

func newPersistentListener(raw net.Listener) *persistentListener {
	p := &persistentListener{
		raw:      raw,
		conns:    make(chan net.Conn),
		closed:   make(chan struct{}),
		pumpDone: make(chan struct{}),
	}
	go p.pump()
	return p
}

// pump moves accepted connections onto the channel generations read from.
// It is the only reader of the raw socket, so a generation closing (reload)
// never disturbs the accept loop.
func (p *persistentListener) pump() {
	defer close(p.pumpDone)
	for {
		conn, err := p.raw.Accept()
		if err != nil {
			p.mu.Lock()
			p.acceptErr = err
			p.mu.Unlock()
			return
		}
		select {
		case p.conns <- conn:
		case <-p.closed:
			_ = conn.Close()
			return
		}
	}
}

// Close shuts the underlying socket. Only for host/port changes and process
// exit — a reload must close its generation instead.
func (p *persistentListener) Close() error {
	p.closeOnce.Do(func() { close(p.closed) })
	return p.raw.Close()
}

func (p *persistentListener) Addr() net.Addr { return p.raw.Addr() }

func (p *persistentListener) takeErr() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.acceptErr != nil {
		return p.acceptErr
	}
	return net.ErrClosed
}

// generation returns a net.Listener view for one run() lifetime. Closing it —
// which http.Server.Shutdown does — detaches the view without touching the
// socket; the next generation picks up exactly where this one stopped.
func (p *persistentListener) generation() net.Listener {
	return &generationListener{p: p, done: make(chan struct{})}
}

type generationListener struct {
	p         *persistentListener
	closeOnce sync.Once
	done      chan struct{}
}

func (g *generationListener) Accept() (net.Conn, error) {
	select {
	case <-g.done:
		return nil, net.ErrClosed
	default:
	}
	select {
	case <-g.done:
		return nil, net.ErrClosed
	case conn := <-g.p.conns:
		return conn, nil
	case <-g.p.pumpDone:
		return nil, g.p.takeErr()
	}
}

func (g *generationListener) Close() error {
	g.closeOnce.Do(func() { close(g.done) })
	return nil
}

func (g *generationListener) Addr() net.Addr { return g.p.Addr() }
