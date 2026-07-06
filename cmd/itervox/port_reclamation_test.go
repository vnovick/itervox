package main

import (
	"net"
	"strconv"
	"testing"
	"time"
)

// TestListenWithFallbackReleasesPort verifies the contract that backs the
// release-checklist gate "port reclamation on clean quit": when listenWithFallback
// returns a listener and the caller closes it, the OS releases the port within
// the SO_REUSEADDR / TIME_WAIT window so the next launch can rebind without
// drifting to port+1.
//
// The integration end-to-end variant (boot a full daemon, send SIGINT, observe
// graceful shutdown, re-bind the same port) is intentionally heavier; this test
// pins the invariant the checklist actually depends on, which is that the
// listener's Close() returns the port to the OS pool.
func TestListenWithFallbackReleasesPort(t *testing.T) {
	// Pick a free port via :0, then immediately release it so we know it was
	// available. listenWithFallback will rebind the same port in the second step.
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("probe listen: %v", err)
	}
	addr := probe.Addr().(*net.TCPAddr)
	port := addr.Port
	_ = probe.Close()

	// First bind: simulate the daemon's startup port acquisition.
	ln, gotAddr, err := listenWithFallback("127.0.0.1", port, 0)
	if err != nil {
		t.Fatalf("first bind on port %d: %v", port, err)
	}
	expectedAddr := "127.0.0.1:" + strconv.Itoa(port)
	if gotAddr != expectedAddr {
		t.Fatalf("first bind addr = %q, want %q", gotAddr, expectedAddr)
	}

	// Simulate clean shutdown.
	if err := ln.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}

	// Second bind: must succeed on the SAME port within 1s. Poll because
	// the OS may briefly hold the port in TIME_WAIT before releasing.
	deadline := time.Now().Add(1 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		ln2, gotAddr2, err := listenWithFallback("127.0.0.1", port, 0)
		if err == nil {
			if gotAddr2 != expectedAddr {
				_ = ln2.Close()
				t.Fatalf("rebind addr = %q, want %q (fell through to next port — port not reclaimed)", gotAddr2, expectedAddr)
			}
			_ = ln2.Close()
			return
		}
		lastErr = err
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("rebind on port %d did not succeed within 1s: %v", port, lastErr)
}
