package server

import (
	"bytes"
	"encoding/json"
	"sync"
	"time"
)

// sse_broadcast.go — the dashboard stream's fan-out (#210): the broadcaster
// that wakes SSE clients on a state change, coalesced to at most
// one delivery per notifyInterval, and the per-version frame cache that lets every client
// write the same encoded snapshot instead of building and marshalling its
// own.

// notifyInterval is the broadcaster's minimum gap between two deliveries:
// at most 4 per second. A change is never held back longer than this — the
// first change after a quiet period is delivered at once (leading edge) and
// a change inside the window is delivered when the window ends (trailing
// edge), so the last change always reaches the clients and an
// input-required or guardrail-approval change waits at most 250 ms.
const notifyInterval = 250 * time.Millisecond

// frameMaxAge bounds how long an encoded frame is reused for its version.
// A version only changes on a delivered notify; this keeps a client that
// connects during a long quiet period from receiving a frame built long ago
// (its generatedAt and elapsed times would be stale).
const frameMaxAge = time.Second

// broadcaster fans out state-change notifications to multiple SSE clients.
//
// Every delivery bumps version; the frame cache keys encoded snapshots by it.
type broadcaster struct {
	mu      sync.Mutex
	clients map[chan struct{}]struct{}
	// interval is the minimum gap between deliveries (notifyInterval;
	// tests and the fan-out benchmark set 0 to deliver every notify).
	interval time.Duration
	version  uint64
	lastFire time.Time
	// trailing is the pending trailing-edge delivery, nil when none.
	trailing *time.Timer
}

func newBroadcaster() *broadcaster {
	return &broadcaster{clients: make(map[chan struct{}]struct{}), interval: notifyInterval}
}

func (b *broadcaster) subscribe() chan struct{} {
	ch := make(chan struct{}, 1)
	b.mu.Lock()
	b.clients[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

func (b *broadcaster) unsubscribe(ch chan struct{}) {
	b.mu.Lock()
	delete(b.clients, ch)
	b.mu.Unlock()
}

// notify records a state change. It is delivered now when the last
// delivery is at least interval ago, otherwise once the interval has
// passed; notifies in between fold into that one delivery, which carries
// the newest state because clients read the snapshot when they wake.
func (b *broadcaster) notify() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.trailing != nil {
		return // a delivery is already scheduled and will carry this change
	}
	if wait := b.interval - time.Since(b.lastFire); wait > 0 && !b.lastFire.IsZero() {
		b.trailing = time.AfterFunc(wait, b.fireTrailing)
		return
	}
	b.fireLocked()
}

func (b *broadcaster) fireTrailing() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.trailing = nil
	b.fireLocked()
}

// fireLocked starts a new version and wakes every client. Caller holds mu.
func (b *broadcaster) fireLocked() {
	b.version++
	b.lastFire = time.Now()
	for ch := range b.clients {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// currentVersion is the number of deliveries so far.
func (b *broadcaster) currentVersion() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.version
}

// close wakes up every subscribed SSE handler so they can exit promptly on
// graceful shutdown. Each subscriber's channel is removed from the clients
// map so a duplicate notify() doesn't double-send, and a pending trailing
// delivery is cancelled. Safe to call multiple times. G-03 (gaps_280426_2).
func (b *broadcaster) close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.trailing != nil {
		b.trailing.Stop()
		b.trailing = nil
	}
	for ch := range b.clients {
		// Send before delete so any already-blocked receiver wakes; the
		// `default` branch covers handlers that have already drained.
		select {
		case ch <- struct{}{}:
		default:
		}
		delete(b.clients, ch)
	}
}

// sseFrameCache holds the encoded "data: <snapshot>\n\n" frame of one
// broadcaster version. The first SSE writer of a version builds and encodes
// the snapshot; every other client of that version writes the same bytes.
// A cached frame is never modified — a new version gets a new slice — so
// handing the slice to many writers shares nothing mutable.
type sseFrameCache struct {
	mu      sync.Mutex
	version uint64
	builtAt time.Time
	frame   []byte
	// encodes counts snapshot encodings (TestSSEClientsShareOneEncodingPerVersion).
	encodes uint64
}

// sseFrame returns the current version's encoded snapshot frame, building
// it when this version has none yet or the cached one is older than
// frameMaxAge. Concurrent callers of a new version wait for the one encode.
func (s *Server) sseFrame() ([]byte, error) {
	version := s.bc.currentVersion()
	c := &s.frames
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.frame != nil && c.version == version && time.Since(c.builtAt) < frameMaxAge {
		return c.frame, nil
	}
	frame, err := encodeSSEFrame(s.snapshot())
	if err != nil {
		return nil, err
	}
	c.version, c.builtAt, c.frame = version, time.Now(), frame
	c.encodes++
	return frame, nil
}

// encodeSSEFrame renders snap as one SSE data frame. json.Encoder escapes
// exactly as json.Marshal does and adds the newline that ends the data
// line; JSON never contains a raw newline, so the payload is one line.
func encodeSSEFrame(snap StateSnapshot) ([]byte, error) {
	var buf bytes.Buffer
	buf.Grow(64 << 10)
	buf.WriteString("data: ")
	if err := json.NewEncoder(&buf).Encode(snap); err != nil {
		return nil, err
	}
	buf.WriteByte('\n')
	return buf.Bytes(), nil
}
