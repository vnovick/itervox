package jira_test

import (
	"encoding/json"
	"maps"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/tracker"
)

const testKey = "0b7c3f9e-5a41-4d2b-9c6e-1f2a3b4c5d6e"

// keyedFake is a Jira comment store for one issue that behaves like the live
// site for comment properties: it keeps the properties sent with a create, the
// create response and the comment pages carry none, and
// POST /rest/api/3/comment/list?expand=properties gives each requested comment
// its properties list, empty when it has none. An id it does not know is left
// out of that response. The fault fields make one part of a read fail.
type keyedFake struct {
	mu       sync.Mutex
	comments []map[string]any
	posts    int       // create requests
	listIDs  [][]int64 // the ids of each comment/list request

	ignoreStartAt bool   // every comment page is the first page
	omitTotal     bool   // comment pages have no total
	failPageAt    string // the startAt of a comment page that fails with 502
	failList      bool   // comment/list fails with 502
	listLeavesOut string // a comment id comment/list leaves out
	listNoProps   string // a comment id comment/list returns without a properties list
}

// add stores a comment with text and, when propKey is not empty, a property
// propKey whose value is {"key": key}. It returns the new comment id.
func (f *keyedFake) add(text, propKey, key string) string {
	if propKey == "" {
		return f.addWithProperties(text)
	}
	return f.addWithProperties(text, map[string]any{"key": propKey, "value": map[string]any{"key": key}})
}

// addWithProperties stores a comment with text and the given properties, each
// {"key": name, "value": ...} as Jira lists them. It returns the new comment
// id.
func (f *keyedFake) addWithProperties(text string, props ...any) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := strconv.Itoa(1000 + len(f.comments))
	cm := commentFixture(id, "2024-03-01T00:00:00.000+0000", text)
	if len(props) > 0 {
		cm["properties"] = props
	}
	f.comments = append(f.comments, cm)
	return id
}

func (f *keyedFake) postCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.posts
}

func (f *keyedFake) listRequests() [][]int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]int64(nil), f.listIDs...)
}

func (f *keyedFake) handler(_ int, r recorded, w http.ResponseWriter) {
	f.mu.Lock()
	defer f.mu.Unlock()
	q, _ := url.ParseQuery(r.Query)
	switch {
	case r.Method == "POST" && r.Path == "/rest/api/3/comment/list":
		f.answerList(r, q, w)
	case r.Method == "POST" && strings.HasSuffix(r.Path, "/comment"):
		var in map[string]any
		_ = json.Unmarshal([]byte(r.Body), &in)
		f.posts++
		id := strconv.Itoa(1000 + len(f.comments))
		cm := commentFixture(id, "2024-03-01T00:00:00.000+0000", "")
		cm["body"] = in["body"]
		if props, ok := in["properties"]; ok {
			cm["properties"] = props
		}
		f.comments = append(f.comments, cm)
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, withoutProperties(cm))
	case r.Method == "GET" && strings.HasSuffix(r.Path, "/comment"):
		if f.failPageAt != "" && q.Get("startAt") == f.failPageAt {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		start, _ := strconv.Atoi(q.Get("startAt"))
		if f.ignoreStartAt {
			start = 0
		}
		size, _ := strconv.Atoi(q.Get("maxResults"))
		page := []any{}
		for i := start; i < len(f.comments) && i < start+size; i++ {
			page = append(page, withoutProperties(f.comments[i]))
		}
		resp := map[string]any{"startAt": start, "maxResults": size, "comments": page}
		if !f.omitTotal {
			resp["total"] = len(f.comments)
		}
		writeJSON(w, resp)
	default:
		writeJSON(w, issueFixture("10001", "PROJ-1", "To Do"))
	}
}

func (f *keyedFake) answerList(r recorded, q url.Values, w http.ResponseWriter) {
	if f.failList {
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	var in struct {
		IDs []int64 `json:"ids"`
	}
	_ = json.Unmarshal([]byte(r.Body), &in)
	f.listIDs = append(f.listIDs, in.IDs)
	values := []any{}
	for _, id := range in.IDs {
		sid := strconv.FormatInt(id, 10)
		for _, stored := range f.comments {
			if stored["id"] != sid || sid == f.listLeavesOut {
				continue
			}
			cm := withoutProperties(stored)
			if q.Get("expand") == "properties" && sid != f.listNoProps {
				cm["properties"] = []any{}
				if props, ok := stored["properties"]; ok {
					cm["properties"] = props
				}
			}
			values = append(values, cm)
		}
	}
	writeJSON(w, map[string]any{"isLast": true, "startAt": 0, "maxResults": 1048576, "total": len(values), "values": values})
}

func withoutProperties(cm map[string]any) map[string]any {
	out := maps.Clone(cm)
	delete(out, "properties")
	return out
}

// listRequest asserts that r is a property read and returns the ids it sent.
func listRequest(t *testing.T, r recorded) []int64 {
	t.Helper()
	assert.Equal(t, "POST /rest/api/3/comment/list", r.Method+" "+r.Path)
	q, _ := url.ParseQuery(r.Query)
	assert.Equal(t, "properties", q.Get("expand"))
	var in struct {
		IDs []int64 `json:"ids"`
	}
	require.NoError(t, json.Unmarshal([]byte(r.Body), &in))
	return in.IDs
}

// commentPageRequest asserts that r reads a comment page without the expand
// parameter, which Atlassian does not document for properties there.
func commentPageRequest(t *testing.T, r recorded) url.Values {
	t.Helper()
	assert.Equal(t, "GET /rest/api/3/issue/PROJ-1/comment", r.Method+" "+r.Path)
	q, _ := url.ParseQuery(r.Query)
	assert.False(t, q.Has("expand"), "no expand on the comment list")
	return q
}

func TestCreateCommentWithKeySendsKeyAsCommentProperty(t *testing.T) {
	f := &keyedFake{}
	c, seen := newJiraServer(t, f.handler)

	got, err := c.CreateCommentWithKey(t.Context(), "PROJ-1", testKey, "question text")

	require.NoError(t, err)
	reqs := seen()
	require.Len(t, reqs, 1, "one create request and no lookup")
	assert.Equal(t, "POST", reqs[0].Method)
	assert.Equal(t, "/rest/api/3/issue/PROJ-1/comment", reqs[0].Path)
	assert.JSONEq(t, `{
		"body":{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"question text"}]}]},
		"properties":[{"key":"itervox.commentKey","value":{"key":"`+testKey+`"}}]}`, reqs[0].Body)
	assert.NotContains(t, reqs[0].Body, "itervox:ck", "the key is not written into the text people read")
	require.NotNil(t, got)
	assert.Equal(t, "1000", got.ID)
	assert.True(t, tracker.CommentHasKey(got.Body, testKey), "the returned comment matches its key the way the orchestrator matches it")
}

func TestFindCommentByKeyReturnsStoredCommentAndPostsNothing(t *testing.T) {
	f := &keyedFake{}
	c, seen := newJiraServer(t, f.handler)
	f.add("older human comment", "", "")
	created, err := c.CreateCommentWithKey(t.Context(), "PROJ-1", testKey, "question")
	require.NoError(t, err)
	f.add("newer human comment", "", "")

	got, found, err := c.FindCommentByKey(t.Context(), "PROJ-1", testKey)

	require.NoError(t, err)
	require.True(t, found)
	require.NotNil(t, got)
	assert.Equal(t, created.ID, got.ID)
	assert.True(t, tracker.CommentHasKey(got.Body, testKey))
	assert.Equal(t, 1, f.postCount(), "the lookup creates nothing")
	reqs := seen()
	require.Len(t, reqs, 3, "the create, one comment page, one property read")
	commentPageRequest(t, reqs[1])
	assert.Equal(t, []int64{1000, 1001, 1002}, listRequest(t, reqs[2]))
}

func TestFindCommentByKeyNotFoundAfterCompleteRead(t *testing.T) {
	f := &keyedFake{}
	c, seen := newJiraServer(t, f.handler)
	c.SetCommentPageSize(2)
	f.add("a", "", "")
	f.add("b", "itervox.commentKey", "another-key")
	f.add("c", "other.app", testKey) // the right value under another property name
	f.add("d", "", "")
	f.add("e", "", "")

	got, found, err := c.FindCommentByKey(t.Context(), "PROJ-1", testKey)

	require.NoError(t, err)
	assert.False(t, found)
	assert.Nil(t, got)
	reqs := seen()
	require.Len(t, reqs, 4, "pages at startAt 0, 2 and 4, then one property read")
	for i, want := range []string{"0", "2", "4"} {
		assert.Equal(t, want, commentPageRequest(t, reqs[i]).Get("startAt"))
	}
	assert.Equal(t, []int64{1000, 1001, 1002, 1003, 1004}, listRequest(t, reqs[3]))
}

func TestFindCommentByKeyReadsEveryPage(t *testing.T) {
	f := &keyedFake{}
	c, seen := newJiraServer(t, f.handler)
	c.SetCommentPageSize(2)
	for _, text := range []string{"a", "b", "c", "d"} {
		f.add(text, "", "")
	}
	want := f.add("question on the last page", "itervox.commentKey", testKey)

	got, found, err := c.FindCommentByKey(t.Context(), "PROJ-1", testKey)

	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, want, got.ID)
	assert.Len(t, seen(), 4)
}

func TestFindCommentByKeyErrorsWhenAPageFails(t *testing.T) {
	f := &keyedFake{failPageAt: "2"}
	c, _ := newJiraServer(t, f.handler)
	c.SetCommentPageSize(2)
	for _, text := range []string{"a", "b", "c", "d"} {
		f.add(text, "", "")
	}

	got, found, err := c.FindCommentByKey(t.Context(), "PROJ-1", testKey)

	require.Error(t, err, "a failed page makes the answer unknown, never not-found")
	assert.False(t, found)
	assert.Nil(t, got)
	assert.Contains(t, err.Error(), "jira_find_comment_by_key")
}

func TestFindCommentByKeyErrorsWhenPagingStopsEarly(t *testing.T) {
	cases := []struct {
		name      string
		fake      *keyedFake
		keyAt     int // index of the keyed comment, or -1
		wantFound bool
	}{
		{"server ignores startAt", &keyedFake{ignoreStartAt: true}, -1, false},
		{"page without total", &keyedFake{omitTotal: true}, -1, false},
		{"a match on a short read still counts", &keyedFake{ignoreStartAt: true}, 1, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := newJiraServer(t, tc.fake.handler)
			c.SetCommentPageSize(2)
			for i, text := range []string{"a", "b", "c", "d"} {
				if i == tc.keyAt {
					tc.fake.add(text, "itervox.commentKey", testKey)
				} else {
					tc.fake.add(text, "", "")
				}
			}

			got, found, err := c.FindCommentByKey(t.Context(), "PROJ-1", testKey)

			if tc.wantFound {
				require.NoError(t, err)
				require.True(t, found)
				assert.Equal(t, "1001", got.ID)
				return
			}
			require.Error(t, err, "a read that ends before the total is unknown, never not-found")
			assert.False(t, found)
			assert.Nil(t, got)
			assert.Contains(t, err.Error(), "jira_find_comment_by_key")
		})
	}
}

func TestFindCommentByKeyErrorsWhenPropertiesAreUnknown(t *testing.T) {
	cases := []struct {
		name  string
		fake  *keyedFake
		badID bool // the second comment has an id that is not a number
	}{
		{"the property read fails", &keyedFake{failList: true}, false},
		{"a comment is left out of the property read", &keyedFake{listLeavesOut: "1001"}, false},
		{"a comment comes back without a properties list", &keyedFake{listNoProps: "1001"}, false},
		{"a comment id is not a number", &keyedFake{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := newJiraServer(t, tc.fake.handler)
			for _, text := range []string{"a", "b", "c"} {
				tc.fake.add(text, "", "")
			}
			if tc.badID {
				tc.fake.comments[1]["id"] = "x1001"
			}

			got, found, err := c.FindCommentByKey(t.Context(), "PROJ-1", testKey)

			require.Error(t, err, "a comment whose properties are unknown could hold the key")
			assert.False(t, found)
			assert.Nil(t, got)
			assert.Contains(t, err.Error(), "jira_find_comment_by_key")
		})
	}
}

// Atlassian accepts at most 1000 ids in one comment/list request.
func TestCommentPropertiesAreReadInChunksOf1000(t *testing.T) {
	f := &keyedFake{}
	c, _ := newJiraServer(t, f.handler)
	c.SetCommentPageSize(1001)
	for range 1000 {
		f.add("x", "", "")
	}
	want := f.add("question in the second chunk", "itervox.commentKey", testKey)

	got, found, err := c.FindCommentByKey(t.Context(), "PROJ-1", testKey)

	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, want, got.ID)
	lists := f.listRequests()
	require.Len(t, lists, 2)
	assert.Len(t, lists[0], 1000)
	assert.Equal(t, []int64{2000}, lists[1])
}

func TestKeyedCommentsRejectEmptyKeyOrBody(t *testing.T) {
	c, seen := newJiraServer(t, emptySearch)
	ctx := t.Context()

	for _, key := range []string{"", " \t"} {
		_, err := c.CreateCommentWithKey(ctx, "PROJ-1", key, "body")
		require.Error(t, err)
		assert.True(t, strings.HasPrefix(err.Error(), "jira_create_comment_with_key: empty key"), err.Error())

		got, found, err := c.FindCommentByKey(ctx, "PROJ-1", key)
		require.Error(t, err)
		assert.False(t, found)
		assert.Nil(t, got)
		assert.True(t, strings.HasPrefix(err.Error(), "jira_find_comment_by_key: empty key"), err.Error())
	}
	_, err := c.CreateCommentWithKey(ctx, "PROJ-1", testKey, " \n\t ")
	require.Error(t, err)
	assert.True(t, strings.HasPrefix(err.Error(), "jira_create_comment_with_key: empty body"), err.Error())

	assert.Empty(t, seen())
}

// The orchestrator finds a keyed question among FetchIssueDetail's comments
// with tracker.CommentHasKey. Jira keeps the key in a comment property, so the
// adapter must turn that property into the marker on read.
func TestFetchIssueDetailMarksKeyedComments(t *testing.T) {
	f := &keyedFake{}
	c, seen := newJiraServer(t, f.handler)
	f.add("human reply", "", "")
	f.add("question", "itervox.commentKey", testKey)
	f.add("other app", "other.app", testKey)

	got, err := c.FetchIssueDetail(t.Context(), "PROJ-1")

	require.NoError(t, err)
	require.Len(t, got.Comments, 3)
	assert.Equal(t, "human reply", got.Comments[0].Body)
	assert.Equal(t, "question\n\n"+tracker.CommentKeyMarker(testKey), got.Comments[1].Body)
	assert.True(t, tracker.CommentHasKey(got.Comments[1].Body, testKey))
	assert.Equal(t, "other app", got.Comments[2].Body, "only the itervox.commentKey property is a key")
	reqs := seen()
	require.Len(t, reqs, 3, "the issue, one comment page, one property read")
	commentPageRequest(t, reqs[1])
	assert.Equal(t, []int64{1000, 1001, 1002}, listRequest(t, reqs[2]))
}

func TestFetchIssueDetailWithoutCommentsSendsNoPropertyRequest(t *testing.T) {
	f := &keyedFake{}
	c, seen := newJiraServer(t, f.handler)

	got, err := c.FetchIssueDetail(t.Context(), "PROJ-1")

	require.NoError(t, err)
	assert.Empty(t, got.Comments)
	reqs := seen()
	require.Len(t, reqs, 2, "the issue and one comment page")
	commentPageRequest(t, reqs[1])
	assert.Empty(t, f.listRequests())
}

// The managed marker of a comment comes from its properties only, so a
// comment whose properties are unknown could be Itervox's own comment
// without its marker. Read as a person's comment after the agent's question,
// it would make the agent resume on Itervox's own text. So the detail read
// fails, the same way it fails when the property request fails, and the
// daemon reads again on the next poll. In each case the comment whose
// properties are unknown is an Itervox comment.
func TestFetchIssueDetailFailsWhenPropertiesAreUnknown(t *testing.T) {
	cases := []struct {
		name  string
		fake  *keyedFake
		badID bool // the Itervox comment has an id that is not a number
	}{
		{"the property read fails", &keyedFake{failList: true}, false},
		{"a comment is left out of the property read", &keyedFake{listLeavesOut: "1001"}, false},
		{"a comment comes back without a properties list", &keyedFake{listNoProps: "1001"}, false},
		{"a comment id is not a number", &keyedFake{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := newJiraServer(t, tc.fake.handler)
			tc.fake.add("question", "", "")
			tc.fake.addWithProperties("Itervox's reply", map[string]any{"key": "itervox.managed", "value": map[string]any{"managed": true}})
			if tc.badID {
				tc.fake.comments[1]["id"] = "x1001"
			}

			got, err := c.FetchIssueDetail(t.Context(), "PROJ-1")

			require.Error(t, err, "Itervox's reply would read as a person's reply, without its marker: %+v", got)
			assert.Nil(t, got)
			assert.Contains(t, err.Error(), "jira_fetch_comments")
		})
	}
}
