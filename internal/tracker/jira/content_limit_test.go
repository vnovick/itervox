package jira_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/tracker"
	"github.com/vnovick/itervox/internal/tracker/jira"
)

// jiraLimit is the most text Jira Cloud stores in a comment body or an issue
// description. It was measured on a live site on 2026-11-20; the facts are
// next to contentLimit in adf.go. The fake server below enforces it on its
// own, so these tests do not trust the adapter's constant.
const jiraLimit = 32767

// storedCount counts an ADF document the way the live site did: UTF-16 units
// of the text, 1 for each hard break and 2 between two paragraphs.
func storedCount(t *testing.T, doc any) int {
	t.Helper()
	m, ok := doc.(map[string]any)
	require.True(t, ok, "document is %T", doc)
	paras, _ := m["content"].([]any)
	n := 0
	for i, p := range paras {
		if i > 0 {
			n += 2
		}
		para, _ := p.(map[string]any)
		inline, _ := para["content"].([]any)
		for _, in := range inline {
			node, _ := in.(map[string]any)
			switch node["type"] {
			case "hardBreak":
				n++
			case "text":
				s, _ := node["text"].(string)
				n += units(s)
			}
		}
	}
	return n
}

// units returns the UTF-16 length of s.
func units(s string) int { return len(utf16.Encode([]rune(s))) }

// limitedCommentServer answers comment POSTs like Jira Cloud: a body over
// jiraLimit gets 400 CONTENT_LIMIT_EXCEEDED, any other body is stored and
// echoed back. When echo is false the 201 answer carries no body.
func limitedCommentServer(t *testing.T, echo bool) (*jira.Client, func() []recorded) {
	t.Helper()
	return newJiraServer(t, func(_ int, r recorded, w http.ResponseWriter) {
		if r.Method != "POST" || !strings.HasSuffix(r.Path, "/comment") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var in map[string]any
		if err := json.Unmarshal([]byte(r.Body), &in); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if storedCount(t, in["body"]) > jiraLimit {
			w.WriteHeader(http.StatusBadRequest)
			writeJSON(w, map[string]any{"errorMessages": []string{"CONTENT_LIMIT_EXCEEDED"}, "errors": map[string]any{"comment": "CONTENT_LIMIT_EXCEEDED"}})
			return
		}
		w.WriteHeader(http.StatusCreated)
		out := map[string]any{"id": "900", "created": "2024-02-01T10:00:00.000+0000"}
		if echo {
			out["body"] = in["body"]
		}
		writeJSON(w, out)
	})
}

// sentText returns the text of the one comment the client posted.
func sentText(t *testing.T, seen func() []recorded) string {
	t.Helper()
	reqs := seen()
	require.Len(t, reqs, 1, "exactly one POST")
	return jira.ADFToText(reqs[0].jsonBody(t)["body"])
}

// sessionSummary is a long run's session summary: n short paragraphs of agent
// narration under a heading.
func sessionSummary(n int) string {
	var b strings.Builder
	b.WriteString("## Itervox Agent Session — PROJ-1\n\n")
	for i := range n {
		fmt.Fprintf(&b, "Step %d: ran the tests, read `internal/foo/bar.go` and changed the retry loop so that it stops after the deadline.\n\n", i)
	}
	return b.String()
}

// shortenedNote matches the line the adapter writes where it cut the text.
var shortenedNote = regexp.MustCompile(`\n\n\[Shortened by Itervox: ([0-9]+) characters cut to fit Jira's 32767-character limit\.\]`)

// splitAtNote returns the kept text before the note, the number the note
// states, and the text after the note.
func splitAtNote(t *testing.T, text string) (kept string, cut int, after string) {
	t.Helper()
	loc := shortenedNote.FindStringSubmatchIndex(text)
	require.NotNil(t, loc, "no shortened note in the stored text")
	n, err := strconv.Atoi(text[loc[2]:loc[3]])
	require.NoError(t, err)
	return text[:loc[0]], n, text[loc[1]:]
}

// TestJiraCommentOverTheContentLimitIsDelivered is the red team's
// TestRedteamJiraCommentOverTheContentLimitIsDelivered, ported to this branch.
// A session summary longer than Jira's limit used to be refused with 400
// CONTENT_LIMIT_EXCEEDED on every try, and the outbox retries a comment for
// ever and delivers only the head of each issue's queue: the completion move
// behind it never reached Jira. It must be delivered, shortened, in one POST,
// with a note that says how much was cut.
func TestJiraCommentOverTheContentLimitIsDelivered(t *testing.T) {
	c, seen := limitedCommentServer(t, true)
	body := tracker.MarkManagedComment(sessionSummary(400))
	require.Greater(t, units(body), jiraLimit)

	got, err := c.CreateComment(t.Context(), "PROJ-1", body)

	require.NoError(t, err, "Jira refused the comment; the outbox would retry it for ever")
	require.NotNil(t, got)
	text := sentText(t, seen)
	kept, cut, after := splitAtNote(t, text)
	assert.True(t, strings.HasPrefix(body, kept+"\n"), "the stored text starts with the original text, cut at a line end")
	original := strings.TrimSuffix(body, "\n\n"+tracker.ManagedCommentMarker)
	assert.Equal(t, units(original)-units(kept), cut, "the note states how much was cut")
	assert.Equal(t, "\n\n"+tracker.ManagedCommentMarker, after)
	assert.True(t, tracker.IsManagedComment(*got), "a shortened comment must still carry the managed marker")
	assert.Equal(t, text, got.Body, "the returned comment holds what Jira stored")
}

// The daemon knows its own comments by the marker lines at their end. A cut
// that dropped them would make it read its own comment as a person's reply.
func TestCreateCommentShortenedKeepsTrailingMarkers(t *testing.T) {
	c, seen := limitedCommentServer(t, true)
	body := tracker.MarkCommentKey(tracker.MarkManagedComment(sessionSummary(400)), "ck-1")

	got, err := c.CreateComment(t.Context(), "PROJ-1", body)

	require.NoError(t, err)
	text := sentText(t, seen)
	_, _, after := splitAtNote(t, text)
	assert.Equal(t, "\n\n"+tracker.ManagedCommentMarker+"\n\n"+tracker.CommentKeyMarker("ck-1"), after,
		"both marker lines are kept, in order, after the note")
	assert.True(t, tracker.IsManagedComment(*got))
	assert.True(t, tracker.CommentHasKey(got.Body, "ck-1"))
}

// The daemon finds the agent's question by its leading text, so the cut keeps
// the start of the text, even when the question is one very long line.
func TestCreateCommentShortenedKeepsTheQuestionPrefix(t *testing.T) {
	c, seen := limitedCommentServer(t, true)
	const prefix = "🤖 **Agent needs your input**"
	body := tracker.MarkManagedComment(prefix + "\n\n" + strings.Repeat("Which of these files should I change? ", 1200) +
		"\n\n---\n_Reply in the tracker or via the Itervox dashboard to continue._")

	got, err := c.CreateComment(t.Context(), "PROJ-1", body)

	require.NoError(t, err)
	text := sentText(t, seen)
	assert.True(t, strings.HasPrefix(text, prefix+"\n\nWhich of these files"), "stored text starts %q", text[:60])
	kept, _, _ := splitAtNote(t, text)
	assert.True(t, strings.HasPrefix(body, kept), "the kept text is the start of the original")
	assert.True(t, tracker.IsManagedComment(*got))
}

// A text Jira can store is sent exactly as before; one character more is
// shortened. The text has an emoji, which Jira counts as 2, and a run of
// three line feeds, which textToADF stores as one paragraph break (2).
func TestCreateCommentAtTheContentLimitIsSentUnchanged(t *testing.T) {
	head := "Done 🤖\n\n\nDetails:\n"
	headCount := units("Done 🤖") + 2 + units("Details:") + 1
	atLimit := head + strings.Repeat("a", jiraLimit-headCount)

	t.Run("at the limit", func(t *testing.T) {
		c, seen := limitedCommentServer(t, true)
		require.Equal(t, jiraLimit+1, units(atLimit), "one more line feed than Jira counts")

		_, err := c.CreateComment(t.Context(), "PROJ-1", atLimit)

		require.NoError(t, err)
		reqs := seen()
		require.Len(t, reqs, 1)
		want, err := json.Marshal(map[string]any{"body": jira.TextToADF(atLimit)})
		require.NoError(t, err)
		assert.Equal(t, string(want), reqs[0].Body, "sent byte for byte as without the limit")
	})

	t.Run("one over the limit", func(t *testing.T) {
		c, seen := limitedCommentServer(t, true)

		_, err := c.CreateComment(t.Context(), "PROJ-1", atLimit+"a")

		require.NoError(t, err)
		splitAtNote(t, sentText(t, seen))
	})
}

// A cut never lands inside a character: not inside a multi-byte UTF-8
// sequence, and not between the two UTF-16 halves of an emoji. The text has
// no line feed, so the cut lands inside the run of emoji; the two cases move
// it by one unit, so one of them has an odd number of units left for it.
func TestCreateCommentCutNeverSplitsACharacter(t *testing.T) {
	for _, lead := range []string{"", "a"} {
		t.Run(fmt.Sprintf("lead %q", lead), func(t *testing.T) {
			c, seen := limitedCommentServer(t, true)
			body := tracker.MarkManagedComment(lead + strings.Repeat("😀", 20000))

			_, err := c.CreateComment(t.Context(), "PROJ-1", body)

			require.NoError(t, err, "a cut that counts an emoji as 1 goes over the limit")
			text := sentText(t, seen)
			require.True(t, utf8.ValidString(text))
			assert.NotContains(t, text, "�", "a split character arrives as U+FFFD")
			kept, _, _ := splitAtNote(t, text)
			assert.Equal(t, "", strings.ReplaceAll(strings.TrimPrefix(kept, lead), "😀", ""), "only whole emoji are kept")
			assert.Greater(t, units(kept), jiraLimit-200, "the cut keeps nearly all the room")
		})
	}
}

// When a line ends near the cut, the cut is made there, so no line is
// stored half written.
func TestCreateCommentCutPrefersTheEndOfALine(t *testing.T) {
	c, seen := limitedCommentServer(t, true)
	var b strings.Builder
	for i := range 1000 {
		fmt.Fprintf(&b, "line %04d %s\n", i, strings.Repeat("x", 40))
	}
	body := tracker.MarkManagedComment(b.String())

	_, err := c.CreateComment(t.Context(), "PROJ-1", body)

	require.NoError(t, err)
	kept, _, _ := splitAtNote(t, sentText(t, seen))
	assert.True(t, strings.HasPrefix(body, kept+"\n"), "the kept text ends with a whole line: %q", kept[len(kept)-20:])
}

// When Jira's answer carries no body, the returned comment holds the text
// that was sent, which is the shortened text, not the original.
func TestCreateCommentReturnsTheShortenedText(t *testing.T) {
	c, seen := limitedCommentServer(t, false)
	body := tracker.MarkManagedComment(sessionSummary(400))

	got, err := c.CreateComment(t.Context(), "PROJ-1", body)

	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, sentText(t, seen), got.Body)
	assert.NotEqual(t, body, got.Body)
	assert.True(t, tracker.IsManagedComment(*got))
}

// CreateIssue sends the description through the same limit.
func TestCreateIssueShortensAnOverLimitDescription(t *testing.T) {
	c, seen := newJiraServer(t, func(_ int, r recorded, w http.ResponseWriter) {
		switch {
		case r.Method == "POST" && r.Path == "/rest/api/3/issue":
			fields, _ := r.jsonBody(t)["fields"].(map[string]any)
			if storedCount(t, fields["description"]) > jiraLimit {
				w.WriteHeader(http.StatusBadRequest)
				writeJSON(w, map[string]any{"errors": map[string]any{"description": "The entered text is too long. It exceeds the allowed limit of 32767 characters."}})
				return
			}
			w.WriteHeader(http.StatusCreated)
			writeJSON(w, map[string]any{"id": "10050", "key": "PROJ-13"})
		case r.Path == "/rest/api/3/issue/bulkfetch":
			writeJSON(w, map[string]any{"issues": []any{issueFixture("10050", "PROJ-13", "To Do")}})
		default:
			w.WriteHeader(http.StatusTeapot)
		}
	})
	desc := sessionSummary(400)

	got, err := c.CreateIssue(t.Context(), "", tracker.NewIssue{Title: "Follow up", Body: desc})

	require.NoError(t, err)
	require.NotNil(t, got)
	reqs := seen()
	require.Len(t, reqs, 2, "create, read-back")
	fields, _ := reqs[0].jsonBody(t)["fields"].(map[string]any)
	stored := jira.ADFToText(fields["description"])
	kept, cut, after := splitAtNote(t, stored)
	assert.True(t, strings.HasPrefix(desc, kept+"\n"))
	assert.Equal(t, units(desc)-units(kept), cut)
	assert.Empty(t, after)
}
