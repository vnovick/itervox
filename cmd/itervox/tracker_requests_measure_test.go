//go:build measure

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Tracker requests: v0.2.2 polling versus the tracker store with
// incremental pulls (#114). Not part of the normal test run:
//
//	go test -tags measure -run '^TestMeasureTrackerRequests$' -v ./cmd/itervox/
//
// The real daemon runs against a fake GitHub API that counts every request,
// once with the store off (the v0.2.2 polling path) and once with it on, for
// the same simulated hour, with and without an open dashboard (which reads
// /api/v1/issues every 5 simulated seconds). Every interval is the default
// divided by timeScale, and the full-sync interval is set just under the
// hour so the store's hourly full pull is counted. A person edits one issue
// and closes another during the run; with the store on, both must reach the
// board.

const timeScale = 30

var simulatedHour = time.Hour / timeScale

type fakeIssue struct {
	number  int
	title   string
	closed  bool
	labels  []string
	updated time.Time
}

type countingGitHub struct {
	mu     sync.Mutex
	issues map[int]*fakeIssue
	counts map[string]int
	total  int
}

var measureIssuePath = regexp.MustCompile(`^/repos/owner/repo/issues/(\d+)(/comments)?$`)

func newFakeGitHubProject() *countingGitHub {
	f := &countingGitHub{issues: map[int]*fakeIssue{}, counts: map[string]int{}}
	base := time.Now().Add(-48 * time.Hour)
	label := func(n int) []string {
		switch {
		case n <= 15:
			return []string{"backlog"}
		case n <= 20:
			return []string{"in-review"}
		default:
			return []string{"done"}
		}
	}
	for n := 1; n <= 40; n++ { // an idle pilot project: no issue waiting for an agent
		f.issues[n] = &fakeIssue{number: n, title: fmt.Sprintf("Issue %d", n), closed: n > 20,
			labels: label(n), updated: base.Add(time.Duration(n) * time.Minute)}
	}
	return f
}

func (f *countingGitHub) edit(n int, change func(*fakeIssue)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	change(f.issues[n])
	f.issues[n].updated = time.Now()
}

func (f *countingGitHub) json(i *fakeIssue) map[string]any {
	labels := make([]any, len(i.labels))
	for k, l := range i.labels {
		labels[k] = map[string]any{"name": l}
	}
	state := "open"
	if i.closed {
		state = "closed"
	}
	return map[string]any{
		"number": i.number, "title": i.title, "state": state, "labels": labels, "body": "",
		"html_url":   fmt.Sprintf("https://github.com/owner/repo/issues/%d", i.number),
		"created_at": "2026-01-01T00:00:00Z", "updated_at": i.updated.UTC().Format(time.RFC3339),
	}
}

func (f *countingGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	q := r.URL.Query()
	kind := r.Method + " " + r.URL.Path
	w.Header().Set("Content-Type", "application/json")
	write := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	switch {
	case r.URL.Path == "/repos/owner/repo/issues":
		kind = "GET issues?state=" + q.Get("state")
		if q.Get("since") != "" {
			kind += "&since"
		}
		var since time.Time
		if s := q.Get("since"); s != "" {
			since, _ = time.Parse(time.RFC3339, s)
		}
		var list []*fakeIssue
		for _, i := range f.issues {
			switch q.Get("state") {
			case "open":
				if i.closed || (q.Get("labels") != "" && !hasLabel(i, q.Get("labels"))) {
					continue
				}
			case "closed":
				if !i.closed {
					continue
				}
			}
			if !since.IsZero() && i.updated.Truncate(time.Second).Before(since) {
				continue
			}
			list = append(list, i)
		}
		sort.Slice(list, func(a, b int) bool { return list[a].updated.Before(list[b].updated) })
		out := make([]any, 0, len(list))
		for _, i := range list {
			out = append(out, f.json(i))
		}
		write(out)
	case measureIssuePath.MatchString(r.URL.Path):
		m := measureIssuePath.FindStringSubmatch(r.URL.Path)
		n, _ := strconv.Atoi(m[1])
		i, ok := f.issues[n]
		switch {
		case !ok:
			kind = "GET issues/{n}"
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
		case m[2] != "":
			kind = "GET issues/{n}/comments"
			write([]any{})
		default:
			kind = "GET issues/{n}"
			write(f.json(i))
		}
	case r.URL.Path == "/repos/owner/repo":
		write(map[string]any{"full_name": "owner/repo"})
	default:
		write([]any{})
	}
	f.counts[kind]++
	f.total++
}

func hasLabel(i *fakeIssue, label string) bool {
	for _, l := range i.labels {
		if strings.EqualFold(l, label) {
			return true
		}
	}
	return false
}

func (f *countingGitHub) snapshot() (int, map[string]int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]int{}
	for k, v := range f.counts {
		out[k] = v
	}
	return f.total, out
}

type measureResult struct {
	mode   string
	total  int
	counts map[string]int
}

// measureMode runs one scenario. syncEvery is tracker.store.sync_interval_ms
// in real time (zero: the store is off).
func measureMode(t *testing.T, mode string, syncEvery time.Duration, dashboard bool) measureResult {
	t.Helper()
	gh := newFakeGitHubProject()
	srv := httptest.NewServer(gh)
	defer srv.Close()
	p := newDaemonProject(t)
	ms := func(d time.Duration) int { return int(d / timeScale / time.Millisecond) }
	store := syncEvery > 0
	storeBlock := ""
	if store {
		storeBlock = fmt.Sprintf("  store:\n    enabled: true\n    sync_interval_ms: %d\n    full_sync_interval_ms: %d\n",
			ms(syncEvery), ms(55*time.Minute))
	}
	wf := fmt.Sprintf(`---
itervox_schema_version: 2
tracker:
  kind: github
  api_key: $GITHUB_TOKEN
  project_slug: owner/repo
  endpoint: %s
  active_states: ["todo", "in-progress"]
  terminal_states: ["done", "cancelled"]
  backlog_states: ["backlog"]
  completion_state: "in-review"
%spolling:
  interval_ms: %d
agent:
  command: %s
workspace:
  root: %s
server:
  host: 127.0.0.1
  port: 0
---
Work on {{ issue.identifier }}.
`, srv.URL, storeBlock, ms(30*time.Second), filepath.Join(p.Dir, "bin", "claude"), filepath.Join(p.Dir, "workspaces"))
	if err := os.WriteFile(p.Workflow, []byte(wf), 0o644); err != nil {
		t.Fatal(err)
	}
	p.env = append(p.env, "GITHUB_TOKEN=fixture")
	p.startHeadless(t, nil)
	tokenFile := filepath.Join(p.LogsDir, apiTokenFileName)
	waitFor(t, 60*time.Second, "the daemon serves", func() bool {
		_, err := os.Stat(tokenFile)
		return err == nil
	})
	token := readTrim(t, tokenFile)
	base := strings.TrimSuffix(readTrim(t, dashboardURLFilePath(p.Workflow)), "/")
	startTotal, startCounts := gh.snapshot() // startup reads are left out: the same once per start in both modes

	issues := func() map[string]string {
		req, _ := http.NewRequest(http.MethodGet, base+"/api/v1/issues", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var rows []struct {
			Identifier, Title, State string
		}
		_ = json.NewDecoder(resp.Body).Decode(&rows)
		out := map[string]string{}
		for _, r := range rows {
			out[r.Identifier] = r.Title + "|" + r.State
		}
		return out
	}
	// An open dashboard: /api/v1/issues every 5 simulated seconds.
	stop := make(chan struct{})
	var dash sync.WaitGroup
	dash.Go(func() {
		if !dashboard {
			return
		}
		tick := time.NewTicker(5 * time.Second / timeScale)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				req, _ := http.NewRequest(http.MethodGet, base+"/api/v1/issues", nil)
				req.Header.Set("Authorization", "Bearer "+token)
				if resp, err := http.DefaultClient.Do(req); err == nil {
					_ = resp.Body.Close()
				}
			}
		}
	})
	time.Sleep(simulatedHour / 3)
	gh.edit(5, func(i *fakeIssue) { i.title = "Edited by a person" })
	time.Sleep(simulatedHour / 3)
	gh.edit(16, func(i *fakeIssue) { i.closed, i.labels = true, []string{"done"} })
	time.Sleep(simulatedHour / 3)
	close(stop)
	dash.Wait()
	total, counts := gh.snapshot()
	if store {
		board := issues()
		if board["#5"] != "Edited by a person|backlog" {
			t.Errorf("%s: the person's edit is not on the board: %q", mode, board["#5"])
		}
		if board["#16"] != "Issue 16|done" {
			t.Errorf("%s: the closed issue is not done on the board: %q", mode, board["#16"])
		}
	}
	for k, v := range startCounts {
		if counts[k] -= v; counts[k] == 0 {
			delete(counts, k)
		}
	}
	p.stopDaemonByPIDFile(t)
	return measureResult{mode: mode, total: total - startTotal, counts: counts}
}

func TestMeasureTrackerRequests(t *testing.T) {
	// Pairs: v0.2.2 polling, then the store, in the same scenario. The store
	// runs at the default sync interval (1 min), at 2 min, and at 10 min
	// (the webhook safety-net interval, #131).
	results := []measureResult{
		measureMode(t, "daemon, v0.2.2 polling", 0, false),
		measureMode(t, "daemon, store, sync 1m", time.Minute, false),
		measureMode(t, "daemon, v0.2.2 polling", 0, false),
		measureMode(t, "daemon, store, sync 2m", 2*time.Minute, false),
		measureMode(t, "daemon, v0.2.2 polling", 0, false),
		measureMode(t, "daemon, store, sync 10m", 10*time.Minute, false),
		measureMode(t, "+dashboard, v0.2.2 polling", 0, true),
		measureMode(t, "+dashboard, store, sync 1m", time.Minute, true),
	}
	for _, r := range results {
		t.Logf("%-28s %5d tracker requests per simulated hour", r.mode, r.total)
		keys := make([]string, 0, len(r.counts))
		for k := range r.counts {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			t.Logf("    %-34s %5d", k, r.counts[k])
		}
	}
	// A measurement, not a gate: the drop depends on the scenario and the
	// intervals, and is reported for each.
	for i := 0; i < len(results); i += 2 {
		drop := 100 * (1 - float64(results[i+1].total)/float64(results[i].total))
		t.Logf("%-28s → %-28s %5d → %4d requests/hour, drop %.1f%%",
			results[i].mode, results[i+1].mode, results[i].total, results[i+1].total, drop)
	}
}
