package hub

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/vnovick/itervox/internal/apitokens"
)

// The live terminal through the hub (#298 follow-up): a scoped, READ-ONLY
// pass-through of a member's tmux terminal.
//
//   - POST /hub/p/{project}/api/v1/sessions/{id}/terminal/ticket goes
//     through handleProxy's checks (the member's table declares it
//     operator, so a viewer hub token is refused there) and is then
//     brokered here, never proxied as is: a "write" mode is refused
//     (terminal_write_not_proxied), the member is asked for a read ticket
//     with the hub's OPERATOR member token (the hub never holds an admin
//     one) and the hub caller's name in hubCallerHeader. The member trusts
//     that header only on its KindHub token (internal/server/terminal_hub.go),
//     binds the ticket to that token and the name, and feeds the attach
//     as "hub:<name>". The hub remembers the ticket (hash only) with the
//     caller's hub token id and secret hash.
//   - GET /hub/p/{project}/api/v1/sessions/{id}/terminal?ticket=… is the
//     socket. A browser cannot send a bearer header on a WebSocket, so
//     this route sits outside the hub's bearer group (access class
//     hubTicket): the brokered ticket is its credential. It is spent on
//     first use and must name the same project (the same WORKFLOW.md) and
//     session; the caller's hub token is re-checked; the Origin must be
//     the hub's own or in its allowed origins. Only then does the hub open
//     the member's socket itself, with its operator member token in
//     Authorization (the member redeems a hub ticket only with it) and no
//     Origin, Cookie or Referer of the browser's. A ticket the hub did not
//     broker is refused here and never reaches the member.
//   - terminal_relay.go relays the frames: the member's as they are (a
//     4403 close reason gets the "member_" prefix), the browser's only
//     when they are a resize — typed input is dropped by the hub as well
//     as by the member. The caller's hub token is re-checked every
//     terminalRecheck and on every change of the hub token store; a token
//     revoked, expired, rotated or below operator closes the socket with
//     4403 and a "hub_…" reason.

// hubCallerHeader is internal/server.HubCallerHeader (the hub imports no
// server code; TestHubCallerHeaderMatchesServer pins the two).
const hubCallerHeader = "X-Itervox-Hub-Caller"

// Hub-side limits of the pass-through.
const (
	maxHubTerminals       = 16
	maxHubTerminalTickets = 256
	// hubTicketTTL caps a brokered ticket's life (the member's is 30 s).
	hubTicketTTL       = 30 * time.Second
	maxTicketBodyBytes = 4 << 10
)

// terminalRecheckInterval is how often an open pass-through re-checks the
// caller's hub token (besides every token store change). A variable so
// tests can shorten it before making their hub.
var terminalRecheckInterval = 30 * time.Second

// Brokered ticket errors.
var (
	errHubTicketInvalid = errors.New("hub: unknown or already used terminal ticket")
	errHubTicketExpired = errors.New("hub: the terminal ticket has expired")
	errHubTicketWrong   = errors.New("hub: the terminal ticket is for another project or session")
)

// hubTerminalTicket is a ticket the hub brokered.
type hubTerminalTicket struct {
	project, workflow, sessionID string
	// caller is the hub principal it was brokered for; tokenHash the hash
	// of the hub secret that asked (a rotation ends the terminal).
	caller    Principal
	tokenHash string
	expires   time.Time
}

// hubTerminalTickets holds the brokered tickets by SHA-256, in memory.
type hubTerminalTickets struct {
	mu     sync.Mutex
	byHash map[[32]byte]hubTerminalTicket
	now    func() time.Time
}

func newHubTerminalTickets() *hubTerminalTickets {
	return &hubTerminalTickets{byHash: map[[32]byte]hubTerminalTicket{}, now: time.Now}
}

// add records a brokered ticket; false when the store is full.
func (s *hubTerminalTickets) add(secret string, t hubTerminalTicket) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for h, old := range s.byHash {
		if !now.Before(old.expires) {
			delete(s.byHash, h)
		}
	}
	if len(s.byHash) >= maxHubTerminalTickets {
		return false
	}
	if limit := now.Add(hubTicketTTL); t.expires.IsZero() || t.expires.After(limit) {
		t.expires = limit
	}
	s.byHash[sha256.Sum256([]byte(secret))] = t
	return true
}

// redeem spends secret for project and session; a found ticket is removed
// whatever the outcome.
func (s *hubTerminalTickets) redeem(secret, project, sessionID string) (hubTerminalTicket, error) {
	h := sha256.Sum256([]byte(secret))
	s.mu.Lock()
	t, ok := s.byHash[h]
	delete(s.byHash, h)
	now := s.now()
	s.mu.Unlock()
	switch {
	case !ok:
		return hubTerminalTicket{}, errHubTicketInvalid
	case !now.Before(t.expires):
		return hubTerminalTicket{}, errHubTicketExpired
	case t.project != project || t.sessionID != sessionID:
		return hubTerminalTicket{}, errHubTicketWrong
	}
	return t, nil
}

// terminalPath splits /api/v1/sessions/{id}/terminal[/ticket]: the session
// id and whether it is the ticket route. ok is false for any other path.
func terminalPath(rest string) (id string, ticket, ok bool) {
	segs := strings.Split(strings.TrimPrefix(rest, "/api/v1/"), "/")
	if len(segs) < 3 || segs[0] != "sessions" || segs[2] != "terminal" || segs[1] == "" {
		return "", false, false
	}
	switch {
	case len(segs) == 3:
		return segs[1], false, true
	case len(segs) == 4 && segs[3] == "ticket":
		return segs[1], true, true
	}
	return "", false, false
}

// isTerminalTicketRequest is the one terminal request handleProxy brokers:
// a POST to the ticket route that is not an upgrade.
func isTerminalTicketRequest(r *http.Request, rest string) bool {
	if r.Method != http.MethodPost || isUpgrade(r) {
		return false
	}
	_, ticket, ok := terminalPath(rest)
	return ok && ticket
}

func isUpgrade(r *http.Request) bool {
	return r.Header.Get("Upgrade") != "" || strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade")
}

// memberTicket is the member's POST …/terminal/ticket answer.
type memberTicket struct {
	Ticket    string    `json:"ticket"`
	Mode      string    `json:"mode"`
	ExpiresAt time.Time `json:"expiresAt"`
	URL       string    `json:"url"`
}

// brokerTerminalTicket answers a ticket request handleProxy already
// checked (route declared, caller at least operator, member needs a token).
func (h *Hub) brokerTerminalTicket(w http.ResponseWriter, r *http.Request, m memberState, project, rest string, p Principal) {
	id, _, _ := terminalPath(rest)
	if !p.Scope.Allows(apitokens.ScopeOperator) {
		writeInsufficientScope(w, apitokens.ScopeOperator, p.Scope)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxTicketBodyBytes+1))
	if err != nil || len(raw) > maxTicketBodyBytes {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid body")
		return
	}
	var body struct {
		Mode string `json:"mode"`
	}
	if len(bytes.TrimSpace(raw)) > 0 && json.Unmarshal(raw, &body) != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid body")
		return
	}
	switch body.Mode {
	case "", "read":
	case "write":
		writeError(w, http.StatusForbidden, "terminal_write_not_proxied",
			"typing into the live terminal is never reachable through the hub; the hub's terminal is read-only — open the project's own dashboard to type")
		return
	default:
		writeError(w, http.StatusBadRequest, "bad_request", `mode must be "read" or "write"`)
		return
	}
	secret, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	rep, err := h.client.doHeaders(r.Context(), m.rec, apitokens.ScopeOperator, http.MethodPost, rest,
		[]byte(`{"mode":"read"}`), http.Header{hubCallerHeader: {p.Name}})
	if err != nil {
		writeError(w, http.StatusBadGateway, "member_unreachable", "the project's daemon did not answer")
		return
	}
	switch rep.status {
	case http.StatusCreated:
	case http.StatusUnauthorized:
		h.client.invalidate(m.rec, apitokens.ScopeOperator)
		w.Header().Set("X-Itervox-Hub-Error", "member_auth_failed")
		writeError(w, http.StatusBadGateway, "member_auth_failed", "the project refused the hub's token")
		return
	default:
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if v := rep.header.Get(RequiredScopeHeader); v != "" {
			w.Header().Set(RequiredScopeHeader, v)
		}
		w.WriteHeader(rep.status)
		_, _ = w.Write(rep.body)
		return
	}
	var t memberTicket
	wantURL := "/api/v1/sessions/" + url.PathEscape(id) + "/terminal?"
	if json.Unmarshal(rep.body, &t) != nil || t.Mode != "read" || t.Ticket == "" || !strings.HasPrefix(t.URL, wantURL) {
		writeError(w, http.StatusBadGateway, "member_bad_ticket", "the project answered with an unusable terminal ticket")
		return
	}
	if !h.terminals.add(t.Ticket, hubTerminalTicket{project: project, workflow: m.rec.Workflow, sessionID: id,
		caller: p, tokenHash: apitokens.HashSecret(secret), expires: t.ExpiresAt}) {
		writeError(w, http.StatusTooManyRequests, "too_many_terminals", "too many terminal tickets are pending; try again shortly")
		return
	}
	// The member's URL (/api/v1/…) is left as it is: the dashboard maps it
	// through its API base, /hub/p/{project}/api/v1 under the hub.
	writeJSON(w, http.StatusCreated, t)
}

// terminalAccessLost re-checks a brokered ticket's hub token and returns
// why it no longer holds a read-only terminal, or "" while it does.
func (h *Hub) terminalAccessLost(t hubTerminalTicket) string {
	list, err := h.tokens.List()
	if err != nil {
		return "hub_token_store_invalid"
	}
	for _, tok := range list {
		if tok.ID != t.caller.TokenID {
			continue
		}
		switch {
		case tok.Expired(time.Now()):
			return "hub_token_expired"
		case tok.Hash != t.tokenHash:
			return "hub_token_rotated"
		case !tok.Scope.Allows(apitokens.ScopeOperator):
			return "hub_scope_insufficient"
		}
		return ""
	}
	return "hub_token_revoked"
}
