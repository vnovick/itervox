package hub

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/apitokens"
)

// TestHubTerminalSeesARevokeRightAfterItsCheck: a revoke that lands right
// after a passing check (before the watcher waits again) still closes the
// pass-through at once: the watcher holds the store's change signal from
// before the check, so the revoke fires it. The periodic re-check is an
// hour away here.
func TestHubTerminalSeesARevokeRightAfterItsCheck(t *testing.T) {
	saved := terminalRecheckInterval
	terminalRecheckInterval = time.Hour
	t.Cleanup(func() { terminalRecheckInterval = saved })
	th, _ := terminalHub(t)
	var callerID string
	list, err := th.h.tokens.List()
	require.NoError(t, err)
	for _, tok := range list {
		if tok.Name == "operator-hub" {
			callerID = tok.ID
		}
	}
	require.NotEmpty(t, callerID)
	var armed, fired atomic.Bool
	th.h.terminalChecked = func() {
		if armed.Load() && fired.CompareAndSwap(false, true) {
			_, err := th.h.tokens.Revoke(callerID, "test")
			assert.NoError(t, err)
		}
	}
	ws := openHubTerminal(t, th, th.secrets[apitokens.ScopeOperator])

	// An unrelated change makes the watcher check (and pass) once more;
	// the hook revokes the caller's token right after that check.
	armed.Store(true)
	_, _, err = th.h.tokens.Create("unrelated", apitokens.ScopeViewer, nil, "test")
	require.NoError(t, err)
	code, reason := ws.readUntil(t, "never")
	assert.True(t, fired.Load())
	assert.Equal(t, terminalCloseAccessLost, code)
	assert.Equal(t, "hub_token_revoked", reason)
}

// TestReadWSFrameRejectsFragmentedControlFrames: a control frame (close,
// ping, pong) without FIN is malformed (RFC 6455 5.5); with FIN it reads.
func TestReadWSFrameRejectsFragmentedControlFrames(t *testing.T) {
	for _, op := range []byte{wsOpClose, wsOpPing, wsOpPong, 0xB} {
		_, err := readWSFrame(bufio.NewReader(bytes.NewReader([]byte{op, 0})), false, 1<<10)
		require.ErrorIs(t, err, errBadFrame, "opcode %#x without FIN", op)
		f, err := readWSFrame(bufio.NewReader(bytes.NewReader([]byte{0x80 | op, 0})), false, 1<<10)
		require.NoError(t, err, "opcode %#x with FIN", op)
		assert.True(t, f.fin)
	}
	// A data frame without FIN is still a (dropped) fragment, not an error.
	_, err := readWSFrame(bufio.NewReader(bytes.NewReader([]byte{wsOpBinary, 0})), false, 1<<10)
	assert.NoError(t, err)
}

// TestTruncateCloseReasonKeepsUTF8: a close reason is cut to at most 123
// bytes at a rune boundary, so the close frame stays valid UTF-8.
func TestTruncateCloseReasonKeepsUTF8(t *testing.T) {
	assert.Equal(t, "member_token_revoked", truncateCloseReason("member_token_revoked"))
	exact := strings.Repeat("a", maxCloseReason)
	assert.Equal(t, exact, truncateCloseReason(exact))
	// 122 ASCII bytes then a two-byte rune straddling the limit.
	straddle := strings.Repeat("a", maxCloseReason-1) + "é"
	got := truncateCloseReason(straddle)
	assert.Equal(t, strings.Repeat("a", maxCloseReason-1), got)
	for _, s := range []string{strings.Repeat("é", 100), "member_" + strings.Repeat("日本", 40), strings.Repeat("😀", 50)} {
		got := truncateCloseReason(s)
		assert.LessOrEqual(t, len(got), maxCloseReason)
		assert.True(t, utf8.ValidString(got), "%q", got)
		assert.True(t, strings.HasPrefix(s, got))
		frame := closeFrame(terminalCloseAccessLost, s)
		assert.LessOrEqual(t, len(frame)-2, 125, "a control frame's payload")
		assert.Equal(t, int(frame[1]), len(frame)-2)
	}
}

// TestHubRelaysALongMemberCloseReasonAsValidUTF8: the member's 4403 close
// reason gets "member_" and is cut at a rune boundary within 123 bytes.
func TestHubRelaysALongMemberCloseReasonAsValidUTF8(t *testing.T) {
	browser, browserPeer := net.Pipe()
	member, memberPeer := net.Pipe()
	rl := newTerminalRelay(browserPeer, bufio.NewReader(browserPeer), memberPeer, bufio.NewReader(memberPeer))
	go rl.memberToBrowser()
	defer rl.end()

	reason := strings.Repeat("é", 60) // 120 bytes; with "member_" 127
	payload := make([]byte, 2, 2+len(reason))
	binary.BigEndian.PutUint16(payload, terminalCloseAccessLost)
	payload = append(payload, reason...)
	go func() {
		_, _ = member.Write(append([]byte{0x80 | wsOpClose, byte(len(payload))}, payload...))
	}()
	require.NoError(t, browser.SetReadDeadline(time.Now().Add(5*time.Second)))
	f, err := readWSFrame(bufio.NewReader(browser), false, 1<<10)
	require.NoError(t, err)
	require.Equal(t, wsOpClose, f.opcode)
	assert.Equal(t, uint16(terminalCloseAccessLost), binary.BigEndian.Uint16(f.payload))
	got := string(f.payload[2:])
	assert.True(t, utf8.ValidString(got), "%q", got)
	assert.LessOrEqual(t, len(got), maxCloseReason)
	assert.True(t, strings.HasPrefix(got, "member_éé"))
}

// TestHubRelayWritesNothingAfterItsCloseFrame: once the relay sent the
// browser a close frame (its own 4403 or the member's), no member frame is
// written after it, even before the connections are closed.
func TestHubRelayWritesNothingAfterItsCloseFrame(t *testing.T) {
	browser, browserPeer := net.Pipe()
	_, memberPeer := net.Pipe()
	rl := newTerminalRelay(browserPeer, bufio.NewReader(browserPeer), memberPeer, bufio.NewReader(memberPeer))
	defer rl.end()
	frames := make(chan wsFrame, 4)
	go func() {
		r := bufio.NewReader(browser)
		for {
			f, err := readWSFrame(r, false, 1<<10)
			if err != nil {
				close(frames)
				return
			}
			frames <- f
		}
	}()
	require.NoError(t, rl.toBrowser([]byte{0x80 | wsOpBinary, 1, 'a'}))
	require.NoError(t, rl.closeBrowser(closeFrame(terminalCloseAccessLost, "hub_token_revoked")))
	err := rl.toBrowser([]byte{0x80 | wsOpBinary, 1, 'b'})
	require.True(t, errors.Is(err, net.ErrClosed), "a frame after the close is refused: %v", err)
	require.ErrorIs(t, rl.closeBrowser(closeFrame(1000, "")), net.ErrClosed, "one close frame only")
	assert.Equal(t, wsOpBinary, (<-frames).opcode)
	assert.Equal(t, wsOpClose, (<-frames).opcode)
	_ = browser.Close()
	_, more := <-frames
	assert.False(t, more, "nothing after the close frame")
}
