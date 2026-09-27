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

type ackReloadSnap struct {
	MaxConcurrentAgents int `json:"maxConcurrentAgents"`
	RecentFailures      []struct {
		Kind       string `json:"kind"`
		Identifier string `json:"identifier"`
		OccurredAt string `json:"occurredAt"`
	} `json:"recentFailures"`
	FailureAcks []struct {
		Identifier string `json:"identifier"`
		UpTo       string `json:"upTo"`
	} `json:"failureAcks"`
	Capabilities []string `json:"capabilities"`
}

func writeAckReloadWorkflow(t *testing.T, p *daemonProject, fake string, maxAgents int) {
	content := "---\nitervox_schema_version: 2\ntracker:\n  kind: memory\n  active_states: [\"Todo\", \"In Progress\"]\n  terminal_states: [\"Done\"]\nagent:\n  command: " + fake +
		"\n  max_concurrent_agents: " + string(rune('0'+maxAgents)) + "\n  max_retries: 0\nworkspace:\n  root: " + filepath.Join(p.Dir, "workspaces") +
		"\nserver:\n  host: 127.0.0.1\n  port: 0\n---\n\nYou are working on {{ issue.identifier }}.\n"
	if err := os.WriteFile(p.Workflow, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestFailureAckSurvivesWorkflowReload (M6-close V3, CORE-175): an operator
// ack must survive a WORKFLOW.md reload exactly like the RecentFailures ring
// it refers to (SeedRecentFailures). Real re-exec'd daemon, a failing fake
// agent, the real ack route, then a real reload by editing WORKFLOW.md.
func TestFailureAckSurvivesWorkflowReload(t *testing.T) {
	p := newDaemonProject(t)
	fake := filepath.Join(p.Dir, "bin", "claude")
	script := "#!/bin/bash\ncase \"$1\" in --version|-v) echo fake 0.0.0; exit 0;; esac\necho '{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"zz\"}'\nexit 1\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	writeAckReloadWorkflow(t, p, fake, 1)
	const tok = "ack-reload-token-0123456789"
	p.startHeadless(t, nil, "ITERVOX_DRY_RUN=0", "ITERVOX_API_TOKEN="+tok)
	defer p.stopDaemonByPIDFile(t)
	var base string
	waitFor(t, 30*time.Second, "dashboard url", func() bool {
		b, err := os.ReadFile(dashboardURLFilePath(p.Workflow))
		base = strings.TrimSuffix(strings.TrimSpace(string(b)), "/")
		return err == nil && base != ""
	})
	get := func() ackReloadSnap {
		req, _ := http.NewRequest("GET", base+"/api/v1/state", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		var s ackReloadSnap
		if err != nil {
			return s
		}
		defer func() { _ = resp.Body.Close() }()
		_ = json.NewDecoder(resp.Body).Decode(&s)
		return s
	}
	var id, at string
	waitFor(t, 60*time.Second, "a worker_failed row", func() bool {
		for _, f := range get().RecentFailures {
			if f.Kind == "worker_failed" || f.Kind == "worker_stalled" {
				id, at = f.Identifier, f.OccurredAt
				return true
			}
		}
		return false
	})
	body, _ := json.Marshal(map[string]string{"upTo": time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)})
	req, _ := http.NewRequest("POST", base+"/api/v1/issues/"+id+"/failures/ack", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	rb, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("ack POST %s -> %d %s", id, resp.StatusCode, rb)
	}
	t.Logf("ack POST %s (occurredAt %s) -> %d %s", id, at, resp.StatusCode, strings.TrimSpace(string(rb)))
	waitFor(t, 10*time.Second, "ack in snapshot", func() bool {
		for _, a := range get().FailureAcks {
			if a.Identifier == id {
				return true
			}
		}
		return false
	})
	t.Logf("before reload: failureAcks has %s", id)
	// Reload: change max_concurrent_agents 1 -> 2 in WORKFLOW.md.
	writeAckReloadWorkflow(t, p, fake, 2)
	waitFor(t, 60*time.Second, "reload (maxConcurrentAgents=2)", func() bool { return get().MaxConcurrentAgents == 2 })
	time.Sleep(2 * time.Second)
	s := get()
	hasRow, hasAck := false, false
	for _, f := range s.RecentFailures {
		if f.Identifier == id && (f.Kind == "worker_failed" || f.Kind == "worker_stalled") {
			hasRow = true
		}
	}
	for _, a := range s.FailureAcks {
		if a.Identifier == id {
			hasAck = true
		}
	}
	if !hasRow {
		t.Fatalf("precondition: the %s failure row must survive the reload (SeedRecentFailures)", id)
	}
	if !hasAck {
		t.Fatalf("the ack for %s was lost across the WORKFLOW.md reload (failureAcks=%v)", id, s.FailureAcks)
	}
}
