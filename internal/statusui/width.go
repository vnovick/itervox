package statusui

import (
	"strings"
	"unicode"

	xansi "github.com/charmbracelet/x/ansi"
)

// Width helpers for the TUI (CORE-072).
//
// Every cell-budgeted string in this package is measured and cut by terminal
// display width, never by byte or rune count: a byte slice of a lipgloss-styled
// string can stop inside an SGR escape (leaving an unterminated ESC '[' that
// swallows the following text) or inside a multi-byte rune, and fmt's "%-*s"
// pads by rune count, which overpads nothing and underpads CJK/emoji. Both
// helpers below are ANSI-aware (escape sequences occupy zero cells) and
// grapheme-aware (wide runes count as two cells, combining marks as zero).

// truncate shortens s to at most max display cells, replacing the cut tail with
// "…". A string that already fits is returned unchanged. max <= 1 yields "…"
// for any string that does not fit, matching the historical behaviour.
func truncate(s string, max int) string {
	if xansi.StringWidth(s) <= max {
		return s
	}
	if max <= 1 {
		return "…"
	}
	return xansi.Truncate(s, max, "…")
}

// fitWidth returns s cut and right-padded with spaces to exactly w display
// cells. s may carry ANSI styling; escape sequences are never split. When a
// wide rune straddles the boundary it is dropped and the gap is padded, so the
// result is always exactly w cells.
func fitWidth(s string, w int) string {
	if w <= 0 {
		return ""
	}
	s = xansi.Truncate(s, w, "")
	if pad := w - xansi.StringWidth(s); pad > 0 {
		s += strings.Repeat(" ", pad)
	}
	return s
}

// lineBreakMark is the visible separator singleLine puts where the source text
// had one or more line breaks. U+23CE has East Asian Width "N", so terminals
// agree it is one cell.
const lineBreakMark = " ⏎ "

// singleLine flattens free-form text (agent output, subagent descriptions,
// error strings) into one display line before it is width-budgeted (BH-M5-4).
// xansi.StringWidth counts '\n', '\r', '\t' and other control bytes as zero
// cells, so truncate/fitWidth pass them through untouched and one logical row
// renders as several terminal lines, growing the pane past its height. Runs of
// line breaks become lineBreakMark; runs of other whitespace and control
// characters (tabs, ESC, BEL, DEL, C1) become one space; leading and trailing
// whitespace is dropped. Only call this on raw text, never on a string that
// already carries lipgloss styling: it would neutralise the ESC bytes.
func singleLine(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	pendingBreak, pendingSpace := false, false
	for _, r := range s {
		switch {
		case r == '\n' || r == '\r' || r == '\u0085' || r == '\u2028' || r == '\u2029':
			pendingBreak = b.Len() > 0
		case unicode.IsSpace(r) || unicode.IsControl(r):
			pendingSpace = b.Len() > 0
		default:
			if pendingBreak {
				b.WriteString(lineBreakMark)
			} else if pendingSpace {
				b.WriteByte(' ')
			}
			pendingBreak, pendingSpace = false, false
			b.WriteRune(r)
		}
	}
	return b.String()
}
