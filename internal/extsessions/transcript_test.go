package extsessions

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func discoverAll(t *testing.T) []Session {
	t.Helper()
	now := time.Now()
	claude, codex := fixtures(t, now)
	got, err := Discover(Options{ClaudeProjectsDir: claude, CodexSessionsDir: codex,
		Window: 24 * time.Hour, Scope: ScopeAll}, now)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func summary(es []Entry) []string {
	out := make([]string, 0, len(es))
	for _, e := range es {
		out = append(out, e.Role+"/"+e.Kind+":"+e.Text)
	}
	return out
}

// The Claude transcript shows user and assistant text, tool calls by name
// and abbreviated tool results; thinking, meta, sidechain and unknown
// records are left out, a malformed line is skipped, and text is redacted.
func TestReadTranscriptClaudeFormat(t *testing.T) {
	s := find(t, discoverAll(t), idClaudeApp)
	page, err := ReadTranscript(s, -1, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(summary(page.Entries), "\n")
	want := strings.Join([]string{
		"user/text:Add a health check endpoint",
		"assistant/text:I will add it to server.go.",
		"assistant/tool:Edit",
		"user/tool_result:edited; MY_API_KEY=*** was in env",
		"assistant/text:Done: GET /healthz returns 200.",
	}, "\n")
	if got != want {
		t.Fatalf("transcript:\n%s\nwant:\n%s", got, want)
	}
	if page.HasMore || page.Size == 0 {
		t.Fatalf("page = %+v", page)
	}
	if strings.Contains(got, "private reasoning") || strings.Contains(got, "sidechain") {
		t.Fatal("thinking or sidechain content leaked into the transcript")
	}
}

// Both Codex layouts parse: the session_meta envelope (context messages
// and reasoning left out) and the legacy bare items.
func TestReadTranscriptCodexFormats(t *testing.T) {
	all := discoverAll(t)
	page, err := ReadTranscript(find(t, all, idCodexApp), -1, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(summary(page.Entries), "\n")
	want := "user/text:Fix the flaky test\nassistant/tool:shell\nuser/tool_result:ok  pkg 0.1s\nassistant/text:The test is fixed."
	if got != want {
		t.Fatalf("codex transcript:\n%s\nwant:\n%s", got, want)
	}
	legacy, err := ReadTranscript(find(t, all, idCodexLegacy), -1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(summary(legacy.Entries), "\n"); got != "user/text:legacy hello\nassistant/text:legacy reply" {
		t.Fatalf("legacy transcript:\n%s", got)
	}
}

// Pages are read backwards from a cursor; following Before walks the whole
// transcript exactly once, oldest page last; long texts are abbreviated.
func TestReadTranscriptPaginatesBackwards(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	var b strings.Builder
	for i := range 250 {
		fmt.Fprintf(&b, `{"type":"user","sessionId":"x","message":{"role":"user","content":"msg %03d"}}`+"\n", i)
	}
	fmt.Fprintf(&b, `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":%q}]}}`+"\n", strings.Repeat("y", maxTextRunes+10))
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	s := Session{ID: "x", Backend: BackendClaude, Path: path}
	var pages [][]Entry
	before := int64(-1)
	for range 10 {
		p, err := ReadTranscript(s, before, 100)
		if err != nil {
			t.Fatal(err)
		}
		pages = append(pages, p.Entries)
		if !p.HasMore {
			break
		}
		before = p.Before
	}
	if len(pages) != 3 || len(pages[0]) != 100 || len(pages[2]) != 51 {
		t.Fatalf("pages of %d, %d…: %d pages", len(pages[0]), len(pages[len(pages)-1]), len(pages))
	}
	last := pages[0][len(pages[0])-1]
	if !last.Truncated || len([]rune(last.Text)) != maxTextRunes+1 {
		t.Fatalf("long text not abbreviated: truncated=%v len=%d", last.Truncated, len(last.Text))
	}
	var seen []string
	for i := len(pages) - 1; i >= 0; i-- {
		for _, e := range pages[i] {
			if e.Role == "user" {
				seen = append(seen, e.Text)
			}
		}
	}
	if len(seen) != 250 || seen[0] != "msg 000" || seen[249] != "msg 249" {
		t.Fatalf("walked %d user messages: first %q last %q", len(seen), seen[0], seen[len(seen)-1])
	}
}

// The parsers tolerate what they do not know: malformed JSON, unknown
// record and block types, and content in an unexpected shape yield no
// entries instead of an error.
func TestParsersTolerateMalformedAndUnknownRecords(t *testing.T) {
	for _, line := range []string{
		``, `not json`, `{"type":"user"}`, `{"type":"user","message":{"content":42}}`,
		`{"type":"assistant","message":{"content":[{"type":"image"}]}}`,
		`{"type":"future","message":{"role":"user","content":"x"}}`,
	} {
		if es := claudeEntries([]byte(line)); len(es) != 0 {
			t.Fatalf("claude %q yielded %v", line, es)
		}
	}
	for _, line := range []string{
		``, `not json`, `{"type":"response_item"}`, `{"type":"response_item","payload":{"type":"message","role":"developer","content":[{"text":"x"}]}}`,
		`{"type":"event_msg","payload":{"type":"agent_message","message":"dup"}}`, `{"type":"unknown_future"}`,
	} {
		if es := codexEntries([]byte(line)); len(es) != 0 {
			t.Fatalf("codex %q yielded %v", line, es)
		}
	}
}
