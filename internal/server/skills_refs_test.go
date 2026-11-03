package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/skills"
)

func postCheckRefs(t *testing.T, s *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/skills/check-refs", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(rec, req)
	return rec
}

// TestSkillsCheckRefsReturnsUTF16Ranges: the adapter reports byte offsets;
// the route returns UTF-16 offsets (what the browser editor counts), the
// resolved backend, and doctor's message for a non-ok reference.
func TestSkillsCheckRefsReturnsUTF16Ranges(t *testing.T) {
	t.Parallel()
	text := "🚀 é Use the `ghost` skill."
	start := strings.Index(text, "ghost")
	fake := &fakeSkillsClient{refCheck: &PromptRefCheck{
		Backend: "codex",
		Findings: []skills.RefFinding{{
			PromptRefSpan: skills.PromptRefSpan{PromptRef: skills.PromptRef{Kind: "skill", Name: "ghost"}, Start: start, End: start + len("ghost")},
			Status:        skills.RefStatusMissing,
			Issue:         &skills.InventoryIssue{ID: "MISSING_SKILL_REF", Severity: "warn", Description: "not found"},
		}},
	}}
	s := newSkillsTestServer(t, fake)
	body, _ := json.Marshal(PromptRefCheckRequest{Profile: "p", Command: "codex", Backend: "codex", Text: text})
	rec := postCheckRefs(t, s, string(body))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, PromptRefCheckRequest{Profile: "p", Command: "codex", Backend: "codex", Text: text}, fake.lastRefReq)

	var resp promptRefCheckResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "codex", resp.Backend)
	assert.Equal(t, []string{}, resp.SSHHosts)
	require.Len(t, resp.Refs, 1)
	// "🚀" is 2 UTF-16 units (4 bytes), "é" is 1 (2 bytes).
	utf16Start := 2 + len(" ") + 1 + len(" Use the `")
	assert.Equal(t, promptRefRow{
		Kind: "skill", Name: "ghost", From: utf16Start, To: utf16Start + 5,
		Status: "missing", Severity: "warn", IssueID: "MISSING_SKILL_REF", Message: "not found",
	}, resp.Refs[0])
}

func TestSkillsCheckRefsUnavailableBeforeScan(t *testing.T) {
	t.Parallel()
	s := newSkillsTestServer(t, &fakeSkillsClient{})
	rec := postCheckRefs(t, s, `{"text":"Ask @agent-x."}`)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestSkillsCheckRefsRejectsHugePrompt(t *testing.T) {
	t.Parallel()
	s := newSkillsTestServer(t, &fakeSkillsClient{refCheck: &PromptRefCheck{Backend: "claude"}})
	body, _ := json.Marshal(PromptRefCheckRequest{Text: strings.Repeat("a", maxPromptRefCheckBytes+1)})
	rec := postCheckRefs(t, s, string(body))
	assert.NotEqual(t, http.StatusOK, rec.Code)
}

func TestSkillsProfileBackends(t *testing.T) {
	t.Parallel()
	s := newSkillsTestServer(t, &fakeSkillsClient{backends: map[string]string{"": "codex", "reviewer": "claude"}})
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/skills/profile-backends", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"default":"codex","profiles":{"reviewer":"claude"}}`, rec.Body.String())
}

func TestUTF16OffsetsRestartBackwards(t *testing.T) {
	t.Parallel()
	u := newUTF16Offsets("a🚀b")
	assert.Equal(t, 3, u.at(5))
	assert.Equal(t, 1, u.at(1))
	assert.Equal(t, 4, u.at(100))
}
