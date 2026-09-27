package agent

import (
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// CORE-002 whitebox coverage for the stream reader's type detection and its
// line-size tiers (see the "Stream line reader" comment in claude.go).

func TestDetectStreamLineType(t *testing.T) {
	rows := []struct {
		name, in, want string
		found          bool
	}{
		{"type_first", `{"type":"result","x":1}`, "result", true},
		{"leading_ws_spaced", " \t\r\n{ \"type\" : \"turn.completed\" }", "turn.completed", true},
		{"after_nested_values", `{"a":{"type":"inner","b":[1,{"c":"]"}]},"n":-1.5e3,"t":true,"z":null,"type":"turn.failed"}`, "turn.failed", true},
		{"escaped_quote_in_key_value", `{"k\"type":"v\\\"}","type":"assistant"}`, "assistant", true},
		{"nested_type_only", `{"a":{"type":"result"}}`, "", false},
		{"truncated_before_type", `{"message":"aaaa`, "", false},
		{"truncated_type_value", `{"type":"resu`, "", false},
		{"non_string_type", `{"type":1}`, "", false},
		{"not_object", `["type","result"]`, "", false},
		{"empty", ``, "", false},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			got, found := detectStreamLineType([]byte(r.in))
			assert.Equal(t, r.found, found)
			assert.Equal(t, r.want, got)
		})
	}
}

// A non-terminal line between the detection budget and the non-terminal cap
// (e.g. a 200 KiB assistant message) is still decoded, not skipped — every
// line the pre-CORE-002 1 MiB Scanner decoded still decodes.
func TestStreamLineReaderDecodesMidSizedNonTerminalLine(t *testing.T) {
	body := strings.Repeat("b", 200<<10)
	in := `{"type":"assistant","text":"` + body + "\"}\r\n" + `{"type":"result"}` // final line unterminated
	lr := newStreamLineReader(strings.NewReader(in))

	first, err := lr.next()
	require.NoError(t, err)
	assert.Zero(t, first.skippedBytes)
	assert.Equal(t, `{"type":"assistant","text":"`+body+`"}`, string(first.line), "'\\r' stripped")

	second, err := lr.next()
	require.NoError(t, err)
	assert.Equal(t, `{"type":"result"}`, string(second.line))

	_, err = lr.next()
	assert.ErrorIs(t, err, io.EOF)
}

// The line-size tiers: exactly at the non-terminal cap decodes, one byte over
// is skipped with its full length reported; a terminal line over the
// non-terminal cap decodes.
func TestStreamLineReaderTiers(t *testing.T) {
	prefix := `{"type":"user","c":"`
	suffix := `"}`
	at := prefix + strings.Repeat("x", maxNonTerminalLineBytes-len(prefix)-len(suffix)) + suffix
	over := prefix + strings.Repeat("x", maxNonTerminalLineBytes-len(prefix)-len(suffix)+1) + suffix
	term := `{"type":"result","r":"` + strings.Repeat("y", 3<<20) + `"}`
	lr := newStreamLineReader(strings.NewReader(at + "\n" + over + "\n" + term + "\n"))

	sl, err := lr.next()
	require.NoError(t, err)
	assert.Len(t, sl.line, maxNonTerminalLineBytes)

	sl, err = lr.next()
	require.NoError(t, err)
	assert.Nil(t, sl.line)
	assert.Equal(t, maxNonTerminalLineBytes+1, sl.skippedBytes)
	assert.Equal(t, "user", sl.skippedType)

	sl, err = lr.next()
	require.NoError(t, err)
	assert.Len(t, sl.line, len(term))
}

// M0-close re-check G6: the detection budget never decides the fate of a line
// of at most 1 MiB. A line whose "type" is not detectable within the first
// 64 KiB (it comes last, after a 900 KiB value) is returned in full; the same
// shape one byte over 1 MiB is skipped with no type reported.
func TestStreamLineReaderIgnoresDetectionBudgetUpToNonTerminalCap(t *testing.T) {
	prefix := `{"payload":"`
	suffix := `","type":"result"}`
	fill := maxNonTerminalLineBytes - len(prefix) - len(suffix)
	at := prefix + strings.Repeat("x", fill) + suffix
	over := prefix + strings.Repeat("x", fill+1) + suffix
	lr := newStreamLineReader(strings.NewReader(at + "\n" + over + "\n"))

	sl, err := lr.next()
	require.NoError(t, err)
	assert.Zero(t, sl.skippedBytes, "a 1 MiB line is decoded even with type beyond the budget")
	assert.Len(t, sl.line, maxNonTerminalLineBytes)

	sl, err = lr.next()
	require.NoError(t, err)
	assert.Nil(t, sl.line)
	assert.Equal(t, maxNonTerminalLineBytes+1, sl.skippedBytes)
	assert.Empty(t, sl.skippedType, "type lies beyond the detection budget")
}
