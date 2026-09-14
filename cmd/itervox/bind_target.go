package main

import (
	"log/slog"
	"net"
	"strconv"
	"time"
)

// sameBindTarget reports whether (host, port) would land on the socket the
// daemon has already bound at currentAddr.
//
// The reload path used to compare the literal "host:port" string against the
// previous one. That is not socket identity: `server.host: 127.0.0.1` and
// `server.host: localhost` are different strings and the same socket, so a
// purely cosmetic WORKFLOW.md edit looked like a rebind. The daemon then
// bound the new listener BEFORE closing the old one, hit EADDRINUSE against
// itself, and called fatalExit — which, being os.Exit, skipped the deferred
// removal of daemon.pid / dashboard_url / HEARTBEAT.md. Those are exactly
// the files `itervox doctor` and `itervox status` read to decide the daemon
// is alive, so it died and left evidence saying otherwise.
//
// Resolving both sides collapses the aliases. An explicit `server.port: 0`
// is handled below: the OS already picked a port on the first bind, so the
// held socket still satisfies the request and is kept.
func sameBindTarget(currentAddr, host string, port int) bool {
	if currentAddr == "" {
		return false
	}
	current, err := net.ResolveTCPAddr("tcp", currentAddr)
	if err != nil {
		return false
	}
	// `server.port: 0` means "any free port", and the OS already picked one
	// the first time — so the socket we hold still satisfies the request and
	// must be kept.
	//
	// Treating 0 as never-matching re-rolled the port on EVERY reload, which
	// is the exact symptom of issue #44 that this whole path exists to fix,
	// reintroduced for precisely the configuration the upgrade notes
	// recommend for running several daemons at once. The literal-string
	// comparison this predicate replaced got this case right by accident:
	// "127.0.0.1:0" equalled "127.0.0.1:0".
	//
	// Only the host is compared in that case; a genuine first bind is
	// already handled by the `srvPersist == nil` check at the call site.
	if port == 0 {
		port = current.Port
	}
	desired, err := net.ResolveTCPAddr("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return false
	}
	if current.Port != desired.Port {
		return false
	}
	return current.IP.Equal(desired.IP)
}

// runShutdownGrace bounds how long run() waits for the other half of the
// daemon (HTTP server or orchestrator) to stop before giving up and
// returning anyway.
//
// It MUST exceed the longest bounded wait inside orch.Run's shutdown, which
// ends by draining autoClearWg, discardWg, commentWg and depsRefreshWg.
// Each of the two worst paths is a tracker call followed by an event send,
// SEQUENTIALLY in the same goroutine, so the bounds add:
//
//   - commentWg's input-required comment (event_loop.go): CreateComment
//     bounded by postRunTimeout (60s, worker.go) THEN a 30s send  = 90s.
//   - asyncDiscardAndTransitionTo (event_loop.go): the caller passes a 15s
//     ctx, but directWriteSink.UpdateIssueState (write_sink.go) builds its
//     OWN context.Background()+postRunTimeout call ctx and only checks the
//     caller's ctx between attempts — so attempt 1 runs up to 60s, then a
//     30s send                                                    = 90s.
//
// 105s clears that 90s worst case with headroom. Two earlier values were
// wrong and both failed the same way: 6s was shorter than every one of these
// waits, and 75s counted the trailing 30s send for the discard path while
// omitting it for the comment path that has the identical shape.
//
// Getting this too low is not a slow shutdown — it is silent corruption.
// awaitStop LOGS and returns anyway on timeout, so run() returns with the
// orchestrator still live and main()'s reload loop opens a SECOND
// outbox.New on the same .itervox/outbox.json; each persist is a whole-file
// rewrite from one handle's memory.
const runShutdownGrace = 105 * time.Second

// awaitStop waits for done to fire, up to grace. It reports whether the
// component stopped in time.
//
// Both exit paths in run() must use it. The orchestrator-exits-first path
// always did; the server-exits-first path returned immediately, and on a
// config reload that is the path actually taken — cancelling runCtx shuts the
// HTTP generation down with a channel close, while orch.Run is still draining
// WaitGroups whose goroutines hold context.Background(). run() therefore
// returned while the orchestrator was still live, and main()'s reload loop
// immediately built a SECOND orchestrator and a second outbox.New on the same
// .itervox/outbox.json. Both handles rewrite the whole file on every
// persist, so one silently erases the other's durably-recorded entry or
// resurrects one it had already delivered — in the component whose entire
// purpose is not losing tracker writes.
func awaitStop(done <-chan error, grace time.Duration) bool {
	select {
	case <-done:
		return true
	case <-time.After(grace):
		return false
	}
}

// awaitOutboxFlusher joins the outbox flusher before run() returns.
//
// The flusher and its absent-reconcile children write .itervox/outbox.json,
// and main()'s reload loop opens a SECOND outbox handle on that path as soon
// as run() returns. Every persist is a whole-file rewrite from one handle's
// in-memory list, so a late write from the outgoing generation either erases
// an entry the incoming one durably enqueued — breaking Enqueue's "durable
// iff nil error" contract — or resurrects one it had already dropped and
// re-delivers it to the tracker. run() already joined the orchestrator for
// exactly this reason; the flusher is the other writer of the same file.
func awaitOutboxFlusher(done <-chan struct{}) {
	select {
	case <-done:
	case <-time.After(runShutdownGrace):
		slog.Warn("run: outbox flusher did not stop within the shutdown grace; "+
			"a reload now would run two outbox handles against one file",
			"grace", runShutdownGrace)
	}
}
