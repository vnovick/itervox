package hub

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"
)

// The pass-through's frame relay (terminal.go). The hub parses every frame
// (RFC 6455 framing, no extensions are negotiated) so that it can:
//   - drop every browser frame except a resize (op 0x01 + cols + rows):
//     typed input never leaves the hub, even though the member's read-only
//     attach would drop it too;
//   - write its own 4403 close frame between two member frames, never
//     inside one, when the caller's hub token loses access;
//   - relay the member's close frame with its code, prefixing a 4403
//     reason with "member_" (the project ended the hub's access, not the
//     person's token).

// WebSocket opcodes.
const (
	wsOpContinuation byte = 0x0
	wsOpBinary       byte = 0x2
	wsOpClose        byte = 0x8
	wsOpPing         byte = 0x9
	wsOpPong         byte = 0xA
)

// Relay limits and codes.
const (
	// maxBrowserFrame bounds a browser frame (the member's own bound).
	maxBrowserFrame = 64 << 10
	// maxMemberFrame bounds a member frame (the member sends ≤ 32 KiB).
	maxMemberFrame = 1 << 20
	// terminalCloseAccessLost is internal/server's close code of a terminal
	// whose caller lost access.
	terminalCloseAccessLost = 4403
	// terminalOpResize is the browser's resize op (internal/server).
	terminalOpResize byte = 0x01
)

var errBadFrame = errors.New("hub: malformed WebSocket frame")

// wsFrame is one frame: raw is the bytes as received (header, mask key and
// still-masked payload), payload the unmasked payload.
type wsFrame struct {
	fin     bool
	opcode  byte
	raw     []byte
	payload []byte
}

// readWSFrame reads one frame from r. masked says whether the peer must
// mask (a browser) or must not (the member); limit bounds the payload.
func readWSFrame(r *bufio.Reader, masked bool, limit int) (wsFrame, error) {
	var hdr [2]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return wsFrame{}, err
	}
	f := wsFrame{fin: hdr[0]&0x80 != 0, opcode: hdr[0] & 0x0f}
	if hdr[0]&0x70 != 0 || (hdr[1]&0x80 != 0) != masked {
		return wsFrame{}, errBadFrame // reserved bits, or the wrong masking
	}
	raw := append(make([]byte, 0, 14), hdr[:]...)
	n := uint64(hdr[1] & 0x7f)
	switch n {
	case 126:
		var b [2]byte
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return wsFrame{}, err
		}
		raw = append(raw, b[:]...)
		n = uint64(binary.BigEndian.Uint16(b[:]))
	case 127:
		var b [8]byte
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return wsFrame{}, err
		}
		raw = append(raw, b[:]...)
		n = binary.BigEndian.Uint64(b[:])
	}
	if n > uint64(limit) || (f.opcode >= wsOpClose && n > 125) {
		return wsFrame{}, errBadFrame
	}
	var mask [4]byte
	if masked {
		if _, err := io.ReadFull(r, mask[:]); err != nil {
			return wsFrame{}, err
		}
		raw = append(raw, mask[:]...)
	}
	start := len(raw)
	raw = append(raw, make([]byte, n)...)
	if _, err := io.ReadFull(r, raw[start:]); err != nil {
		return wsFrame{}, err
	}
	f.raw = raw
	f.payload = append([]byte(nil), raw[start:]...)
	if masked {
		for i := range f.payload {
			f.payload[i] ^= mask[i%4]
		}
	}
	return f, nil
}

// closeFrame is an unmasked (server → browser) close frame.
func closeFrame(code uint16, reason string) []byte {
	if len(reason) > 123 {
		reason = reason[:123]
	}
	b := []byte{0x80 | wsOpClose, byte(2 + len(reason)), 0, 0}
	binary.BigEndian.PutUint16(b[2:], code)
	return append(b, reason...)
}

// terminalRelay is one open pass-through.
type terminalRelay struct {
	browser  net.Conn
	browserR *bufio.Reader
	member   net.Conn
	memberR  *bufio.Reader

	wmu  sync.Mutex // browser writes: whole frames only
	done chan struct{}
	once sync.Once
}

func newTerminalRelay(browser net.Conn, br *bufio.Reader, member net.Conn, mr *bufio.Reader) *terminalRelay {
	return &terminalRelay{browser: browser, browserR: br, member: member, memberR: mr, done: make(chan struct{})}
}

// end closes both sides once.
func (rl *terminalRelay) end() {
	rl.once.Do(func() {
		close(rl.done)
		_ = rl.browser.Close()
		_ = rl.member.Close()
	})
}

// toBrowser writes one whole frame to the browser.
func (rl *terminalRelay) toBrowser(frame []byte) error {
	rl.wmu.Lock()
	defer rl.wmu.Unlock()
	select {
	case <-rl.done:
		return net.ErrClosed
	default:
	}
	_ = rl.browser.SetWriteDeadline(time.Now().Add(10 * time.Second))
	_, err := rl.browser.Write(frame)
	return err
}

// revoke closes the browser's socket with 4403 and reason, then ends.
func (rl *terminalRelay) revoke(reason string) {
	_ = rl.toBrowser(closeFrame(terminalCloseAccessLost, reason))
	rl.end()
}

// run relays until either side ends, the hub stops, ctx ends or the
// caller's hub token loses access.
func (rl *terminalRelay) run(ctx context.Context, h *Hub, t hubTerminalTicket) {
	defer rl.end()
	go func() {
		defer rl.end()
		defer recoverHubGoroutine("hub-terminal-member")
		rl.memberToBrowser()
	}()
	go func() {
		defer rl.end()
		defer recoverHubGoroutine("hub-terminal-access")
		h.watchTerminalAccess(ctx, t, rl)
	}()
	rl.browserToMember()
}

// memberToBrowser relays the member's frames as they are; a close frame
// ends the relay after it is relayed (a 4403 reason gets "member_").
func (rl *terminalRelay) memberToBrowser() {
	for {
		f, err := readWSFrame(rl.memberR, false, maxMemberFrame)
		if err != nil {
			return
		}
		if f.opcode == wsOpClose {
			out := f.raw
			if len(f.payload) >= 2 && binary.BigEndian.Uint16(f.payload) == terminalCloseAccessLost {
				out = closeFrame(terminalCloseAccessLost, "member_"+string(f.payload[2:]))
			}
			_ = rl.toBrowser(out)
			return
		}
		if rl.toBrowser(f.raw) != nil {
			return
		}
	}
}

// browserToMember forwards the browser's control frames and resizes and
// drops everything else (typed input, text, fragments).
func (rl *terminalRelay) browserToMember() {
	dropped := 0
	defer func() {
		if dropped > 0 {
			slog.Debug("hub: live terminal: dropped browser frames (read-only)", "frames", dropped)
		}
	}()
	inFragment := false
	for {
		f, err := readWSFrame(rl.browserR, true, maxBrowserFrame)
		if err != nil {
			return
		}
		forward := false
		switch {
		case f.opcode == wsOpClose:
			_, _ = rl.member.Write(f.raw)
			return
		case f.opcode == wsOpPing || f.opcode == wsOpPong:
			forward = true
		case f.opcode == wsOpContinuation || !f.fin:
			inFragment = !f.fin
		case inFragment:
			// a data frame inside a fragmented message: drop it too
		case f.opcode == wsOpBinary && len(f.payload) == 5 && f.payload[0] == terminalOpResize:
			forward = true
		}
		if !forward {
			dropped++
			continue
		}
		_ = rl.member.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if _, err := rl.member.Write(f.raw); err != nil {
			return
		}
	}
}

// watchTerminalAccess re-checks the caller's hub token every
// terminalRecheck and on every hub token store change, and revokes the
// relay once it no longer holds the terminal. It returns when the relay
// ends, ctx ends or the hub stops (which also end the relay).
func (h *Hub) watchTerminalAccess(ctx context.Context, t hubTerminalTicket, rl *terminalRelay) {
	interval := h.terminalRecheck
	if interval <= 0 {
		interval = 30 * time.Second
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-rl.done:
			return
		case <-ctx.Done():
			return
		case <-h.life.Done():
			return
		case <-tick.C:
		case <-h.tokens.Changed():
		}
		if reason := h.terminalAccessLost(t); reason != "" {
			slog.Info("hub: live terminal closed: the hub token lost access", "project", t.project,
				"session_id", t.sessionID, "caller", t.caller.Name, "reason", reason)
			rl.revoke(reason)
			return
		}
	}
}
