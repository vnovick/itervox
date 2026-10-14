package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/atomicfs"
)

// Host daemon registry (#119).
//
// Every daemon writes one JSON record to ~/.itervox/daemons/<pid>.json once
// its HTTP socket is bound, rewrites it on every config-reload generation (a
// reload can move the port), and removes it on shutdown, together with
// daemon.pid / dashboard_url / HEARTBEAT.md. `itervox status --json` reads
// the records, so a supervisor (the macOS menu-bar app) can discover every
// daemon on the host without scanning processes. The directory is 0700 and
// each record 0600. A record never holds a token, only whether one is
// required and where it comes from.

// daemonRecordSchemaVersion versions the record AND the `status --json`
// output. Fields may be added within a version; a rename, removal or change
// of meaning bumps it.
const daemonRecordSchemaVersion = 1

// processStartedAt is when this process started (package init), reported as
// started_at.
var processStartedAt = time.Now().UTC()

// Token sources reported in daemonRecord.TokenSource.
const (
	tokenSourceSupervisor = "supervisor" // ITERVOX_SUPERVISOR_TOKEN
	tokenSourceEnv        = "env"        // ITERVOX_API_TOKEN (environment or .itervox/.env)
	tokenSourceGenerated  = "generated"  // auto-generated; see token_file
	tokenSourceNone       = "none"       // server.allow_unauthenticated: true
	tokenSourceUnknown    = "unknown"    // process_scan record: not reported by that daemon
)

// daemonRecord is one registry file and one element of `status --json`.
// The documented contract lives in docs/api.md ("Supervisor contract").
type daemonRecord struct {
	SchemaVersion int    `json:"schema_version"`
	PID           int    `json:"pid"`
	Workflow      string `json:"workflow"`
	ProjectDir    string `json:"project_dir"`
	URL           string `json:"url"`
	Host          string `json:"host"`
	Port          int    `json:"port"`
	Version       string `json:"version"`
	Commit        string `json:"commit"`
	StartedAt     string `json:"started_at"`
	TokenRequired bool   `json:"token_required"`
	TokenSource   string `json:"token_source"`
	TokenFile     string `json:"token_file"`
	LogsDir       string `json:"logs_dir"`
	Heartbeat     string `json:"heartbeat"`
	Executable    string `json:"executable"`
	PathSource    string `json:"path_source"`
	// Source is how `status` found the daemon: "registry", or "process_scan"
	// for a daemon started by a binary older than the registry. Not stored.
	Source string `json:"source"`
}

// daemonRegistryDir is ~/.itervox/daemons.
func daemonRegistryDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("daemon registry: %w", err)
	}
	return filepath.Join(home, ".itervox", "daemons"), nil
}

// daemonRegistryEntryPath is this process's record.
func daemonRegistryEntryPath() (string, error) {
	dir, err := daemonRegistryDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, strconv.Itoa(os.Getpid())+".json"), nil
}

// tokenSourceFor classifies the active API token.
func tokenSourceFor(token string, generated, supervised bool) string {
	switch {
	case token == "":
		return tokenSourceNone
	case supervised:
		return tokenSourceSupervisor
	case generated:
		return tokenSourceGenerated
	default:
		return tokenSourceEnv
	}
}

// newDaemonRecord builds this process's record for a bound address.
func newDaemonRecord(workflowPath, addr, logsDir, token string, generated, supervised bool) daemonRecord {
	wf, err := filepath.Abs(workflowPath)
	if err != nil {
		wf = workflowPath
	}
	host, portStr, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(portStr)
	exe, _ := os.Executable()
	rec := daemonRecord{
		SchemaVersion: daemonRecordSchemaVersion,
		PID:           os.Getpid(),
		Workflow:      wf,
		ProjectDir:    filepath.Dir(wf),
		URL:           "http://" + addr + "/",
		Host:          host,
		Port:          port,
		Version:       version,
		Commit:        commit,
		StartedAt:     processStartedAt.Format(time.RFC3339),
		TokenRequired: token != "",
		TokenSource:   tokenSourceFor(token, generated, supervised),
		LogsDir:       logsDir,
		Heartbeat:     heartbeatPath(wf),
		Executable:    exe,
	}
	if rec.TokenSource == tokenSourceGenerated {
		rec.TokenFile = filepath.Join(logsDir, apiTokenFileName)
	}
	if r := currentPATHResolution(); r != nil {
		rec.PathSource = r.Source
	}
	return rec
}

// writeDaemonRegistryEntry records this daemon in the host registry. A
// failure is logged, never fatal: the registry is for discovery only.
// `itervox demo` is not recorded: it promises to write nothing outside its
// scratch directory, and the registry lives under the home directory.
func writeDaemonRegistryEntry(rec daemonRecord) {
	if demoSession != nil {
		return
	}
	path, err := daemonRegistryEntryPath()
	if err == nil {
		err = ensurePrivateDir(filepath.Dir(path))
	}
	if err == nil {
		var data []byte
		if data, err = json.MarshalIndent(rec, "", "  "); err == nil {
			err = atomicfs.WriteFile(path, append(data, '\n'), 0o600)
		}
	}
	if err != nil {
		slog.Warn("itervox: could not record the daemon in the host registry — `itervox status --json` will not list it", "path", path, "error", err)
	}
}

// removeDaemonRegistryEntry is the shutdown counterpart; idempotent.
func removeDaemonRegistryEntry() {
	path, err := daemonRegistryEntryPath()
	if err != nil {
		return
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		slog.Warn("itervox: failed to remove the daemon registry entry", "path", path, "error", err)
	}
}

// registryRecordLive reports whether rec still describes a running daemon:
// the pid is alive and the workflow's daemon.pid still names it. The second
// check rejects a record left by a killed daemon whose pid was recycled.
func registryRecordLive(rec daemonRecord) bool {
	if rec.PID <= 0 || rec.Workflow == "" || !processAlive(rec.PID) {
		return false
	}
	pid, _, _, err := readPIDFile(rec.Workflow)
	return err == nil && pid == rec.PID
}

// readDaemonRegistry returns the live records in dir and removes the stale
// ones (a daemon that was SIGKILLed cannot remove its own).
func readDaemonRegistry(dir string) []daemonRecord {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []daemonRecord
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var rec daemonRecord
		if json.Unmarshal(data, &rec) != nil || !registryRecordLive(rec) {
			_ = os.Remove(path)
			continue
		}
		rec.Source = "registry"
		out = append(out, rec)
	}
	return out
}

// scannedDaemonRecord describes a daemon found by process scan that has no
// registry record (started by an older binary). Only a process whose working
// directory holds a .itervox/daemon.pid naming it counts, so other processes
// whose command line merely mentions "itervox" are ignored. Version, start
// time and token source are unknown; token_required is reported true, the
// default since v0.2.1.
func scannedDaemonRecord(pid int, cwd func(int) string) (daemonRecord, bool) {
	dir := cwd(pid)
	if dir == "" {
		return daemonRecord{}, false
	}
	recPID, recorded, _, err := readPIDFile(filepath.Join(dir, "WORKFLOW.md"))
	if err != nil || recPID != pid {
		return daemonRecord{}, false
	}
	wf := recorded
	if wf == "" {
		wf = filepath.Join(dir, "WORKFLOW.md")
	}
	rec := daemonRecord{
		SchemaVersion: daemonRecordSchemaVersion,
		PID:           pid,
		Workflow:      wf,
		ProjectDir:    filepath.Dir(wf),
		TokenRequired: true,
		TokenSource:   tokenSourceUnknown,
		Heartbeat:     heartbeatPath(wf),
		Source:        "process_scan",
	}
	if raw, err := os.ReadFile(dashboardURLFilePath(wf)); err == nil {
		rec.URL = strings.TrimSpace(string(raw))
		if host, port, ok := hostPortFromURL(rec.URL); ok {
			rec.Host, rec.Port = host, port
		}
	}
	return rec, true
}

// hostPortFromURL splits "http://host:port/" into its host and port.
func hostPortFromURL(u string) (string, int, bool) {
	rest := strings.TrimPrefix(strings.TrimPrefix(u, "http://"), "https://")
	rest, _, _ = strings.Cut(rest, "/")
	host, portStr, err := net.SplitHostPort(rest)
	if err != nil {
		return "", 0, false
	}
	port, err := strconv.Atoi(portStr)
	return host, port, err == nil
}

// collectDaemons lists every running daemon on the host: the registry first,
// then process-scan finds the registry does not cover. Sorted by workflow
// path, then pid, so the output is stable.
func collectDaemons(registryDir string, scan func() []int, cwd func(int) string) []daemonRecord {
	out := readDaemonRegistry(registryDir)
	for _, pid := range scan() {
		if slices.ContainsFunc(out, func(r daemonRecord) bool { return r.PID == pid }) {
			continue
		}
		if rec, ok := scannedDaemonRecord(pid, cwd); ok {
			out = append(out, rec)
		}
	}
	slices.SortFunc(out, func(a, b daemonRecord) int {
		if c := strings.Compare(a.Workflow, b.Workflow); c != 0 {
			return c
		}
		return a.PID - b.PID
	})
	if out == nil {
		out = []daemonRecord{} // `[]`, never `null`
	}
	return out
}
