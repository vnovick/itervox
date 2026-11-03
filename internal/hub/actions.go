package hub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/vnovick/itervox/internal/apitokens"
)

// Interactive channels (#230). A Channel that also implements
// InteractiveChannel holds a connection of its own while this daemon
// serves the hub (Slack's Socket Mode), and acts on members for the people
// it answers through MemberActions: the member's own API, called with the
// hub's stored member token, behind the member's own route scope table.

// InteractiveChannel is a Channel the hub also runs: Run is started on its
// own goroutine when this daemon starts serving the hub and returns when
// ctx ends (the hub stops, or another daemon takes over).
type InteractiveChannel interface {
	Channel
	Run(ctx context.Context, members MemberActions)
}

// MemberRef names one member: its project name and the workflow path that
// identifies it for good (a project name can move to another member when
// members come and go; a workflow path cannot).
type MemberRef struct {
	Project  string
	Workflow string
}

// Key is a short, stable digest of the member's workflow path, for a
// signed action payload: an action signed for one member never reaches
// another that later takes its project name.
func (r MemberRef) Key() string {
	sum := sha256.Sum256([]byte(r.Workflow))
	return hex.EncodeToString(sum[:8])
}

// MemberActions is what an interactive channel may do on members.
type MemberActions interface {
	// Projects lists the current members, by project name.
	Projects() []MemberRef
	// Member returns the current member named project.
	Member(project string) (MemberRef, bool)
	// Call sends method path (a plain /api/v1/... path, its segments
	// already escaped) with body as JSON (nil: none) to ref's member, with
	// the hub's member token of the scope the member's route table declares
	// for it. It fails closed (ErrMemberActionRefused) for a member that is
	// gone or replaced, unauthenticated or without a route table, and for a
	// route the table does not declare as viewer or operator. The reply is
	// bounded; status is the member's.
	Call(ctx context.Context, ref MemberRef, method, path string, body any) (status int, reply []byte, err error)
	// AbsoluteURL turns a hub-relative dashboard link (Notification.URL)
	// into an absolute one on this hub.
	AbsoluteURL(link string) string
}

// ErrMemberActionRefused is a Call the hub refused before reaching the member.
var ErrMemberActionRefused = errors.New("hub: member action refused")

// hubActions implements MemberActions over a serving hub.
type hubActions struct{ h *Hub }

// Projects implements MemberActions.
func (a hubActions) Projects() []MemberRef {
	members := a.h.members.list()
	out := make([]MemberRef, 0, len(members))
	for _, m := range members {
		out = append(out, MemberRef{Project: m.name, Workflow: m.rec.Workflow})
	}
	return out
}

// Member implements MemberActions.
func (a hubActions) Member(project string) (MemberRef, bool) {
	m, ok := a.h.members.get(project)
	if !ok {
		return MemberRef{}, false
	}
	return MemberRef{Project: m.name, Workflow: m.rec.Workflow}, true
}

// Call implements MemberActions.
func (a hubActions) Call(ctx context.Context, ref MemberRef, method, path string, body any) (int, []byte, error) {
	m, ok := a.h.members.get(ref.Project)
	switch {
	case !ok || m.rec.Workflow != ref.Workflow:
		return 0, nil, fmt.Errorf("%w: project %q is no longer that member", ErrMemberActionRefused, ref.Project)
	case !m.rec.TokenRequired:
		return 0, nil, fmt.Errorf("%w: project %q runs unauthenticated", ErrMemberActionRefused, ref.Project)
	case m.routes == nil:
		return 0, nil, fmt.Errorf("%w: no route table for project %q", ErrMemberActionRefused, ref.Project)
	case !plainAPIPath(path):
		return 0, nil, fmt.Errorf("%w: not a plain /api/v1/ path", ErrMemberActionRefused)
	}
	access, ok := m.routes.match(method, path)
	if !ok {
		return 0, nil, fmt.Errorf("%w: the member declares no such route", ErrMemberActionRefused)
	}
	scope := apitokens.Scope(access)
	switch {
	case access == accessPublic && method == http.MethodGet:
		scope = apitokens.ScopeViewer
	case scope != apitokens.ScopeViewer && scope != apitokens.ScopeOperator:
		return 0, nil, fmt.Errorf("%w: the route needs %q", ErrMemberActionRefused, access)
	}
	var data []byte
	if body != nil {
		var err error
		if data, err = json.Marshal(body); err != nil {
			return 0, nil, fmt.Errorf("hub: member action body: %w", err)
		}
	}
	rep, err := a.h.client.do(ctx, m.rec, scope, method, path, data)
	if err != nil {
		return 0, nil, err
	}
	return rep.status, rep.body, nil
}

// AbsoluteURL implements MemberActions.
func (a hubActions) AbsoluteURL(link string) string {
	base, err := url.Parse(a.h.self.URL)
	if err != nil || base.Host == "" || !strings.HasPrefix(link, "/") {
		return link
	}
	return base.Scheme + "://" + base.Host + link
}

// plainAPIPath accepts /api/v1/... with no empty, "." or ".." segment and
// no query or fragment.
func plainAPIPath(path string) bool {
	if !strings.HasPrefix(path, "/api/v1/") || strings.ContainsAny(path, "?#") {
		return false
	}
	for _, seg := range strings.Split(path[1:], "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
		if u, err := url.PathUnescape(seg); err != nil || u == "." || u == ".." || strings.Contains(u, "/") {
			return false
		}
	}
	return true
}
