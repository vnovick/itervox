package hub

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/vnovick/itervox/internal/apitokens"
)

// Member health values in /hub/api/v1/projects.
const (
	healthOK           = "ok"
	healthUnreachable  = "unreachable"
	healthUnauthorized = "unauthorized"
	healthError        = "error"
)

// memberState is one member as the hub currently sees it.
type memberState struct {
	rec    MemberRecord
	name   string
	routes *routeTable
	// routesErr explains a missing route table (the member is then never
	// proxied: fail closed).
	routesErr    string
	health       string
	err          string
	summary      json.RawMessage
	etag         string
	version      string
	capabilities []string
	checkedAt    time.Time
}

// memberSummary is the part of a member's GET /api/v1/summary the hub reads.
type memberSummary struct {
	Version      string   `json:"version"`
	Capabilities []string `json:"capabilities"`
}

// memberSet is the hub's member list. refresh runs on the discovery
// goroutine only; readers take a copy under mu.
type memberSet struct {
	client *memberClient

	mu      sync.RWMutex
	members map[string]*memberState // by project name
	changed chan struct{}           // closed and replaced when the set changes
}

func newMemberSet(client *memberClient) *memberSet {
	return &memberSet{client: client, members: map[string]*memberState{}, changed: make(chan struct{})}
}

// refresh reconciles the set with records: new or restarted members get
// their route table, every member its summary (a conditional request).
func (s *memberSet) refresh(ctx context.Context, records []MemberRecord) {
	records = dedupeByWorkflow(records)
	names := assignNames(records)
	s.mu.RLock()
	prev := make(map[string]memberState, len(s.members))
	for _, m := range s.members {
		prev[m.rec.Workflow] = *m
	}
	s.mu.RUnlock()

	next := make(map[string]*memberState, len(records))
	for _, rec := range records {
		st := memberState{rec: rec, name: names[rec.Workflow]}
		if old, ok := prev[rec.Workflow]; ok && old.rec.sameProcess(rec) {
			st = old
			st.name = names[rec.Workflow]
		}
		if st.routes == nil {
			s.loadRoutes(ctx, &st)
			logMemberProblem(st)
		}
		s.loadSummary(ctx, &st)
		next[st.name] = &st
	}

	s.mu.Lock()
	changed := len(next) != len(s.members)
	for name, m := range next {
		if old, ok := s.members[name]; !ok || !old.rec.sameProcess(m.rec) || old.health != m.health || old.etag != m.etag {
			changed = true
		}
	}
	s.members = next
	if changed {
		close(s.changed)
		s.changed = make(chan struct{})
	}
	s.mu.Unlock()
}

// loadRoutes fetches the member's route scope table with a viewer token.
func (s *memberSet) loadRoutes(ctx context.Context, st *memberState) {
	rep, err := s.client.get(ctx, st.rec, apitokens.ScopeViewer, RouteScopesPath, nil)
	switch {
	case err != nil:
		st.routesErr = err.Error()
	case rep.status != http.StatusOK:
		st.routesErr = "GET " + RouteScopesPath + " answered " + http.StatusText(rep.status) + " (an itervox older than the hub?)"
	default:
		t, perr := parseRouteTable(rep.body)
		if perr != nil {
			st.routesErr = perr.Error()
			return
		}
		st.routes, st.routesErr = t, ""
	}
}

// loadSummary fetches GET /api/v1/summary, conditionally on the last ETag.
func (s *memberSet) loadSummary(ctx context.Context, st *memberState) {
	var hdr http.Header
	if st.etag != "" {
		hdr = http.Header{"If-None-Match": {`"` + st.etag + `"`}}
	}
	rep, err := s.client.get(ctx, st.rec, apitokens.ScopeViewer, "/api/v1/summary", hdr)
	st.checkedAt = time.Now()
	switch {
	case err != nil:
		st.health, st.err = healthUnreachable, err.Error()
	case rep.status == http.StatusNotModified:
		st.health, st.err = healthOK, ""
	case rep.status == http.StatusUnauthorized || rep.status == http.StatusForbidden:
		st.health, st.err = healthUnauthorized, "the member refused the hub's token"
	case rep.status != http.StatusOK:
		st.health, st.err = healthError, "GET /api/v1/summary answered "+http.StatusText(rep.status)
	default:
		var sum memberSummary
		if json.Unmarshal(rep.body, &sum) != nil {
			st.health, st.err = healthError, "the member's summary does not parse"
			return
		}
		st.health, st.err = healthOK, ""
		st.summary = json.RawMessage(slices.Clone(rep.body))
		st.version, st.capabilities = sum.Version, sum.Capabilities
		st.etag = strings.Trim(strings.TrimPrefix(rep.header.Get("ETag"), "W/"), `"`)
	}
}

// get returns a copy of the member named name.
func (s *memberSet) get(name string) (memberState, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	m, ok := s.members[name]
	if !ok {
		return memberState{}, false
	}
	return *m, true
}

// list returns a copy of every member, sorted by name.
func (s *memberSet) list() []memberState {
	s.mu.RLock()
	out := make([]memberState, 0, len(s.members))
	for _, m := range s.members {
		out = append(out, *m)
	}
	s.mu.RUnlock()
	slices.SortFunc(out, func(a, b memberState) int { return strings.Compare(a.name, b.name) })
	return out
}

// changes returns a channel closed at the next change of the set.
func (s *memberSet) changes() <-chan struct{} {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.changed
}

// discover keeps the member set current: at once, on every registry change
// (fsnotify, debounced) and on a safety rescan, until ctx ends.
func (h *Hub) discover(ctx context.Context) {
	wake := make(chan struct{}, 1)
	if w, err := fsnotify.NewWatcher(); err == nil {
		defer func() { _ = w.Close() }()
		if w.Add(h.cfg.RegistryDir) == nil {
			go func() {
				defer recoverHubGoroutine("hub-registry-watch")
				forwardEvents(ctx, w, func(name string) bool { return filepath.Ext(name) == ".json" }, wake)
			}()
		}
	}
	rescan := time.NewTicker(h.cfg.Rescan)
	defer rescan.Stop()
	for {
		h.members.refresh(ctx, h.cfg.Members())
		select {
		case <-ctx.Done():
			return
		case <-rescan.C:
		case <-wake:
			// Debounce: a daemon writes its record and then rewrites it.
			select {
			case <-ctx.Done():
				return
			case <-time.After(250 * time.Millisecond):
			}
		}
	}
}

// logMemberProblem tells an operator reading debug logs why a member is
// listed but not proxied.
func logMemberProblem(st memberState) {
	if st.routesErr != "" {
		slog.Debug("hub: member is listed but not proxied", "project", st.name, "reason", st.routesErr)
	}
}
