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
	"strconv"
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
	requested := bindAddr(host, port)
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
	desc, _ := describePortHolderWithPID(port)
	return desc
}

// describePortHolderWithPID is describePortHolder plus the holder's PID, so
// callers can tell "some other process has this port" from "our own daemon
// has it". Returns pid 0 when the PID column cannot be parsed.
func describePortHolderWithPID(port int) (desc string, pid int) {
	cmd := exec.Command("lsof", "-nP", fmt.Sprintf("-iTCP:%d", port), "-sTCP:LISTEN")
	out, err := cmd.Output()
	if err != nil {
		return "", 0
	}
	return parseLsofHolder(string(out))
}

// parseLsofHolder extracts the operator-facing description and the PID from
// `lsof -nP -iTCP:<port> -sTCP:LISTEN` output. Split out from the exec so the
// parsing is testable without a live socket.
//
// lsof's data columns are COMMAND PID USER ... — the second field is the PID.
func parseLsofHolder(out string) (desc string, pid int) {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		return "", 0
	}
	// Skip header; first data line is good enough for the operator.
	fields := strings.Fields(lines[1])
	if len(fields) >= 2 {
		if parsed, convErr := strconv.Atoi(fields[1]); convErr == nil {
			pid = parsed
		}
	}
	return " (held by " + strings.Join(fields, " ") + ")", pid
}

// shutdownTimeout bounds how long a generation's http.Server.Shutdown waits
// for in-flight requests to drain before its connections are force-closed.
const shutdownTimeout = 5 * time.Second

// serveOnListener starts an HTTP server on an already-bound listener and
// returns a channel that receives its exit error.
//
// When ctx ends (a config reload or daemon exit) the server is shut down in
// three steps (CORE-025):
//  1. closeStreams (the dashboard's (*server.Server).Shutdown) runs via
//     RegisterOnShutdown and ends every SSE stream at once. It is passed
//     explicitly rather than discovered by type-asserting handler: an
//     assertion only matches an unwrapped *server.Server, so any middleware
//     around it silently dropped the closer and every stream ran to the
//     shutdown deadline. nil means the handler has no streams to close.
//     http.Server.Shutdown by itself neither cancels request contexts nor
//     wakes a stream parked between events, so without this a reload left
//     each open dashboard tab attached to the old generation — a frozen
//     snapshot plus keepalives — and kept that generation alive. Clients
//     reconnect on the clean close and reach the new generation.
//  2. http.Server.Shutdown stops accepting and lets ordinary in-flight
//     requests finish (they are deliberately NOT cancelled: no BaseContext
//     tied to ctx), for up to shutdownTimeout.
//  3. If connections are still active at the deadline, srv.Close()
//     force-closes them, so no handler outlives its generation for long.
func serveOnListener(ctx context.Context, ln net.Listener, addr string, handler http.Handler, closeStreams func()) <-chan error {
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

	if closeStreams != nil {
		srv.RegisterOnShutdown(closeStreams)
	}

	go func() {
		defer failFastOnPanic("http-shutdown")
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(shutCtx); err != nil {
			slog.Warn("http server: requests still active at the shutdown deadline; closing their connections",
				"addr", addr, "timeout", shutdownTimeout, "error", err)
			_ = srv.Close()
		}
	}()

	go func() {
		defer failFastOnPanic("http-serve")
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

// bindAddr builds the listen address for host and port with
// net.JoinHostPort, so an IPv6 literal is bracketed (M4-close D6: "%s:%d"
// turned ITERVOX_SERVER_HOST=:: into ":::8090", "too many colons"). A host
// that is already bracketed ("[::1]") is unwrapped first.
func bindAddr(host string, port int) string {
	return net.JoinHostPort(unbracketHost(host), strconv.Itoa(port))
}

// dashboardBaseURL is the dashboard URL for a configured host and port.
func dashboardBaseURL(host string, port int) string {
	return "http://" + bindAddr(host, port) + "/"
}

func unbracketHost(h string) string {
	if len(h) >= 2 && h[0] == '[' && h[len(h)-1] == ']' {
		return h[1 : len(h)-1]
	}
	return h
}
