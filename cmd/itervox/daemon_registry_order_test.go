//go:build unix

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// A daemon that a process scan finds with a port (its dashboard_url) but no
// registry record is reported as a pre-registry daemon: token_source
// "unknown", no token_file. While a current daemon starts or stops that is
// wrong, and the desktop's "Add remote" asked for a token that was about to
// be readable (remote_e2e.rs, "the token file should have been read: None").
// So dashboard_url exists only while the registry record and the token file
// do.

// TestDashboardURLOnlyWhileRegistryRecordExists watches a real daemon start
// and stop, checking at every observation that dashboard_url never exists
// without the registry record (and, at startup, the token file).
func TestDashboardURLOnlyWhileRegistryRecordExists(t *testing.T) {
	p := newDaemonProject(t)
	home := filepath.Join(p.Dir, "home")
	urlPath := dashboardURLFilePath(p.Workflow)
	tokenPath := filepath.Join(p.LogsDir, apiTokenFileName)
	exists := func(path string) bool {
		_, err := os.Stat(path)
		return err == nil
	}
	cmd, _ := p.startHeadless(t, nil)
	pid := cmd.Process.Pid
	reg := registryFile(home, pid)

	deadline := time.Now().Add(30 * time.Second)
	for !exists(urlPath) {
		if time.Now().After(deadline) {
			t.Fatal("the daemon never wrote dashboard_url")
		}
		// No sleep: the window this test guards was a few milliseconds.
	}
	if !exists(reg) {
		t.Fatal("dashboard_url was written before the registry record")
	}
	if !exists(tokenPath) {
		t.Fatal("dashboard_url was written before the token file")
	}
	var rec daemonRecord
	raw, err := os.ReadFile(reg)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatal(err)
	}
	if rec.TokenFile != tokenPath || rec.Port == 0 {
		t.Fatalf("registry record token_file/port = %q/%d, want %q and a port", rec.TokenFile, rec.Port, tokenPath)
	}

	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	pidPath, err := pidFilePath(p.Workflow)
	if err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(15 * time.Second)
	for exists(pidPath) {
		// Registry first, then dashboard_url: a stat order that can only
		// see "url without record" if the daemon removes them that way.
		hasRecord := exists(reg)
		if exists(urlPath) && !hasRecord {
			t.Fatal("on shutdown the registry record was removed before dashboard_url")
		}
		if time.Now().After(deadline) {
			t.Fatal("the daemon did not remove its pid file on shutdown")
		}
	}
}

// TestCollectDaemonsPrefersARecordWrittenDuringTheScan: a daemon that
// writes its registry record after the registry was read, but before the
// scan reached its pid, is reported from that record, not as a
// pre-registry daemon with an unknown token.
func TestCollectDaemonsPrefersARecordWrittenDuringTheScan(t *testing.T) {
	pid := os.Getpid()
	regDir := filepath.Join(t.TempDir(), "daemons")
	if err := os.MkdirAll(regDir, 0o700); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	wf := filepath.Join(dir, "WORKFLOW.md")
	// An unmarked (pre-lock) daemon.pid naming this process: live by the
	// pid probe, for both the scan and the record.
	writeRegistryFixture(t, filepath.Join(t.TempDir(), "elsewhere"), wf, pid, false)
	if err := writeDashboardURLFile(wf, "http://127.0.0.1:41234/"); err != nil {
		t.Fatal(err)
	}
	rec := daemonRecord{
		SchemaVersion: daemonRecordSchemaVersion, PID: pid, Workflow: wf, ProjectDir: dir,
		Host: "127.0.0.1", Port: 41234, TokenRequired: true,
		TokenSource: tokenSourceGenerated, TokenFile: filepath.Join(dir, "logs", apiTokenFileName),
	}
	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	scan := func() []int {
		// The registry has been read (empty); the daemon records itself now.
		if err := os.WriteFile(filepath.Join(regDir, strconv.Itoa(pid)+".json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
		return []int{pid}
	}
	got := collectDaemons(regDir, scan, func(int) string { return dir })
	if len(got) != 1 {
		t.Fatalf("want one daemon, got %+v", got)
	}
	if got[0].Source != "registry" || got[0].TokenFile != rec.TokenFile || got[0].TokenSource != tokenSourceGenerated {
		t.Fatalf("want the registry record, got %+v", got[0])
	}
}
