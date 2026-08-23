package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// persistentListener — the shared HTTP socket that survives config reloads.
//
// The contract under test is the reload sequence main() performs: serve a
// generation, http.Server.Shutdown it (which Closes the generation), then
// serve a NEW generation on the same persistentListener. The socket must stay
// bound and the port must not change — that is the whole point of issue #44.
// ---------------------------------------------------------------------------

func newTestPersistentListener(t *testing.T) *persistentListener {
	t.Helper()
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	p := newPersistentListener(raw)
	t.Cleanup(func() { _ = p.Close() })
	return p
}

// serveGeneration starts an http.Server on a fresh generation and returns the
// server plus its Serve-exit channel.
func serveGeneration(p *persistentListener, body string) (*http.Server, <-chan error) {
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, body)
	})}
	done := make(chan error, 1)
	gen := p.generation()
	go func() { done <- srv.Serve(gen) }()
	return srv, done
}

func get(t *testing.T, addr string) (string, error) {
	t.Helper()
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://" + addr + "/")
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	return string(b), err
}

// The reload sequence: gen1 serves, is shut down, gen2 serves — same socket,
// same port, and requests reach whichever generation is current.
func TestPersistentListenerSurvivesServerShutdown(t *testing.T) {
	p := newTestPersistentListener(t)
	addr := p.Addr().String()

	srv1, done1 := serveGeneration(p, "generation-1")
	body, err := get(t, addr)
	require.NoError(t, err)
	require.Equal(t, "generation-1", body)

	// http.Server.Shutdown closes the generation listener — exactly what a
	// config reload does via serveOnListener's ctx-driven shutdown.
	shutCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NoError(t, srv1.Shutdown(shutCtx))
	select {
	case err := <-done1:
		require.ErrorIs(t, err, http.ErrServerClosed)
	case <-time.After(2 * time.Second):
		t.Fatal("generation-1 Serve did not return after Shutdown")
	}

	// The socket must still be bound at the SAME address for the next run.
	srv2, _ := serveGeneration(p, "generation-2")
	defer func() { _ = srv2.Close() }()
	body, err = get(t, addr)
	require.NoError(t, err)
	assert.Equal(t, "generation-2", body, "the new generation must serve on the unchanged address")
	assert.Equal(t, addr, p.Addr().String(), "the bound address must not change across generations")
}

// A connection arriving in the reload gap — after the old generation closed,
// before the new one started — must be served by the next generation rather
// than refused. The kernel keeps it in the socket's accept queue.
func TestPersistentListenerConnDuringGapServedByNextGeneration(t *testing.T) {
	p := newTestPersistentListener(t)
	addr := p.Addr().String()

	srv1, done1 := serveGeneration(p, "generation-1")
	shutCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NoError(t, srv1.Shutdown(shutCtx))
	<-done1

	// No generation is serving now. Fire the request anyway.
	type result struct {
		body string
		err  error
	}
	resCh := make(chan result, 1)
	go func() {
		body, err := get(t, addr)
		resCh <- result{body, err}
	}()

	// Give the request time to be dialed into the gap before serving resumes.
	time.Sleep(200 * time.Millisecond)
	srv2, _ := serveGeneration(p, "generation-2")
	defer func() { _ = srv2.Close() }()

	select {
	case res := <-resCh:
		require.NoError(t, res.err, "a request landing in the reload gap must not be refused")
		assert.Equal(t, "generation-2", res.body)
	case <-time.After(3 * time.Second):
		t.Fatal("gap request never completed")
	}
}

// Closing a generation must NOT close the socket or disturb other accepts —
// it only detaches that generation's Accept.
func TestGenerationCloseLeavesSocketBound(t *testing.T) {
	p := newTestPersistentListener(t)

	gen := p.generation()
	require.NoError(t, gen.Close())
	_, err := gen.Accept()
	require.ErrorIs(t, err, net.ErrClosed)

	// A dial still succeeds: the socket is bound and the kernel accepts.
	conn, err := net.DialTimeout("tcp", p.Addr().String(), 2*time.Second)
	require.NoError(t, err, "the socket must remain bound after a generation closes")
	_ = conn.Close()
}

// Closing the persistentListener itself — host/port change or process exit —
// terminates serving for real: a blocked generation Accept returns.
func TestPersistentListenerCloseTerminatesGenerations(t *testing.T) {
	p := newTestPersistentListener(t)
	gen := p.generation()

	acceptErr := make(chan error, 1)
	go func() {
		_, err := gen.Accept()
		acceptErr <- err
	}()

	time.Sleep(50 * time.Millisecond) // let Accept block
	require.NoError(t, p.Close())

	select {
	case err := <-acceptErr:
		require.Error(t, err, "Accept must return once the socket is closed")
	case <-time.After(2 * time.Second):
		t.Fatal("generation Accept did not return after persistentListener.Close")
	}
}
