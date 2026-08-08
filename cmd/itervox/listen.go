package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// listenStrict tries to listen on the given host:port exactly. On
// EADDRINUSE it returns an operator-friendly diagnostic instead of silently
// shifting to the next port. Use this when the operator explicitly
// configured a port in WORKFLOW.md — silent shifting would mismatch the
// Vite dev proxy / `.itervox/dashboard_url` / HEARTBEAT contract.
//
// Special case `port == 0`: pass through to net.Listen, which makes the
// kernel pick any free port. The returned addr reflects the actual bound
// port so the dashboard URL and dashboard_url file are accurate. This is
// the recommended setting in WORKFLOW.md for "two repos in parallel"
// workflows.
func listenStrict(host string, port int) (net.Listener, string, error) {
	requested := fmt.Sprintf("%s:%d", host, port)
	ln, err := net.Listen("tcp", requested)
	if err == nil {
		// When port == 0 the actual bound addr differs from requested. Always
		// return the actual addr — callers use it for the dashboard URL.
		return ln, ln.Addr().String(), nil
	}
	if !isAddrInUse(err) {
		return nil, "", fmt.Errorf("http listen %s: %w", requested, err)
	}
	holder := describePortHolder(port)
	return nil, "", fmt.Errorf(
		"itervox: port %d already in use%s. Stop the conflicting process, change `server.port` in WORKFLOW.md, or set `server.port: 0` to let the OS pick a free port",
		port, holder)
}

// describePortHolder runs `lsof -nP -iTCP:<port> -sTCP:LISTEN` so the
// startup error names the process holding the port. Returns "" when lsof is
// absent or returned nothing useful — best-effort. Operators on Linux
// without lsof installed still get the actionable port-in-use message.
func describePortHolder(port int) string {
	cmd := exec.Command("lsof", "-nP", fmt.Sprintf("-iTCP:%d", port), "-sTCP:LISTEN")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		return ""
	}
	// Skip header; first data line is good enough for the operator.
	return " (held by " + strings.Join(strings.Fields(lines[1]), " ") + ")"
}

// listenWithFallback tries to listen on the given host:port. If the port is
// already in use, it tries up to maxPortRetries successive ports. Returns the
// listener and the actual address it bound to.
func listenWithFallback(host string, port, maxPortRetries int) (net.Listener, string, error) {
	for i := 0; i <= maxPortRetries; i++ {
		tryPort := port + i
		addr := fmt.Sprintf("%s:%d", host, tryPort)
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

// serveOnListener starts an HTTP server on an already-bound listener and
// returns a channel that receives its exit error.
func serveOnListener(ctx context.Context, ln net.Listener, addr string, handler http.Handler) <-chan error {
	errCh := make(chan error, 1)

	srv := &http.Server{
		Addr:        addr,
		Handler:     handler,
		ReadTimeout: 5 * time.Second,
		// WriteTimeout is intentionally 0 (no deadline) so the SSE /api/v1/events
		// endpoint can stream indefinitely. Per-route write timeouts should use
		// http.TimeoutHandler for non-SSE handlers if needed in future.
		WriteTimeout: 0,
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()

	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			errCh <- err
		} else {
			errCh <- nil
		}
	}()

	return errCh
}

// isAddrInUse reports whether err indicates the address is already in use.
func isAddrInUse(err error) bool {
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		var sysErr *os.SyscallError
		if errors.As(opErr.Err, &sysErr) {
			return sysErr.Err == syscall.EADDRINUSE
		}
	}
	return false
}
