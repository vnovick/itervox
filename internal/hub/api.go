package hub

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// ProjectRow is one member in GET /hub/api/v1/projects.
type ProjectRow struct {
	// Name is the unique project name, the /hub/p/{project} segment.
	Name       string `json:"name"`
	Workflow   string `json:"workflow"`
	ProjectDir string `json:"projectDir"`
	PID        int    `json:"pid"`
	// URL is the member's own dashboard URL.
	URL       string `json:"url"`
	Version   string `json:"version"`
	StartedAt string `json:"startedAt"`
	// Health is "ok", "unreachable", "unauthorized" or "error"; Error says why.
	Health string `json:"health"`
	Error  string `json:"error,omitempty"`
	// Proxied reports whether /hub/p/{name}/api/v1/... reaches the member;
	// ProxyError says why not.
	Proxied      bool     `json:"proxied"`
	ProxyError   string   `json:"proxyError,omitempty"`
	Capabilities []string `json:"capabilities"`
	// Summary is the member's GET /api/v1/summary, as it sent it.
	Summary   json.RawMessage `json:"summary,omitempty"`
	CheckedAt time.Time       `json:"checkedAt,omitzero"`
}

// HubInfo describes the serving hub in GET /hub/api/v1/projects.
type HubInfo struct {
	Version   string `json:"version"`
	PID       int    `json:"pid"`
	URL       string `json:"url"`
	StartedAt string `json:"startedAt"`
	HeadSeq   int64  `json:"headSeq"`
}

// ProjectsResponse is the body of GET /hub/api/v1/projects.
type ProjectsResponse struct {
	Hub      HubInfo      `json:"hub"`
	Projects []ProjectRow `json:"projects"`
}

func (h *Hub) projectRows() []ProjectRow {
	members := h.members.list()
	rows := make([]ProjectRow, 0, len(members))
	for _, m := range members {
		row := ProjectRow{
			Name: m.name, Workflow: m.rec.Workflow, ProjectDir: m.rec.ProjectDir, PID: m.rec.PID,
			URL: m.rec.URL, Version: m.rec.Version, StartedAt: m.rec.StartedAt,
			Health: m.health, Error: m.err, Capabilities: m.capabilities, Summary: m.summary, CheckedAt: m.checkedAt,
			Proxied: m.routes != nil && m.rec.TokenRequired,
		}
		if m.version != "" {
			row.Version = m.version
		}
		if row.Capabilities == nil {
			row.Capabilities = []string{}
		}
		switch {
		case !m.rec.TokenRequired:
			row.ProxyError = "runs with server.allow_unauthenticated"
		case m.routes == nil:
			row.ProxyError = m.routesErr
		}
		rows = append(rows, row)
	}
	return rows
}

func (h *Hub) handleProjects(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, ProjectsResponse{
		Hub: HubInfo{
			Version: h.cfg.Version, PID: h.self.PID, URL: h.self.URL, StartedAt: h.self.StartedAt,
			HeadSeq: h.feed.headSeq(),
		},
		Projects: h.projectRows(),
	})
}

func (h *Hub) handleWhoami(w http.ResponseWriter, r *http.Request) {
	p, _ := principalFrom(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"principal": p, "hub": true})
}

// handleFeed serves GET /hub/api/v1/feed?since=<hub seq>&limit=<n>.
func (h *Hub) handleFeed(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var since int64
	if v := q.Get("since"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "bad_request", "since must be a non-negative integer")
			return
		}
		since = n
	}
	limit := 0
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > feedMaxLimit {
			writeError(w, http.StatusBadRequest, "bad_request", "limit must be between 1 and 1000")
			return
		}
		limit = n
	}
	writeJSON(w, http.StatusOK, h.feed.page(since, limit))
}

// sseKeepAlive is the comment interval that keeps idle streams open
// through proxies.
const sseKeepAlive = 25 * time.Second

// handleEvents serves GET /hub/api/v1/events: an SSE stream that announces
// the merged feed's head (`event: feed`, data {"headSeq":N}) whenever it
// moves and the member count (`event: projects`, data {"count":N}) whenever
// the member set changes, each sent once on connect. A client reads the
// feed itself with GET /hub/api/v1/feed?since=.
func (h *Hub) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "no_streaming", "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	send := func(event string, data any) bool {
		raw, _ := json.Marshal(data)
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, raw); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	ping := time.NewTicker(sseKeepAlive)
	defer ping.Stop()
	_, _, feedWait := h.feed.after(h.feed.headSeq())
	membersWait := h.members.changes()
	if !send("projects", map[string]int{"count": len(h.members.list())}) || !send("feed", map[string]int64{"headSeq": h.feed.headSeq()}) {
		return
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case <-feedWait:
			_, _, feedWait = h.feed.after(h.feed.headSeq())
			if !send("feed", map[string]int64{"headSeq": h.feed.headSeq()}) {
				return
			}
		case <-membersWait:
			membersWait = h.members.changes()
			if !send("projects", map[string]int{"count": len(h.members.list())}) {
				return
			}
		case <-ping.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
