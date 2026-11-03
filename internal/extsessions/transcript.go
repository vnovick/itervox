package extsessions

import (
	"time"
	"unicode/utf8"

	"github.com/vnovick/itervox/internal/logging"
)

// Entry kinds.
const (
	KindText       = "text"        // a user or assistant message
	KindTool       = "tool"        // a tool call; Text is the tool's name
	KindToolResult = "tool_result" // a tool's output, abbreviated
)

// Transcript page bounds.
const (
	// DefaultPageEntries is a page's size when the caller asks for none.
	DefaultPageEntries = 100
	// MaxPageEntries caps a page.
	MaxPageEntries = 500
	// maxTextRunes caps one message's text.
	maxTextRunes = 4000
	// maxToolResultRunes caps one tool result.
	maxToolResultRunes = 600
	// pageScanBytes bounds the bytes one page reads backwards.
	pageScanBytes = 8 * 1024 * 1024
)

// Entry is one transcript item, redacted and abbreviated.
type Entry struct {
	Role      string // "user" or "assistant"
	Kind      string // KindText, KindTool or KindToolResult
	Text      string
	At        time.Time
	Truncated bool
}

// Page is one page of a transcript, oldest entry first.
type Page struct {
	Entries []Entry
	// Before is the cursor for the next older page; meaningful when
	// HasMore.
	Before  int64
	HasMore bool
	// Size is the transcript's size in bytes when it was read.
	Size int64
}

// ReadTranscript returns about limit entries (a record that yields several
// is kept whole, so a page may run a few over) of s's transcript ending
// before byte offset before (< 0: the end), newest last. Every text is
// passed through logging.RedactString and abbreviated.
func ReadTranscript(s Session, before int64, limit int) (Page, error) {
	if limit <= 0 {
		limit = DefaultPageEntries
	}
	limit = min(limit, MaxPageEntries)
	parse := claudeEntries
	if s.Backend == BackendCodex {
		parse = codexEntries
	}
	var rev [][]Entry // per line, newest line first
	n := 0
	stop, err := tailLines(s.Path, before, pageScanBytes, func(line []byte, _ int64) bool {
		if n >= limit {
			return false
		}
		if es := parse(line); len(es) > 0 {
			rev = append(rev, es)
			n += len(es)
		}
		return true
	})
	if err != nil {
		return Page{}, err
	}
	page := Page{Before: stop, HasMore: stop > 0}
	for i := len(rev) - 1; i >= 0; i-- {
		for _, e := range rev[i] {
			page.Entries = append(page.Entries, abbreviate(e))
		}
	}
	if size, err := fileSize(s.Path); err == nil {
		page.Size = size
	}
	return page, nil
}

// abbreviate redacts e's text and caps its length.
func abbreviate(e Entry) Entry {
	e.Text = logging.RedactString(e.Text)
	limit := maxTextRunes
	if e.Kind == KindToolResult {
		limit = maxToolResultRunes
	}
	if utf8.RuneCountInString(e.Text) > limit {
		r := []rune(e.Text)
		e.Text = string(r[:limit]) + "…"
		e.Truncated = true
	}
	return e
}
