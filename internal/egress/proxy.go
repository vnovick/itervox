package egress

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/vnovick/itervox/internal/logging"
	"github.com/vnovick/itervox/internal/metrics"
)

// Decision is one connection the proxy allowed or refused. It holds the
// destination host and port only: never a path, query, header or body, so
// a URL carrying a token never reaches a log or the feed.
type Decision struct {
	Host    string
	Port    int
	Allowed bool
	// Reason is "" when allowed, else one of the Reason* constants.
	Reason string
	// Method is "CONNECT" for a tunnel, else the plain-HTTP method.
	Method string
}

// Target is host:port, for messages.
func (d Decision) Target() string { return net.JoinHostPort(d.Host, strconv.Itoa(d.Port)) }

// DialFunc opens the upstream connection for an allowed destination.
type DialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// dialTimeout bounds one upstream connect (name resolution included).
const dialTimeout = 15 * time.Second

// Proxy is an HTTP proxy that tunnels CONNECT and forwards absolute-form
// plain-HTTP requests to the destinations its Allowlist allows, and answers
// 403 to everything else. Every decision is logged (host and port only),
// counted (metrics.EgressDecision) and handed to OnDecision.
type Proxy struct {
	Allow *Allowlist
	// Dial opens upstream connections; nil uses SafeDial, which refuses
	// destinations that resolve to local addresses. Tests inject a dialer
	// that maps test host names to an httptest server.
	Dial DialFunc
	// OnDecision, when set, is called for every decision on the proxy's
	// connection goroutine. It must not block.
	OnDecision func(Decision)
	// Label names the run in log lines (issue identifier).
	Label string

	srv       *http.Server
	transport *http.Transport
	mu        sync.Mutex
	tunnels   map[net.Conn]struct{}
	closed    bool
}

// Serve accepts proxy connections on l until Close. It returns nil after
// Close.
func (p *Proxy) Serve(l net.Listener) error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		_ = l.Close()
		return nil
	}
	if p.srv == nil {
		p.srv = &http.Server{Handler: p, ReadHeaderTimeout: 30 * time.Second}
		p.transport = &http.Transport{
			Proxy:                 nil,
			DialContext:           p.dial,
			ResponseHeaderTimeout: 5 * time.Minute,
			MaxIdleConnsPerHost:   4,
		}
	}
	srv := p.srv
	p.mu.Unlock()
	err := srv.Serve(l)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Close stops the proxy and every open tunnel.
func (p *Proxy) Close() error {
	p.mu.Lock()
	p.closed = true
	srv, tunnels, transport := p.srv, p.tunnels, p.transport
	p.tunnels = map[net.Conn]struct{}{}
	p.mu.Unlock()
	for c := range tunnels {
		_ = c.Close()
	}
	if transport != nil {
		transport.CloseIdleConnections()
	}
	if srv == nil {
		return nil
	}
	return srv.Close()
}

func (p *Proxy) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	if p.Dial != nil {
		return p.Dial(ctx, network, addr)
	}
	return SafeDial(ctx, network, addr)
}

// errLocalAddress is SafeDial's refusal of a local destination.
var errLocalAddress = errors.New("egress: destination resolves to a local address")

// SafeDial resolves addr's host and connects to its first address that is
// not loopback, link-local, unspecified or multicast; a host with no other
// address is refused (errLocalAddress). Dialing the resolved address, not
// the name, keeps a DNS answer that changes between check and connect from
// reaching a local service.
func SafeDial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	var d net.Dialer
	lastErr := errLocalAddress
	for _, ip := range ips {
		if localAddress(ip.IP) {
			continue
		}
		c, err := d.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
		if err == nil {
			return c, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// localAddress reports whether ip is one the proxy never connects to.
func localAddress(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() || ip.IsMulticast() || ip.IsInterfaceLocalMulticast()
}

// decide applies the allowlist and records the decision.
func (p *Proxy) decide(method, host string, port int) Decision {
	d := Decision{Host: normalizeHost(host), Port: port, Method: method}
	d.Allowed, d.Reason = p.Allow.Allows(host, port)
	p.record(d)
	return d
}

// record logs, counts and reports d.
func (p *Proxy) record(d Decision) {
	host := logging.RedactString(d.Host)
	if d.Allowed {
		metrics.EgressDecision(metrics.EgressAllowed)
		slog.Info("egress: allowed", "run", p.Label, "host", host, "port", d.Port, "method", d.Method)
	} else {
		metrics.EgressDecision(metrics.EgressRefused)
		slog.Warn("egress: refused", "run", p.Label, "host", host, "port", d.Port, "method", d.Method, "reason", d.Reason)
	}
	if p.OnDecision != nil {
		d.Host = host
		p.OnDecision(d)
	}
}

// ServeHTTP handles one proxy request.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		p.serveConnect(w, r)
		return
	}
	p.serveForward(w, r)
}

// refuse answers a refused request. The body names the host so the agent
// (and the operator reading its output) sees why.
func refuse(w http.ResponseWriter, d Decision) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Itervox-Egress", "refused")
	w.WriteHeader(http.StatusForbidden)
	_, _ = fmt.Fprintf(w, "itervox egress: %s refused (%s); add it to agent.egress_allow to allow it\n", d.Target(), d.Reason)
}

func splitTarget(hostport string, defaultPort int) (string, int, bool) {
	host, portStr, err := net.SplitHostPort(hostport)
	if err != nil {
		if defaultPort == 0 {
			return "", 0, false
		}
		host, portStr = hostport, strconv.Itoa(defaultPort)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || host == "" {
		return "", 0, false
	}
	return host, port, true
}

func (p *Proxy) serveConnect(w http.ResponseWriter, r *http.Request) {
	host, port, ok := splitTarget(r.Host, 0)
	if !ok {
		d := Decision{Host: "(none)", Method: http.MethodConnect, Reason: ReasonBadRequest}
		p.record(d)
		refuse(w, d)
		return
	}
	d := p.decide(http.MethodConnect, host, port)
	if !d.Allowed {
		refuse(w, d)
		return
	}
	upstream, err := p.dial(r.Context(), "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		p.refuseAfterDial(w, d, err)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		_ = upstream.Close()
		http.Error(w, "itervox egress: tunnel unsupported", http.StatusInternalServerError)
		return
	}
	client, buf, err := hj.Hijack()
	if err != nil {
		_ = upstream.Close()
		return
	}
	if !p.track(client, upstream) {
		_ = client.Close()
		_ = upstream.Close()
		return
	}
	if _, err := client.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n")); err != nil {
		p.untrack(client, upstream)
		return
	}
	pipe(client, upstream, buf.Reader)
	p.untrack(client, upstream)
}

// refuseAfterDial answers an allowed destination that could not be
// reached; a destination SafeDial refused as local is a refusal like any
// other and is recorded as one.
func (p *Proxy) refuseAfterDial(w http.ResponseWriter, d Decision, err error) {
	if errors.Is(err, errLocalAddress) {
		d.Allowed, d.Reason = false, ReasonLocalAddress
		p.record(d)
		refuse(w, d)
		return
	}
	slog.Warn("egress: upstream unreachable", "run", p.Label, "host", logging.RedactString(d.Host), "port", d.Port,
		"error", logging.RedactString(err.Error()))
	w.Header().Set("X-Itervox-Egress", "unreachable")
	http.Error(w, "itervox egress: "+d.Target()+" unreachable", http.StatusBadGateway)
}

func (p *Proxy) track(conns ...net.Conn) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return false
	}
	if p.tunnels == nil {
		p.tunnels = map[net.Conn]struct{}{}
	}
	for _, c := range conns {
		p.tunnels[c] = struct{}{}
	}
	return true
}

func (p *Proxy) untrack(conns ...net.Conn) {
	p.mu.Lock()
	for _, c := range conns {
		delete(p.tunnels, c)
	}
	p.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
}

// hopHeaders are removed before a plain-HTTP request is forwarded.
var hopHeaders = []string{"Proxy-Authorization", "Proxy-Connection", "Proxy-Authenticate", "Connection",
	"Keep-Alive", "Te", "Trailer", "Transfer-Encoding", "Upgrade"}

func (p *Proxy) serveForward(w http.ResponseWriter, r *http.Request) {
	if !r.URL.IsAbs() || r.URL.Scheme != "http" {
		d := Decision{Host: "(none)", Method: r.Method, Reason: ReasonBadRequest}
		if r.URL.IsAbs() {
			d.Host = r.URL.Hostname()
		}
		p.record(d)
		refuse(w, d)
		return
	}
	host, port, ok := splitTarget(r.URL.Host, 80)
	if !ok {
		d := Decision{Host: "(none)", Method: r.Method, Reason: ReasonBadRequest}
		p.record(d)
		refuse(w, d)
		return
	}
	d := p.decide(r.Method, host, port)
	if !d.Allowed {
		refuse(w, d)
		return
	}
	out := r.Clone(r.Context())
	out.RequestURI = ""
	for _, h := range hopHeaders {
		out.Header.Del(h)
	}
	resp, err := p.transport.RoundTrip(out)
	if err != nil {
		p.refuseAfterDial(w, d, err)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	for _, h := range hopHeaders {
		resp.Header.Del(h)
	}
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// pipe copies both ways between a and b until either side ends; pending
// holds bytes the HTTP server already buffered from a.
func pipe(a, b net.Conn, pending *bufio.Reader) {
	done := make(chan struct{}, 2)
	go func() {
		defer recoverEgressGoroutine("tunnel upstream copy")
		defer func() { done <- struct{}{} }()
		var src io.Reader = a
		if pending != nil && pending.Buffered() > 0 {
			src = io.MultiReader(io.LimitReader(pending, int64(pending.Buffered())), a)
		}
		_, _ = io.Copy(b, src)
		closeWrite(b)
	}()
	go func() {
		defer recoverEgressGoroutine("tunnel downstream copy")
		defer func() { done <- struct{}{} }()
		_, _ = io.Copy(a, b)
		closeWrite(a)
	}()
	<-done
	<-done
}

// closeWrite half-closes c when it supports it, so the peer sees EOF.
func closeWrite(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
		return
	}
	_ = c.Close()
}

// recoverEgressGoroutine ends a panicking egress goroutine (one tunnel or
// relay connection) without taking the daemon down: it logs the panic and
// counts it. egress must not import orchestrator, so it has its own
// recover, accepted by `make no-bare-go`.
func recoverEgressGoroutine(site string) {
	if r := recover(); r != nil {
		metrics.GoroutinePanic()
		slog.Error("egress: goroutine panic recovered", "site", site, "panic", fmt.Sprint(r))
	}
}
