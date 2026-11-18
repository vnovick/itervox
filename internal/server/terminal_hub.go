package server

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// The live terminal through the itervox hub (#298 follow-up): a READ-ONLY
// pass-through. The hub (internal/hub/terminal.go) mints the ticket with
// its operator member token — a stored token of apitokens.KindHub, which
// only the hub's enrolment writes — and names the person behind the
// request in HubCallerHeader. This daemon:
//
//   - trusts that header ONLY on a KindHub principal (Principal.isHub);
//     from any other caller it is ignored, so nobody else can put a name
//     into the audit log or the feed;
//   - refuses a write ticket to a KindHub principal whatever its scope
//     (terminal_write_not_proxied): typing never goes through the hub;
//   - binds the ticket to the hub member token (principal + secret hash,
//     as for every ticket) and the hub caller's name, which becomes the
//     attach's actor, "hub:<name>", in the audit log and the feed;
//   - redeems such a ticket only with that same member token in the
//     socket request's Authorization header (ticketPrincipalMatches). A
//     browser cannot set that header on a WebSocket, so a hub ticket that
//     leaks to a browser is useless against this daemon directly. The hub
//     checks the browser's Origin against its own and drops it before
//     forwarding; this daemon's Origin check is unchanged for everyone.
//
// The open terminal's re-check (terminal_recheck.go) watches the hub
// member token like any other: revoking it closes the terminal with 4403,
// which the hub relays to the browser.

// HubCallerHeader names the person (the hub token's name) a request from
// the hub's member token is made for. internal/hub sends the same name
// (TestHubCallerHeaderMatchesServer pins it).
const HubCallerHeader = "X-Itervox-Hub-Caller"

// maxHubCallerLen bounds the hub caller's name (a hub token's name is at
// most 64 characters).
const maxHubCallerLen = 64

// hubCallerName validates the hub caller header: 1-64 printable characters,
// trimmed. ok is false for anything else.
func hubCallerName(raw string) (string, bool) {
	name := strings.TrimSpace(raw)
	if name == "" || !utf8.ValidString(name) || utf8.RuneCountInString(name) > maxHubCallerLen {
		return "", false
	}
	for _, r := range name {
		if !unicode.IsPrint(r) {
			return "", false
		}
	}
	return name, true
}
