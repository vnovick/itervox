package orchestrator

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

const testKey = "11111111-1111-4111-8111-111111111111"

func TestFindTrackedQuestionCommentByKeyLinearStyle(t *testing.T) {
	comments := []domain.Comment{
		{ID: "other", Body: "unrelated"},
		{ID: testKey, Body: itervoxCommentPrefix + "\n\nWhich file?"},
	}
	idx, c, ok := findTrackedQuestionComment(comments, &InputRequiredEntry{QuestionCommentKey: testKey})
	require.True(t, ok)
	assert.Equal(t, 1, idx)
	assert.Equal(t, testKey, c.ID)
}

func TestFindTrackedQuestionCommentByKeyMarker(t *testing.T) {
	comments := []domain.Comment{
		{ID: "901", Body: tracker.MarkCommentKey(itervoxCommentPrefix+"\n\nWhich file?", testKey)},
	}
	idx, _, ok := findTrackedQuestionComment(comments, &InputRequiredEntry{QuestionCommentKey: testKey})
	require.True(t, ok)
	assert.Equal(t, 0, idx)
}

func TestFindTrackedQuestionCommentLegacyFallbacks(t *testing.T) {
	comments := []domain.Comment{
		{ID: "c1", Body: itervoxCommentPrefix + "\n\nold"},
		{ID: "c2", Body: itervoxCommentPrefix + "\n\nnew"},
	}
	// No key, id recorded -> id match.
	idx, _, ok := findTrackedQuestionComment(comments, &InputRequiredEntry{QuestionCommentID: "c1"})
	require.True(t, ok)
	assert.Equal(t, 0, idx)
	// No key, no id -> latest prefix match.
	idx, _, ok = findTrackedQuestionComment(comments, &InputRequiredEntry{})
	require.True(t, ok)
	assert.Equal(t, 1, idx)
}

// The regression this design exists to prevent: a keyed question that is still
// pending in the outbox must NOT be answered by a previous round's question.
func TestKeyedQuestionNeverFallsBackToOlderPrefixMatch(t *testing.T) {
	comments := []domain.Comment{
		{ID: "old-q", Body: itervoxCommentPrefix + "\n\nprevious round", AuthorID: "bot"},
		{ID: "old-reply", Body: "answer to the previous round", AuthorID: "human"},
	}
	_, _, ok := findTrackedQuestionComment(comments, &InputRequiredEntry{
		QuestionCommentKey: testKey, // not on the tracker yet
		QuestionCommentID:  "old-q", // even a stale id must not rescue it
	})
	assert.False(t, ok, "a key is authoritative: not found means wait")
}

func TestPendingResumeRoundTripCarriesQuestionCommentKey(t *testing.T) {
	entry := &InputRequiredEntry{IssueID: "id1", Identifier: "ENG-1", QuestionCommentKey: testKey}
	pending := buildPendingInputResumeEntry(entry, "reply")
	require.Equal(t, testKey, pending.QuestionCommentKey)
	assert.Equal(t, testKey, inputRequiredEntryFromPending(pending).QuestionCommentKey)
}

// A Linear comment authored by an OAuth application carries `user: null`,
// so both AuthorID and AuthorName are empty. sameCommentAuthor returns false
// when both sides are empty (it cannot positively confirm same-author), so
// without an explicit managed-comment check, Itervox's own dashboard-reply
// comment — which also has empty author fields in that scenario — would be
// mistaken for a genuine human answer and the agent would self-resume.
// findReplyAfterQuestion must skip anything IsManagedComment recognizes,
// regardless of author fields, and keep scanning for the real reply.
func TestFindReplySkipsManagedCommentWithoutAuthor(t *testing.T) {
	question := domain.Comment{
		ID:   "q1",
		Body: itervoxCommentPrefix + "\n\nWhich file?",
		// AuthorID/AuthorName intentionally empty.
	}
	managedReply := domain.Comment{
		ID:   "m1",
		Body: tracker.MarkManagedComment("Edit main.go"),
		// AuthorID/AuthorName intentionally empty — same as the question.
	}
	humanReply := domain.Comment{
		ID:       "h1",
		Body:     "real answer",
		AuthorID: "human-1",
	}

	comments := []domain.Comment{question, managedReply, humanReply}
	reply, found := findReplyAfterQuestion(comments, 0, question)
	require.True(t, found)
	assert.Equal(t, "real answer", reply.Body)

	// With only the question and the managed comment, there is no human
	// reply yet — must not be found.
	comments = []domain.Comment{question, managedReply}
	_, found = findReplyAfterQuestion(comments, 0, question)
	assert.False(t, found)
}

func TestInputRequiredKeyPersistsAcrossRestart(t *testing.T) {
	path := t.TempDir() + "/input_required.json"
	cfg := testConfig()
	mt := tracker.NewMemoryTracker(nil, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)

	first := New(cfg, mt, &blockedRunner{}, nil)
	first.SetInputRequiredFile(path)
	st := NewState(cfg)
	st.InputRequiredIssues["ENG-1"] = &InputRequiredEntry{
		IssueID: "id1", Identifier: "ENG-1", Context: "q", QuestionCommentKey: testKey,
	}
	st.PendingInputResumes["ENG-2"] = &PendingInputResumeEntry{
		IssueID: "id2", Identifier: "ENG-2", UserMessage: "r", QuestionCommentKey: testKey,
	}
	first.saveInputRequiredToDisk(st.InputRequiredIssues, st.PendingInputResumes)

	second := New(cfg, mt, &blockedRunner{}, nil)
	second.SetInputRequiredFile(path)
	loaded := second.loadInputRequiredFromDisk(NewState(cfg))

	require.Contains(t, loaded.InputRequiredIssues, "ENG-1")
	assert.Equal(t, testKey, loaded.InputRequiredIssues["ENG-1"].QuestionCommentKey)
	require.Contains(t, loaded.PendingInputResumes, "ENG-2")
	assert.Equal(t, testKey, loaded.PendingInputResumes["ENG-2"].QuestionCommentKey)
}

// A file written before question_comment_key existed (or one from a build
// where key generation fell back to "" — see newInputRequiredCommentKey)
// must still load and must still resolve the tracked question, through the
// legacy QuestionCommentID branch of findTrackedQuestionComment.
func TestLegacyInputRequiredFileWithoutKeyResolvesByID(t *testing.T) {
	path := t.TempDir() + "/input_required.json"
	cfg := testConfig()
	mt := tracker.NewMemoryTracker(nil, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)

	first := New(cfg, mt, &blockedRunner{}, nil)
	first.SetInputRequiredFile(path)
	st := NewState(cfg)
	st.InputRequiredIssues["ENG-1"] = &InputRequiredEntry{
		IssueID: "id1", Identifier: "ENG-1", Context: "q",
		QuestionCommentKey: testKey, QuestionCommentID: "c-old",
	}
	first.saveInputRequiredToDisk(st.InputRequiredIssues, nil)

	// Strip question_comment_key from the on-disk JSON to simulate a file
	// written before keys existed — the simplest reliable way to get the
	// legacy on-disk shape without hand-rolling the whole envelope.
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var envelope map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &envelope))
	var awaiting map[string]map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(envelope["awaiting"], &awaiting))
	delete(awaiting["ENG-1"], "question_comment_key")
	awaitingBytes, err := json.Marshal(awaiting)
	require.NoError(t, err)
	envelope["awaiting"] = awaitingBytes
	stripped, err := json.Marshal(envelope)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, stripped, 0o644))

	second := New(cfg, mt, &blockedRunner{}, nil)
	second.SetInputRequiredFile(path)
	loaded := second.loadInputRequiredFromDisk(NewState(cfg))

	require.Contains(t, loaded.InputRequiredIssues, "ENG-1")
	entry := loaded.InputRequiredIssues["ENG-1"]
	assert.Equal(t, "", entry.QuestionCommentKey)
	assert.Equal(t, "c-old", entry.QuestionCommentID)

	comments := []domain.Comment{
		{ID: "c-old", Body: itervoxCommentPrefix + "\n\nWhich file?"},
	}
	idx, c, ok := findTrackedQuestionComment(comments, entry)
	require.True(t, ok, "legacy entry without a key must resolve via QuestionCommentID")
	assert.Equal(t, 0, idx)
	assert.Equal(t, "c-old", c.ID)
}
