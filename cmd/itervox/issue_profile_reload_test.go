//go:build unix

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestProfileOverrideSurvivesConfigReload: an operator's per-issue profile
// override (POST /api/v1/issues/{id}/profile) must survive a WORKFLOW.md
// reload. cmd/itervox builds a new Orchestrator per generation, and the
// override lived only in the old one's memory, so after the reload
// GET /api/v1/issues/{id} showed agentProfile: null and the next run used
// the default profile. Real re-exec'd daemon (dry run, memory tracker), the
// real profile route, then a real reload by editing WORKFLOW.md.
func TestProfileOverrideSurvivesConfigReload(t *testing.T) {
	p := newDaemonProject(t)
	fake := filepath.Join(p.Dir, "bin", "claude")
	writeAckReloadWorkflow(t, p, fake, 1)
	const tok = "profile-reload-token-0123456789"
	p.startHeadless(t, nil, "ITERVOX_DRY_RUN=1", "ITERVOX_API_TOKEN="+tok)
	defer p.stopDaemonByPIDFile(t)
	var base string
	waitFor(t, 30*time.Second, "dashboard url", func() bool {
		b, err := os.ReadFile(dashboardURLFilePath(p.Workflow))
		base = strings.TrimSuffix(strings.TrimSpace(string(b)), "/")
		return err == nil && base != ""
	})
	do := func(method, path string, body []byte) (int, []byte) {
		req, _ := http.NewRequest(method, base+path, bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return 0, nil
		}
		defer func() { _ = resp.Body.Close() }()
		rb, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, rb
	}
	const id = "DEMO-1"
	// agentProfile returns the issue's agentProfile and whether it was read.
	agentProfile := func() (any, bool) {
		code, rb := do("GET", "/api/v1/issues/"+id, nil)
		if code != http.StatusOK {
			return nil, false
		}
		var issue map[string]any
		if json.Unmarshal(rb, &issue) != nil {
			return nil, false
		}
		return issue["agentProfile"], true
	}
	maxAgents := func() int {
		_, rb := do("GET", "/api/v1/state", nil)
		var s ackReloadSnap
		_ = json.Unmarshal(rb, &s)
		return s.MaxConcurrentAgents
	}
	waitFor(t, 30*time.Second, "issue "+id+" readable", func() bool { _, ok := agentProfile(); return ok })

	code, rb := do("POST", "/api/v1/issues/"+id+"/profile", []byte(`{"profile":"researcher"}`))
	if code != http.StatusOK {
		t.Fatalf("profile POST -> %d %s", code, rb)
	}
	waitFor(t, 10*time.Second, "agentProfile=researcher before reload", func() bool {
		v, _ := agentProfile()
		return v == "researcher"
	})
	t.Logf("before reload: %s agentProfile=researcher", id)

	writeAckReloadWorkflow(t, p, fake, 2) // reload: max_concurrent_agents 1 -> 2
	waitFor(t, 60*time.Second, "reload (maxConcurrentAgents=2)", func() bool { return maxAgents() == 2 })
	time.Sleep(time.Second)
	v, _ := agentProfile()
	if v != "researcher" {
		t.Fatalf("the per-issue profile override was lost across the WORKFLOW.md reload: agentProfile=%v, want researcher", v)
	}
	t.Logf("after reload: %s agentProfile=%v", id, v)
}
