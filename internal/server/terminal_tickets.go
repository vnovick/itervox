package server

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/vnovick/itervox/internal/apitokens"
)

// Terminal tickets (#298). A browser cannot send an Authorization header
// on a WebSocket, so the live terminal's socket is authenticated with a
// ticket instead: POST /api/v1/sessions/{id}/terminal/ticket (bearer auth,
// operator scope) mints one, and GET /api/v1/sessions/{id}/terminal (the
// socket, access class "ticket" in routeScopes) redeems it in its query.
//
// A ticket is 32 random bytes, base64url, prefixed "itvt_". It is:
//   - short-lived: terminalTicketTTL after minting;
//   - single-use: the first redemption removes it, whatever its outcome;
//   - bound to the session id and the mode (read or write) it was minted
//     for, and to the principal that minted it, whose token must still be
//     valid (and still hold the scope the mode needs) when it is redeemed.
//
// Only its SHA-256 is kept, in memory, per Server (a reload or restart
// drops every unredeemed ticket). The store is bounded: a full store
// refuses new tickets until old ones expire.

const (
	terminalTicketPrefix = "itvt_"
	terminalTicketTTL    = 30 * time.Second
	maxTerminalTickets   = 256
)

// Ticket redemption errors.
var (
	errTicketInvalid      = errors.New("server: unknown or already used terminal ticket")
	errTicketExpired      = errors.New("server: the terminal ticket has expired")
	errTicketWrongSession = errors.New("server: the terminal ticket is for another session")
	errTicketStoreFull    = errors.New("server: too many terminal tickets are pending")
)

// terminalTicket is what a ticket grants.
type terminalTicket struct {
	sessionID string
	write     bool
	principal Principal
	expires   time.Time
}

// requiredScope is the scope the ticket's mode needs: admin to type into
// the session, operator to watch it.
func (t terminalTicket) requiredScope() apitokens.Scope {
	if t.write {
		return apitokens.ScopeAdmin
	}
	return apitokens.ScopeOperator
}

// terminalTickets is the in-memory ticket store.
type terminalTickets struct {
	mu     sync.Mutex
	byHash map[[32]byte]terminalTicket
	now    func() time.Time
}

func newTerminalTickets() *terminalTickets {
	return &terminalTickets{byHash: map[[32]byte]terminalTicket{}, now: time.Now}
}

// mint stores a new ticket and returns its secret and expiry.
func (s *terminalTickets) mint(sessionID string, write bool, p Principal) (string, time.Time, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", time.Time{}, err
	}
	secret := terminalTicketPrefix + base64.RawURLEncoding.EncodeToString(b)
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for h, t := range s.byHash {
		if !now.Before(t.expires) {
			delete(s.byHash, h)
		}
	}
	if len(s.byHash) >= maxTerminalTickets {
		return "", time.Time{}, errTicketStoreFull
	}
	expires := now.Add(terminalTicketTTL)
	s.byHash[sha256.Sum256([]byte(secret))] = terminalTicket{sessionID: sessionID, write: write, principal: p, expires: expires}
	return secret, expires, nil
}

// redeem spends the ticket secret for sessionID. The ticket is removed on
// every lookup that finds it, so it can never be tried twice.
func (s *terminalTickets) redeem(secret, sessionID string) (terminalTicket, error) {
	if !strings.HasPrefix(secret, terminalTicketPrefix) {
		return terminalTicket{}, errTicketInvalid
	}
	h := sha256.Sum256([]byte(secret))
	s.mu.Lock()
	t, ok := s.byHash[h]
	delete(s.byHash, h)
	now := s.now()
	s.mu.Unlock()
	switch {
	case !ok:
		return terminalTicket{}, errTicketInvalid
	case !now.Before(t.expires):
		return terminalTicket{}, errTicketExpired
	case t.sessionID != sessionID:
		return terminalTicket{}, errTicketWrongSession
	}
	return t, nil
}

// samePrincipal reports whether a and b are the same caller.
func samePrincipal(a, b Principal) bool {
	return a.Source == b.Source && a.TokenID == b.TokenID
}

// principalStillAllows re-checks a ticket's principal when it is redeemed:
// a stored token must still exist, be unexpired and hold need; the
// bootstrap and unauthenticated principals hold for the server's life.
func (s *Server) principalStillAllows(p Principal, need apitokens.Scope) bool {
	switch p.Source {
	case principalBootstrap:
		return s.apiToken != "" && p.Scope.Allows(need)
	case principalUnauthenticated:
		return s.apiToken == "" && p.Scope.Allows(need)
	case principalToken:
		if s.tokens == nil || p.TokenID == "" {
			return false
		}
		list, err := s.tokens.List()
		if err != nil {
			return false
		}
		now := time.Now()
		for _, t := range list {
			if t.ID == p.TokenID {
				return !t.Expired(now) && t.Scope.Allows(need)
			}
		}
	}
	return false
}
