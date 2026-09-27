package main

import (
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestDaemonHonoursServerPortEnv is the CORE-058 runtime check: a real daemon
// whose WORKFLOW.md says `port: 0` binds the port ITERVOX_SERVER_PORT names,
// logs that the env var chose it, and answers /api/v1/health there.
func TestDaemonHonoursServerPortEnv(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()

	p := newDaemonProject(t)
	_, stderrPath := p.startHeadless(t, nil, "ITERVOX_SERVER_PORT="+strconv.Itoa(port), "PORT=1")
	waitFor(t, 20*time.Second, "dashboard URL written", func() bool {
		return countFileOccurrences(dashboardURLFilePath(p.Workflow), "http://") > 0
	})
	if got := readTrim(t, dashboardURLFilePath(p.Workflow)); got != "http://127.0.0.1:"+strconv.Itoa(port)+"/" {
		t.Fatalf("dashboard URL = %q, want port %d from ITERVOX_SERVER_PORT", got, port)
	}
	resp, err := http.Get("http://127.0.0.1:" + strconv.Itoa(port) + "/api/v1/health")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if resp.StatusCode != http.StatusOK || body["status"] != "ok" {
		t.Fatalf("health = %d %v, want 200 {status: ok}", resp.StatusCode, body)
	}
	if !strings.Contains(readTrim(t, stderrPath), "port_source=ITERVOX_SERVER_PORT") {
		t.Errorf("no port_source=ITERVOX_SERVER_PORT on the listening line:\n%s", readTrim(t, stderrPath))
	}
	t.Logf("GET /api/v1/health on the env-selected port %d -> %d %v", port, resp.StatusCode, body)
}
