package main

import (
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
// never matches: the operator is asking the OS for a fresh port, so a rebind
// is genuinely intended.
func sameBindTarget(currentAddr, host string, port int) bool {
	if currentAddr == "" || port == 0 {
		return false
	}
	current, err := net.ResolveTCPAddr("tcp", currentAddr)
	if err != nil {
		return false
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
const runShutdownGrace = 6 * time.Second

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
