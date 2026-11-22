package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/apitokens"
)

// hubTicket POSTs a ticket request with a hub caller header.
func (r terminalRig) hubTicket(t *testing.T, token, mode, caller string) (*httptest.ResponseRecorder, terminalTicketResponse) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/itervox-abc123/terminal/ticket", strings.NewReader(`{"mode":"`+mode+`"}`))
	req.Host = "127.0.0.1:8090"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	if caller != "" {
		req.Header.Set(HubCallerHeader, caller)
	}
	rec := httptest.NewRecorder()
	r.srv.ServeHTTP(rec, req)
	var body terminalTicketResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec, body
}

// #298 hub pass-through, member side: the hub caller header is trusted only
// on the hub's own member token (KindHub), whose tickets are read-only,
// name the person ("hub:<name>") and are redeemed only with that token.
func TestHubMemberTokenTicketIsReadOnlyAndNamesTheCaller(t *testing.T) {
	r := newTerminalRig(t, true)
	_, hubOp, err := r.store.CreateKind("hub-operator", apitokens.ScopeOperator, apitokens.KindHub, "test")
	require.NoError(t, err)
	_, hubAdmin, err := r.store.CreateKind("hub-admin", apitokens.ScopeAdmin, apitokens.KindHub, "test")
	require.NoError(t, err)

	// A hub token never gets a write ticket, whatever its scope.
	rec, _ := r.hubTicket(t, hubAdmin, "write", "alice")
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Equal(t, "terminal_write_not_proxied", errCode(t, rec))
	// It must say whom the ticket is for, in a sane name.
	rec, _ = r.hubTicket(t, hubOp, "read", "")
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "hub_caller_required", errCode(t, rec))
	rec, _ = r.hubTicket(t, hubOp, "read", "bad\x01name")
	assert.Equal(t, "hub_caller_required", errCode(t, rec))

	// Any other token's header is ignored: the feed names the token.
	rec, body := r.hubTicket(t, r.secrets[apitokens.ScopeOperator], "read", "mallory")
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	ts := httptest.NewServer(r.srv)
	defer ts.Close()
	ws, err := dialTerminal(t, ts, body.URL, "", "")
	require.NoError(t, err)
	readUntil(t, ws, "hello")
	_ = ws.Close()
	reqs := r.sessions.attachedReqs()
	require.Len(t, reqs, 1)
	assert.True(t, strings.HasPrefix(reqs[0].Actor, "dashboard:"), reqs[0].Actor)

	// A hub ticket without the hub's token on the socket: refused (a
	// browser holding it cannot use it against the member directly).
	rec, body = r.hubTicket(t, hubOp, "read", "alice")
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.Equal(t, "read", body.Mode)
	_, err = dialTerminal(t, ts, body.URL, "", "")
	require.Error(t, err)
	assert.Len(t, r.sessions.attachedReqs(), 1, "refused before attaching")

	// With it, the attach is read-only and names the person.
	rec, body = r.hubTicket(t, hubOp, "read", "alice")
	require.Equal(t, http.StatusCreated, rec.Code)
	ws, err = dialTerminal(t, ts, body.URL, "", hubOp)
	require.NoError(t, err)
	readUntil(t, ws, "hello")
	_ = ws.Close()
	reqs = r.sessions.attachedReqs()
	require.Len(t, reqs, 2)
	assert.Equal(t, "hub:alice", reqs[1].Actor)
	assert.False(t, reqs[1].Write)

	// A foreign Origin is still refused on the member, hub ticket or not.
	rec, body = r.hubTicket(t, hubOp, "read", "alice")
	require.Equal(t, http.StatusCreated, rec.Code)
	_, err = dialTerminal(t, ts, body.URL, "https://evil.example", hubOp)
	require.Error(t, err)
	assert.Len(t, r.sessions.attachedReqs(), 2)
}

func TestHubCallerName(t *testing.T) {
	for raw, want := range map[string]string{"alice": "alice", "  Bob Smith ": "Bob Smith", "": "", "a\nb": "", strings.Repeat("x", 65): ""} {
		got, ok := hubCallerName(raw)
		assert.Equal(t, want, got, "%q", raw)
		assert.Equal(t, want != "", ok, "%q", raw)
	}
}
