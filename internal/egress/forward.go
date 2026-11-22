package egress

import (
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

// The in-sandbox forwarder (bridge mode). The sandbox has its own network
// namespace with nothing but a loopback interface, so the agent cannot
// open a connection to anything outside it. The daemon's proxy and relays
// listen on Unix sockets in the run directory the sandbox binds; the
// forwarder, started first inside the sandbox, listens on the sandbox's
// 127.0.0.1 at each Bridge port and pipes every connection to its socket.
// The agent's HTTPS_PROXY names the proxy port, so the only way out is the
// daemon's allowlist.
//
// Layout: `itervox __itervox-egress-forward --listen PORT=SOCKET… -- argv…`
// starts a copy of itself with --serve (the listeners), waits until they
// listen, and then replaces itself with argv (exec), so the agent keeps the
// forwarder's pid, file descriptors (the prompt on fd 3), process group and
// exit status. The serving copy lives until the sandbox's pid namespace
// ends with the agent.

// ForwardEntrypoint is the argv[1] that selects the forwarder.
const ForwardEntrypoint = "__itervox-egress-forward"

// serveFlag selects the serving copy.
const serveFlag = "--serve"

// readyTimeout bounds the wait for the serving copy's listeners.
const readyTimeout = 10 * time.Second

// IsEntrypoint reports whether arg selects the forwarder.
func IsEntrypoint(arg string) bool { return arg == ForwardEntrypoint }

// ForwarderArgv returns argv run behind the forwarder exe with bridges.
func ForwarderArgv(exe string, bridges []Bridge, argv []string) []string {
	out := []string{exe, ForwardEntrypoint}
	for _, b := range bridges {
		out = append(out, "--listen", strconv.Itoa(b.Port)+"="+b.Socket)
	}
	out = append(out, "--")
	return append(out, argv...)
}

// parseForwardArgs parses the arguments after the entrypoint.
func parseForwardArgs(args []string) (bridges []Bridge, serve bool, argv []string, err error) {
	for i := 0; i < len(args); i++ {
		switch a := args[i]; a {
		case serveFlag:
			serve = true
		case "--listen":
			if i+1 >= len(args) {
				return nil, false, nil, errors.New("--listen needs PORT=SOCKET")
			}
			i++
			portStr, sock, ok := strings.Cut(args[i], "=")
			port, perr := strconv.Atoi(portStr)
			if !ok || perr != nil || port < 1 || port > 65535 || sock == "" {
				return nil, false, nil, fmt.Errorf("bad --listen %q", args[i])
			}
			bridges = append(bridges, Bridge{Port: port, Socket: sock})
		case "--":
			return bridges, serve, args[i+1:], nil
		default:
			return nil, false, nil, fmt.Errorf("unexpected argument %q", a)
		}
	}
	return bridges, serve, nil, nil
}

// serveBridges listens on 127.0.0.1 at every bridge port, then calls ready
// and pipes connections to their sockets until a listener fails.
func serveBridges(bridges []Bridge, ready func()) error {
	var ls []net.Listener
	for _, b := range bridges {
		l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(b.Port)))
		if err != nil {
			for _, l := range ls {
				_ = l.Close()
			}
			return err
		}
		ls = append(ls, l)
	}
	ready()
	errs := make(chan error, len(ls))
	for i, l := range ls {
		sock := bridges[i].Socket
		go func() {
			defer recoverEgressGoroutine("forwarder listener")
			errs <- acceptAndPipe(l, sock)
		}()
	}
	return <-errs
}

func acceptAndPipe(l net.Listener, sock string) error {
	for {
		c, err := l.Accept()
		if err != nil {
			return err
		}
		go func() {
			defer recoverEgressGoroutine("forwarder connection")
			defer func() { _ = c.Close() }()
			up, err := net.DialTimeout("unix", sock, dialTimeout)
			if err != nil {
				return
			}
			defer func() { _ = up.Close() }()
			pipe(c, up, nil)
		}()
	}
}

func usage(stderr io.Writer, err error) int {
	_, _ = fmt.Fprintf(stderr, "itervox egress forwarder: %v\n", err)
	return 126
}
