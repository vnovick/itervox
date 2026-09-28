package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"time"

	"github.com/vnovick/itervox/internal/outbox"
	"github.com/vnovick/itervox/internal/tracker"
)

// listenWithFallback tries to listen on the given host:port. If the port is
// already in use, it tries up to maxPortRetries successive ports. Returns the
// listener and the actual address it bound to.
func listenWithFallback(host string, port, maxPortRetries int) (net.Listener, string, error) {
	for i := 0; i <= maxPortRetries; i++ {
		tryPort := port + i
		addr := bindAddr(host, tryPort)
		ln, err := net.Listen("tcp", addr)
		if err == nil {
			if i > 0 {
				slog.Warn("server: configured port in use, using next available",
					"configured_port", port, "actual_port", tryPort)
			}
			return ln, addr, nil
		}
		if !isAddrInUse(err) {
			return nil, "", fmt.Errorf("http listen %s: %w", addr, err)
		}
	}
	return nil, "", fmt.Errorf("ports %d–%d all in use — is another itervox instance running?",
		port, port+maxPortRetries)
}

// runOutboxFlusherTick performs one flusher tick: it delivers every entry
// ob.Due(now) returns, sequentially, stopping early if ctx is cancelled
// mid-tick. Extracted from startOutboxFlusher's goroutine body so tests can
// drive a single tick directly with an injected `now` (and an injected
// ob.SetNow clock for backoff assertions) instead of waiting on a real
// ticker — same convention as cmd/itervox/deps_auto_analyze.go's
// runDepsAutoAnalyzeTick.
//
// Delegates to runOutboxFlusherTickGated with adapter "" — a key no gate is
// ever recorded under, so the per-entry gate check never fires. Production
// goes through runOutboxFlusherTickForAdapter with the real adapter key.
//
// Sequential, not concurrent: ob.Due already returns at most one entry per
// issue (the FIFO head), so cross-issue delivery could in principle run in
// parallel, but the spec is explicit ("for each entry (sequentially...)")
// and a single in-flight tracker call at a time keeps flusher behavior easy
// to reason about and trivially serializes with any other tracker caller.
func runOutboxFlusherTick(ctx context.Context, ob *outbox.Outbox, tr tracker.Tracker, orch outboxRefresher, now time.Time) {
	runOutboxFlusherTickGated(ctx, ob, tr, orch, now, "")
}
