package jira_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
	"github.com/vnovick/itervox/internal/tracker/jira"
)

// storedText joins the text of every text node in an ADF tree: the text a
// person reads in Jira.
func storedText(v any) string {
	var b strings.Builder
	var walk func(any)
	walk = func(v any) {
		switch node := v.(type) {
		case map[string]any:
			if s, ok := node["text"].(string); ok {
				b.WriteString(s)
			}
			walk(node["content"])
		case []any:
			for _, ch := range node {
				walk(ch)
			}
		}
	}
	walk(v)
	return b.String()
}

// On Jira the comment key lives in the itervox.commentKey comment property,
// and FindCommentByKey returns "the comment whose itervox.commentKey property
// holds key". A comment with no property, only the key's marker typed (or
// pasted) into its text, is not that comment: the outbox reads "found" as
// "already delivered" and would drop its own pending comment, and
// findTrackedQuestionComment would take the comment for the agent's question.
func TestKeyMarkerTypedInACommentIsNotAKey(t *testing.T) {
	f := &keyedFake{}
	c, _ := newJiraServer(t, f.handler)
	f.add("I copied this from somewhere: "+tracker.CommentKeyMarker(testKey), "", "")

	got, found, err := c.FindCommentByKey(t.Context(), "PROJ-1", testKey)

	require.NoError(t, err)
	assert.False(t, found, "a comment without the itervox.commentKey property was taken as the keyed comment (%+v)", got)

	d, err := c.FetchIssueDetail(t.Context(), "PROJ-1")
	require.NoError(t, err)
	require.Len(t, d.Comments, 1)
	assert.False(t, tracker.CommentHasKey(d.Comments[0].Body, testKey),
		"the orchestrator matches keys in the body text, so text a person typed must not read as the key")
}

// GitHub and Linear hide an HTML comment; Jira shows it. Written as text,
// "<!-- itervox:managed -->" shows under every Itervox comment, and a person
// who answers the agent's question by quoting it (copy and paste in the Jira
// editor) copies the marker too. tracker.IsManagedComment would then read the
// reply as an Itervox comment: findReplyAfterQuestion skips it and the agent
// never resumes. The marker goes in a comment property instead, and marker
// text in a comment counts for nothing.
func TestPastedManagedMarkerDoesNotHideAPersonsReply(t *testing.T) {
	f := &keyedFake{}
	c, seen := newJiraServer(t, f.handler)
	question := tracker.MarkManagedComment("🤖 **Agent needs your input**\n\nShould I keep the old API?\n\n---\n_Reply to this comment to continue._")
	_, err := c.CreateComment(t.Context(), "PROJ-1", question)
	require.NoError(t, err)
	sent := seen()[0].jsonBody(t)["body"]
	assert.NotContains(t, storedText(sent), tracker.ManagedCommentMarker,
		"the managed marker is written as text people read in Jira")

	// The person's reply: the question quoted as Jira stores a pasted quote,
	// then the answer.
	para := func(text string) any {
		return map[string]any{"type": "paragraph", "content": []any{map[string]any{"type": "text", "text": text}}}
	}
	reply := commentFixture("2000", "2024-03-02T00:00:00.000+0000", "")
	reply["body"] = map[string]any{"type": "doc", "version": 1, "content": []any{
		map[string]any{"type": "blockquote", "content": []any{
			para("Should I keep the old API?"), para(tracker.ManagedCommentMarker),
		}},
		para("No, drop it."),
	}}
	f.mu.Lock()
	f.comments = append(f.comments, reply)
	f.mu.Unlock()

	d, err := c.FetchIssueDetail(t.Context(), "PROJ-1")
	require.NoError(t, err)
	require.Len(t, d.Comments, 2)
	assert.True(t, tracker.IsManagedComment(d.Comments[0]), "the question is still Itervox's")
	assert.False(t, tracker.IsManagedComment(d.Comments[1]),
		"the person's reply %q reads as an Itervox comment", d.Comments[1].Body)
}

// managedProperty and keyProperty are the comment properties that carry
// Itervox's markers in Jira, as a create request sends them and
// comment/list?expand=properties lists them.
func managedProperty() map[string]any {
	return map[string]any{"key": "itervox.managed", "value": map[string]any{"managed": true}}
}

func keyProperty(key string) map[string]any {
	return map[string]any{"key": "itervox.commentKey", "value": map[string]any{"key": key}}
}

// The markers at the end of what the daemon hands over are sent as comment
// properties, never as text, and come back from the properties in the same
// place and form: the comment that CreateComment returns and the next read
// have the body that was handed over.
func TestCommentMarkersTravelAsProperties(t *testing.T) {
	const managed = tracker.ManagedCommentMarker
	keyMarker := tracker.CommentKeyMarker(testKey)
	cases := []struct {
		name      string
		key       string // CreateCommentWithKey's key; "" calls CreateComment
		body      string
		wantText  string // the text Jira stores and shows
		wantProps []any  // nil: the request has no properties
		wantBody  string // what the create returns and every read gives
	}{
		{
			name:      "managed comment",
			body:      tracker.MarkManagedComment("Done.\n\n- **all** tests pass"),
			wantText:  "Done.\n\n- **all** tests pass",
			wantProps: []any{managedProperty()},
			wantBody:  "Done.\n\n- **all** tests pass\n\n" + managed,
		},
		{
			name:      "managed comment with a key, as the outbox sends a question",
			key:       testKey,
			body:      tracker.MarkManagedComment("Ship it?"),
			wantText:  "Ship it?",
			wantProps: []any{managedProperty(), keyProperty(testKey)},
			wantBody:  "Ship it?\n\n" + managed + "\n\n" + keyMarker,
		},
		{
			name:      "key marker in the text handed to CreateComment",
			body:      tracker.MarkCommentKey(tracker.MarkManagedComment("Ship it?"), testKey),
			wantText:  "Ship it?",
			wantProps: []any{managedProperty(), keyProperty(testKey)},
			wantBody:  "Ship it?\n\n" + managed + "\n\n" + keyMarker,
		},
		{
			name:      "the key marker of the call's own key",
			key:       testKey,
			body:      tracker.MarkCommentKey(tracker.MarkManagedComment("Ship it?"), testKey),
			wantText:  "Ship it?",
			wantProps: []any{managedProperty(), keyProperty(testKey)},
			wantBody:  "Ship it?\n\n" + managed + "\n\n" + keyMarker,
		},
		{
			// No caller builds this order; MarkManagedComment always runs first.
			name:      "key marker before the managed marker reads back in the usual order",
			body:      tracker.MarkManagedComment(tracker.MarkCommentKey("Ship it?", testKey)),
			wantText:  "Ship it?",
			wantProps: []any{managedProperty(), keyProperty(testKey)},
			wantBody:  "Ship it?\n\n" + managed + "\n\n" + keyMarker,
		},
		{
			// MarkManagedComment adds no marker to a text that holds one, so
			// the comment must still be managed; the text keeps what the agent
			// wrote, and it reads back inert.
			name:      "managed marker inside the text",
			body:      tracker.MarkManagedComment("Read `" + managed + "` in managed_comments.go."),
			wantText:  "Read `" + managed + "` in managed_comments.go.",
			wantProps: []any{managedProperty()},
			wantBody:  "Read `&lt;!-- itervox:managed -->` in managed_comments.go.\n\n" + managed,
		},
		{
			name:      "a key marker of another key stays text",
			key:       testKey,
			body:      tracker.MarkCommentKey("Ship it?", "other-key"),
			wantText:  "Ship it?\n\n" + tracker.CommentKeyMarker("other-key"),
			wantProps: []any{keyProperty(testKey)},
			wantBody:  "Ship it?\n\n&lt;!-- itervox:ck:other-key -->\n\n" + keyMarker,
		},
		{
			name:     "no marker",
			body:     "A plain comment.",
			wantText: "A plain comment.",
			wantBody: "A plain comment.",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &keyedFake{}
			c, seen := newJiraServer(t, f.handler)

			var got *domain.Comment
			var err error
			if tc.key == "" {
				got, err = c.CreateComment(t.Context(), "PROJ-1", tc.body)
			} else {
				got, err = c.CreateCommentWithKey(t.Context(), "PROJ-1", tc.key, tc.body)
			}

			require.NoError(t, err)
			req := seen()[0].jsonBody(t)
			assert.Equal(t, tc.wantText, jira.ADFToText(req["body"]), "the text Jira stores")
			if tc.wantProps == nil {
				assert.NotContains(t, req, "properties")
			} else {
				assert.JSONEq(t, string(mustJSON(t, tc.wantProps)), string(mustJSON(t, req["properties"])), "the properties of the create request")
			}
			require.NotNil(t, got)
			assert.Equal(t, tc.wantBody, got.Body, "the comment the create returns")
			d, err := c.FetchIssueDetail(t.Context(), "PROJ-1")
			require.NoError(t, err)
			require.Len(t, d.Comments, 1)
			assert.Equal(t, tc.wantBody, d.Comments[0].Body, "the comment the next read returns")
		})
	}
}

// If Jira refuses the rich document, the plain retry sends the same
// properties, and its text has no marker either.
func TestPlainRetryCarriesTheMarkerProperties(t *testing.T) {
	c, seen := newJiraServer(t, func(_ int, r recorded, w http.ResponseWriter) {
		if strings.Contains(r.Body, `"heading"`) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, map[string]any{"id": "901", "created": "2024-02-01T10:00:00.000+0000", "body": r.jsonBody(t)["body"]})
	})
	const text = "## Done\n\n**ok**"

	got, err := c.CreateCommentWithKey(t.Context(), "PROJ-1", testKey, tracker.MarkManagedComment(text))

	require.NoError(t, err)
	reqs := seen()
	require.Len(t, reqs, 2, "the rich post, then the plain one")
	want := string(mustJSON(t, []any{managedProperty(), keyProperty(testKey)}))
	for i, r := range reqs {
		assert.JSONEq(t, want, string(mustJSON(t, r.jsonBody(t)["properties"])), "request %d", i)
		assert.NotContains(t, r.Body, "itervox:", "request %d writes no marker as text", i)
	}
	assert.JSONEq(t, string(mustJSON(t, jira.PlainTextToADF(text))), string(mustJSON(t, reqs[1].jsonBody(t)["body"])))
	require.NotNil(t, got)
	assert.Equal(t, text+"\n\n"+tracker.ManagedCommentMarker+"\n\n"+tracker.CommentKeyMarker(testKey), got.Body)
}

// Marker text in a comment counts for nothing: it reads with "&lt;" in place
// of its "<", which no marker parser in package tracker matches. Only the
// properties make markers.
func TestMarkerTextInACommentIsInert(t *testing.T) {
	cases := []struct {
		name  string
		text  string
		props []any
		want  string
	}{
		{
			name: "managed marker pasted by a person",
			text: "see " + tracker.ManagedCommentMarker + " above",
			want: "see &lt;!-- itervox:managed --> above",
		},
		{
			name: "managed marker left as text by an older version",
			text: "Done.\n\n" + tracker.ManagedCommentMarker,
			want: "Done.\n\n&lt;!-- itervox:managed -->",
		},
		{
			name: "key marker typed by a person",
			text: tracker.CommentKeyMarker(testKey),
			want: "&lt;!-- itervox:ck:" + testKey + " -->",
		},
		{
			name: "both markers, twice, with no space between",
			text: tracker.ManagedCommentMarker + tracker.CommentKeyMarker(testKey) + "<" + tracker.ManagedCommentMarker,
			want: "&lt;!-- itervox:managed -->&lt;!-- itervox:ck:" + testKey + " --><&lt;!-- itervox:managed -->",
		},
		{
			name:  "key marker pasted into a managed comment",
			text:  "Itervox's text " + tracker.CommentKeyMarker(testKey),
			props: []any{managedProperty()},
			want:  "Itervox's text &lt;!-- itervox:ck:" + testKey + " -->\n\n" + tracker.ManagedCommentMarker,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &keyedFake{}
			c, _ := newJiraServer(t, f.handler)
			f.addWithProperties(tc.text, tc.props...)

			d, err := c.FetchIssueDetail(t.Context(), "PROJ-1")

			require.NoError(t, err)
			require.Len(t, d.Comments, 1)
			got := d.Comments[0]
			assert.Equal(t, tc.want, got.Body)
			assert.Equal(t, tc.props != nil, tracker.IsManagedComment(got), "only the property makes the comment managed")
			assert.False(t, tracker.CommentHasKey(got.Body, testKey), "only the property gives the comment a key")
			_, found, err := c.FindCommentByKey(t.Context(), "PROJ-1", testKey)
			require.NoError(t, err)
			assert.False(t, found)
		})
	}
}

// The managed property must say managed: another value, or the same value
// under another property name, makes no marker.
func TestOnlyTheManagedPropertyMakesTheMarker(t *testing.T) {
	f := &keyedFake{}
	c, _ := newJiraServer(t, f.handler)
	f.addWithProperties("a", map[string]any{"key": "itervox.managed", "value": map[string]any{"managed": false}})
	f.addWithProperties("b", map[string]any{"key": "other.app", "value": map[string]any{"managed": true}})
	f.addWithProperties("c", managedProperty())

	d, err := c.FetchIssueDetail(t.Context(), "PROJ-1")

	require.NoError(t, err)
	require.Len(t, d.Comments, 3)
	assert.Equal(t, "a", d.Comments[0].Body)
	assert.Equal(t, "b", d.Comments[1].Body)
	assert.Equal(t, "c\n\n"+tracker.ManagedCommentMarker, d.Comments[2].Body)
}

// A create response without a body (Jira always sends one; a proxy or fake
// may not) gives the text that was sent, read the same way.
func TestCreatedCommentWithoutABodyInTheResponse(t *testing.T) {
	c, _ := newJiraServer(t, func(_ int, _ recorded, w http.ResponseWriter) {
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, map[string]any{"id": "900"})
	})
	body := tracker.MarkManagedComment("Done.")

	got, err := c.CreateCommentWithKey(t.Context(), "PROJ-1", testKey, body)

	require.NoError(t, err)
	assert.Equal(t, body+"\n\n"+tracker.CommentKeyMarker(testKey), got.Body)
}
