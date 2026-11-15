//go:build unix

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// #132 — the tracker store is on by default.

// staleListingGitHub is a GitHub API whose issue listings lag behind the
// tracker: they still show #1 in "todo", while a person has since closed it
// as done (what a by-ID read returns). #2 is in "todo" everywhere.
func staleListingGitHub(t *testing.T) *httptest.Server {
	t.Helper()
	issue := func(n int, state string, labels ...string) map[string]any {
		ls := make([]any, 0, len(labels))
		for _, l := range labels {
			ls = append(ls, map[string]any{"name": l})
		}
		return map[string]any{
			"number": n, "title": fmt.Sprintf("Issue %d", n), "state": state, "labels": ls, "body": "",
			"html_url":   fmt.Sprintf("https://github.com/owner/repo/issues/%d", n),
			"created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-10-01T09:00:00Z",
		}
	}
	listed := []any{issue(1, "open", "todo"), issue(2, "open", "todo")}
	byID := map[string]any{"1": issue(1, "closed", "done"), "2": issue(2, "open", "todo")}
	one := regexp.MustCompile(`^/repos/owner/repo/issues/(\d+)(/comments)?$`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		write := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		switch m := one.FindStringSubmatch(r.URL.Path); {
		case r.URL.Path == "/repos/owner/repo/issues":
			if r.URL.Query().Get("state") == "closed" {
				write([]any{})
				return
			}
			if l := r.URL.Query().Get("labels"); l != "" && l != "todo" {
				write([]any{})
				return
			}
			write(listed)
		case m != nil && m[2] != "":
			write([]any{})
		case m != nil:
			write(byID[m[1]])
		default:
			write([]any{})
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestDefaultStoreDoesNotDispatchAnIssueMovedOutBetweenSyncs: with no
// tracker.store block, the daemon serves its list reads from the tracker
// store; an issue a person moved out of the active states since the
// store's last pull is still listed as active, and the claim check's read
// by ID keeps it from being dispatched, while an unchanged issue is.
func TestDefaultStoreDoesNotDispatchAnIssueMovedOutBetweenSyncs(t *testing.T) {
	gh := staleListingGitHub(t)
	p := newDaemonProject(t)
	wf := fmt.Sprintf(`---
itervox_schema_version: 2
tracker:
  kind: github
  api_key: $GITHUB_TOKEN
  project_slug: owner/repo
  endpoint: %s
  active_states: ["todo", "in-progress"]
  terminal_states: ["done", "cancelled"]
polling:
  interval_ms: 200
agent:
  command: %s
workspace:
  root: %s
server:
  host: 127.0.0.1
  port: 0
---
Work on {{ issue.identifier }}.
`, gh.URL, filepath.Join(p.Dir, "bin", "claude"), filepath.Join(p.Dir, "workspaces"))
	if err := os.WriteFile(p.Workflow, []byte(wf), 0o644); err != nil {
		t.Fatal(err)
	}
	p.env = append(p.env, "GITHUB_TOKEN=fixture")
	_, stderr := p.startHeadless(t, nil)
	t.Cleanup(func() { p.stopDaemonByPIDFile(t) })

	waitFor(t, 30*time.Second, "#2 dispatched", func() bool {
		raw, _ := os.ReadFile(stderr)
		return strings.Contains(string(raw), "would dispatch agent identifier=#2")
	})
	time.Sleep(time.Second) // several more ticks
	raw, _ := os.ReadFile(stderr)
	got := string(raw)
	assert.Contains(t, got, "tracker store: enabled", "the store is on with no tracker.store block")
	assert.NotContains(t, got, "would dispatch agent identifier=#1", "the issue the person closed is not dispatched")
}
