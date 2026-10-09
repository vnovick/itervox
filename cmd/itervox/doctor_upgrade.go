package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/config"
)

// `itervox doctor --upgrade` (#82): the v0.2.1 CHANGELOG has 30 numbered
// upgrade notes plus breaking changes, and most operators will not read them
// all. This reads the workflow, the environment and the machine, and prints
// only the notes that apply, each with the concrete fix.
//
// The front matter is read raw, not through config.Load: several of these
// changes make an old config fail to load, and that is exactly the config
// this command must still be able to explain.

// upgradeNotesURL is the v0.2.1 CHANGELOG section the notes live in.
const upgradeNotesURL = "https://github.com/vnovick/itervox/blob/main/CHANGELOG.md#021--2026-09-28"

// upgradeProbe is everything a rule may look at. Tests build it directly;
// newUpgradeProbe fills it from the real machine.
type upgradeProbe struct {
	WorkflowPath string
	// Front is the WORKFLOW.md front matter as plain YAML maps.
	Front map[string]any
	// Lookup reads the daemon's environment (with .itervox/.env loaded);
	// a variable that is set but empty is present.
	Lookup func(string) (string, bool)
	// Home is the user's home directory ("" when unknown).
	Home string
	// PortInUse reports whether another process holds 127.0.0.1:port.
	PortInUse func(port int) bool
	// SystemdUnits are the installed itervox.service paths to inspect.
	SystemdUnits []string
	// SecretFiles are the service environment files (EnvironmentFile=) a
	// unit may pin ITERVOX_API_TOKEN in.
	SecretFiles []string
}

func (p upgradeProbe) getenv(key string) string {
	if p.Lookup == nil {
		return ""
	}
	v, _ := p.Lookup(key)
	return v
}

func (p upgradeProbe) isSet(key string) bool {
	if p.Lookup == nil {
		return false
	}
	_, ok := p.Lookup(key)
	return ok
}

// upgradeFinding is one note that applies, with what to do about it.
type upgradeFinding struct {
	Note   string // "note 6", "breaking: …"
	Title  string
	Detail string // what in this setup makes it apply
	Fix    string
}

// upgradeRule checks one CHANGELOG note against a probe.
type upgradeRule struct {
	Note  string
	Title string
	Check func(p upgradeProbe) (detail, fix string, applies bool)
}

// upgradeRules are the v0.2.1 breaking and behaviour changes that depend on
// something this command can read: the workflow, the environment, the
// project's runtime files or the machine. Left out: notes that apply the
// same way to every install (1–4, 7, 8, 16, 18, 21, 25–27), deployment
// packaging (23, 24, 28–30), and keys that are new in v0.2.1, so no earlier
// config has them (backend_fallback, the allowed_hosts entry format). A
// malformed new key is reported by plain `itervox doctor` as a load failure.
var upgradeRules = []upgradeRule{
	{"note 5", "Default workspace and log paths are now per project", rulePerProjectPaths},
	{"note 6", "server.port now defaults to a fixed 8090", ruleFixedPort},
	{"note 9", "dependencies.auto_analyze is deprecated", ruleAutoAnalyze},
	{"note 10", "Headless daemons no longer print the tokenised dashboard URL", ruleHeadlessToken},
	{"note 11", "Invalid SSH host-key checking values now fail validation", ruleSSHStrictHost},
	{"note 12", "WORKFLOW.md edits create a .WORKFLOW.md.lock file", ruleWorkflowLockIgnored},
	{"notes 13 and 14", "Unauthenticated daemons refuse other origins and unlisted host names", ruleUnauthenticatedHosts},
	{"note 15", "itervox stop no longer signals a daemon the pid lock cannot vouch for", ruleLegacyPIDFile},
	{"notes 17 and 22", "SIGTERM now drains, and the systemd unit changed", ruleSystemdUnit},
	{"note 19", "A set PORT environment variable now moves the dashboard", rulePortEnv},
	{"note 20", "Headless --log-format json now also makes stderr JSON", ruleLogFormatJSON},
	{"breaking (#48)", "Bearer-token auth is now on by default on loopback too", ruleLoopbackAuth},
	{"breaking", "server.allow_unauthenticated_lan was renamed to server.allow_unauthenticated", ruleAllowUnauthenticatedRename},
	{"upgrade notes (CORE-115)", "A backend that contradicts the command is no longer applied", ruleBackendContradictsCommand},
}

// checkUpgradeNotes runs every rule.
func checkUpgradeNotes(p upgradeProbe) []upgradeFinding {
	var out []upgradeFinding
	for _, r := range upgradeRules {
		if detail, fix, ok := r.Check(p); ok {
			out = append(out, upgradeFinding{Note: r.Note, Title: r.Title, Detail: detail, Fix: fix})
		}
	}
	return out
}

// renderUpgradeReport prints the findings, or one all-clear line.
func renderUpgradeReport(workflowPath string, findings []upgradeFinding) string {
	var b strings.Builder
	if len(findings) == 0 {
		fmt.Fprintf(&b, "upgrade: all clear — none of the v0.2.1 upgrade notes need a change in %s or this environment.\n", workflowPath)
		return b.String()
	}
	fmt.Fprintf(&b, "upgrade: %d v0.2.1 upgrade note(s) apply to %s:\n", len(findings), workflowPath)
	for _, f := range findings {
		fmt.Fprintf(&b, "\n[%s] %s\n", f.Note, f.Title)
		fmt.Fprintf(&b, "  why:  %s\n", f.Detail)
		fmt.Fprintf(&b, "  fix:  %s\n", f.Fix)
		fmt.Fprintf(&b, "  see:  %s (%s)\n", upgradeNotesURL, f.Note)
	}
	return b.String()
}

// runUpgradeDoctor is `itervox doctor --upgrade`: exit 0 when nothing
// applies, 1 when any note does, 2 when the workflow cannot be read.
func runUpgradeDoctor(workflowPath string) (string, int) {
	p, err := newUpgradeProbe(workflowPath)
	if err != nil {
		return fmt.Sprintf("upgrade: %v\n", err), 2
	}
	findings := checkUpgradeNotes(p)
	code := 0
	if len(findings) > 0 {
		code = 1
	}
	return renderUpgradeReport(workflowPath, findings), code
}

func newUpgradeProbe(workflowPath string) (upgradeProbe, error) {
	front, err := readRawFrontMatter(workflowPath)
	if err != nil {
		return upgradeProbe{}, err
	}
	home, _ := os.UserHomeDir()
	return upgradeProbe{
		WorkflowPath: workflowPath,
		Front:        front,
		Lookup:       os.LookupEnv,
		Home:         home,
		PortInUse:    loopbackPortInUse,
		SystemdUnits: existingFiles("/etc/systemd/system/itervox.service", "/lib/systemd/system/itervox.service"),
		SecretFiles:  []string{"/etc/itervox/secrets.env", "/run/itervox/env"},
	}, nil
}

func readRawFrontMatter(workflowPath string) (map[string]any, error) {
	raw, err := os.ReadFile(workflowPath)
	if err != nil {
		return nil, err
	}
	front, _, ok := splitWorkflowFrontMatter(string(raw))
	if !ok {
		return nil, fmt.Errorf("%s has no YAML front matter", workflowPath)
	}
	var m map[string]any
	if err := yaml.Unmarshal([]byte(front), &m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", workflowPath, err)
	}
	if m == nil {
		m = map[string]any{}
	}
	return m, nil
}

func loopbackPortInUse(port int) bool {
	l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return true
	}
	_ = l.Close()
	return false
}

func existingFiles(paths ...string) []string {
	var out []string
	for _, p := range paths {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			out = append(out, p)
		}
	}
	return out
}

// section returns front[key] as a map (nil when absent or not a map).
func section(m map[string]any, key string) map[string]any {
	v, _ := m[key].(map[string]any)
	return v
}

func hasKey(m map[string]any, key string) bool {
	if m == nil {
		return false
	}
	_, ok := m[key]
	return ok
}

func strValue(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	s, _ := m[key].(string)
	return strings.TrimSpace(s)
}

// --- rules ---------------------------------------------------------------

// rulePerProjectPaths (note 5): worktrees and runtime state left in the old
// shared locations are not migrated.
func rulePerProjectPaths(p upgradeProbe) (string, string, bool) {
	if p.Home == "" {
		return "", "", false
	}
	var found []string
	if strValue(section(p.Front, "workspace"), "root") == "" {
		shared := filepath.Join(p.Home, ".itervox", "workspaces")
		entries, _ := os.ReadDir(shared)
		for _, e := range entries {
			// The old layout put each issue's worktree straight under the
			// shared root; per-project roots hold directories of worktrees.
			if _, err := os.Stat(filepath.Join(shared, e.Name(), ".git")); err == nil {
				found = append(found, fmt.Sprintf("worktrees directly under %s (e.g. %s)", shared, e.Name()))
				break
			}
		}
	}
	if logs := v020LogsDir(p); logs != "" {
		if f := runtimeStateIn(logs); f != "" {
			found = append(found, fmt.Sprintf("runtime state in the v0.2.0 log directory %s (%s)", logs, f))
		}
	}
	if len(found) == 0 {
		return "", "", false
	}
	return "found " + strings.Join(found, " and ") + "; the upgraded daemon starts from empty state in a per-project directory",
		"finish or discard in-flight work before upgrading, or set `workspace.root` explicitly to keep the old worktree path; delete the old directories once you are satisfied",
		true
}

// ruleFixedPort (note 6): with no explicit port the daemon binds 8090, which
// fails when another daemon (or anything else) already holds it.
func ruleFixedPort(p upgradeProbe) (string, string, bool) {
	if hasKey(section(p.Front, "server"), "port") || p.isSet(config.EnvServerPort) || p.isSet(config.EnvGenericPort) {
		return "", "", false
	}
	if p.PortInUse == nil || !p.PortInUse(config.DefaultServerPort) {
		return "", "", false
	}
	if ownDashboardOnPort(p.WorkflowPath, config.DefaultServerPort) {
		return "", "", false // this project's own daemon is the one holding it
	}
	return fmt.Sprintf("server.port is not set, so the daemon binds %d, and another process on this machine already holds it", config.DefaultServerPort),
		"set a distinct `server.port` for this project, `server.port: 0` for an OS-assigned port, or `ITERVOX_SERVER_PORT`",
		true
}

// ownDashboardOnPort reports whether this project's .itervox/dashboard_url
// names port, i.e. the port is held by this project's own daemon.
func ownDashboardOnPort(workflowPath string, port int) bool {
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(workflowPath), ".itervox", "dashboard_url"))
	if err != nil {
		return false
	}
	return strings.Contains(string(raw), ":"+strconv.Itoa(port))
}

// ruleAutoAnalyze (note 9).
func ruleAutoAnalyze(p upgradeProbe) (string, string, bool) {
	deps := section(p.Front, "dependencies")
	if !hasKey(deps, "auto_analyze") {
		return "", "", false
	}
	mode := "auto"
	if v, ok := deps["auto_analyze"].(bool); ok && !v {
		mode = "manual"
	}
	if hasKey(deps, "analysis_mode") {
		return "both `dependencies.auto_analyze` and `analysis_mode` are set; every reload warns about it",
			"delete the `auto_analyze` line (`analysis_mode` already wins)", true
	}
	return "`dependencies.auto_analyze` is set; it still parses, with a startup warning",
		fmt.Sprintf("replace it with `analysis_mode: %s`", mode), true
}

// ruleSSHStrictHost (note 11): values other than the ssh_config set used to
// be dropped silently; now the config does not load.
func ruleSSHStrictHost(p upgradeProbe) (string, string, bool) {
	ag := section(p.Front, "agent")
	var bad []string
	if v, ok := ag["ssh_strict_host_checking"]; ok && v != nil {
		if s, isStr := v.(string); !isStr || !config.IsValidSSHStrictHostMode(s) {
			bad = append(bad, fmt.Sprintf("agent.ssh_strict_host_checking: %v", v))
		}
	}
	if byHost, ok := ag["ssh_strict_host_by_host"].(map[string]any); ok {
		hosts := make([]string, 0, len(byHost))
		for h := range byHost {
			hosts = append(hosts, h)
		}
		slices.Sort(hosts)
		for _, h := range hosts {
			if s, isStr := byHost[h].(string); !isStr || !config.IsValidSSHStrictHostMode(s) {
				bad = append(bad, fmt.Sprintf("agent.ssh_strict_host_by_host[%s]: %v", h, byHost[h]))
			}
		}
	}
	if len(bad) == 0 {
		return "", "", false
	}
	return strings.Join(bad, "; ") + " — before v0.2.1 this was ignored (the daemon ran with accept-new); now the config does not load",
		"use one of `yes`, `no`, `ask`, `accept-new`, `off` (exact, lowercase, quoted)", true
}

// ruleWorkflowLockIgnored (note 12).
func ruleWorkflowLockIgnored(p upgradeProbe) (string, string, bool) {
	gi := filepath.Join(filepath.Dir(p.WorkflowPath), ".gitignore")
	raw, err := os.ReadFile(gi)
	if err != nil {
		return "", "", false // no .gitignore: nothing to patch
	}
	for _, line := range strings.Split(string(raw), "\n") {
		l := strings.TrimSpace(line)
		if l == ".WORKFLOW.md.lock" || l == "/.WORKFLOW.md.lock" || l == ".*.lock" || l == "*.lock" {
			return "", "", false
		}
	}
	return gi + " does not ignore `.WORKFLOW.md.lock`, the edit lock file the daemon now creates next to WORKFLOW.md",
		"run `itervox init --update`, or add the line `.WORKFLOW.md.lock` to .gitignore", true
}

// ruleUnauthenticatedHosts (notes 13 and 14).
func ruleUnauthenticatedHosts(p upgradeProbe) (string, string, bool) {
	srv := section(p.Front, "server")
	unauth := false
	for _, k := range []string{"allow_unauthenticated", "allow_unauthenticated_lan"} {
		if v, ok := srv[k].(bool); ok && v {
			unauth = true
		}
	}
	if !unauth {
		return "", "", false
	}
	if list, ok := srv["allowed_hosts"].([]any); ok && len(list) > 0 {
		return "", "", false
	}
	return "the daemon runs without a token (`allow_unauthenticated: true`) and lists no `server.allowed_hosts`: requests to any host NAME other than localhost or `server.host` are refused (403 `host_not_allowed`), and cross-origin writes get 403 `cross_origin_forbidden`",
		"if you reach it through a reverse proxy, tunnel, MagicDNS or container service name, add that name to `server.allowed_hosts`; if a web page on another origin calls the API, set `ITERVOX_API_TOKEN` and send it as a bearer token", true
}

// ruleSystemdUnit (notes 17 and 22): an installed unit from before v0.2.1
// kills turns on stop instead of letting them drain.
func ruleSystemdUnit(p upgradeProbe) (string, string, bool) {
	for _, unit := range p.SystemdUnits {
		raw, err := os.ReadFile(unit)
		if err != nil {
			continue
		}
		s := string(raw)
		var missing []string
		if !strings.Contains(s, "--shutdown-grace") {
			missing = append(missing, "`--shutdown-grace`")
		}
		if !strings.Contains(s, "KillMode=mixed") {
			missing = append(missing, "`KillMode=mixed`")
		}
		if len(missing) > 0 {
			return fmt.Sprintf("%s lacks %s, so a stop or restart cuts running agent turns short", unit, strings.Join(missing, " and ")),
				"re-install the unit (re-run `bootstrap.sh`, or copy `deploy/systemd/itervox.service` and substitute `@USER@`, `@WORKDIR@`, `@ROOT@`), then `systemctl daemon-reload`; keep `TimeoutStopSec` ≥ shutdown-grace + 35s", true
		}
	}
	return "", "", false
}

// rulePortEnv (note 19). Judged as applyServerEnv does: the first PRESENT
// of ITERVOX_SERVER_PORT and PORT wins, and only the winner is validated.
func rulePortEnv(p upgradeProbe) (string, string, bool) {
	if p.isSet(config.EnvServerPort) || !p.isSet(config.EnvGenericPort) {
		return "", "", false
	}
	port := p.getenv(config.EnvGenericPort)
	if _, err := config.ParseBindPort(port); err != nil {
		return fmt.Sprintf("`PORT=%q` is set and invalid (%v): the daemon now refuses to start", port, err),
			"unset `PORT` for the daemon, or set `ITERVOX_SERVER_PORT` (which wins over `PORT`)", true
	}
	return fmt.Sprintf("`PORT=%s` is set in this environment; the dashboard now binds that port instead of `server.port`", strings.TrimSpace(port)),
		"if `PORT` belongs to another project, unset it for the daemon or set `ITERVOX_SERVER_PORT`", true
}

// ruleLogFormatJSON (note 20).
func ruleLogFormatJSON(p upgradeProbe) (string, string, bool) {
	if !strings.EqualFold(strings.TrimSpace(p.getenv("ITERVOX_LOG_FORMAT")), "json") {
		return "", "", false
	}
	return "`ITERVOX_LOG_FORMAT=json` is set: under systemd or in a container, stderr records are now JSON too",
		"point a log shipper that parses the old text lines at JSON, or run with `text`", true
}

// ruleAllowUnauthenticatedRename (Changed, breaking).
func ruleAllowUnauthenticatedRename(p upgradeProbe) (string, string, bool) {
	srv := section(p.Front, "server")
	if !hasKey(srv, "allow_unauthenticated_lan") {
		return "", "", false
	}
	return "`server.allow_unauthenticated_lan` is set; it still parses as a deprecated alias (with a startup warning) and now disables auth on every bind, loopback included",
		"rename it to `server.allow_unauthenticated`", true
}

// ruleBackendContradictsCommand (CORE-115).
func ruleBackendContradictsCommand(p upgradeProbe) (string, string, bool) {
	ag := section(p.Front, "agent")
	var bad []string
	check := func(where, command, backend string) {
		cmdBackend := agent.BackendFromCommand(command)
		if backend != "" && cmdBackend != "" && backend != cmdBackend {
			bad = append(bad, fmt.Sprintf("%s runs %s but sets backend %s", where, cmdBackend, backend))
		}
	}
	defaultCommand := strValue(ag, "command")
	if defaultCommand == "" {
		defaultCommand = "claude" // config.Load's default agent.command
	}
	check("agent", defaultCommand, strValue(ag, "backend"))
	if profiles, ok := ag["profiles"].(map[string]any); ok {
		names := make([]string, 0, len(profiles))
		for n := range profiles {
			names = append(names, n)
		}
		slices.Sort(names)
		for _, n := range names {
			pm, _ := profiles[n].(map[string]any)
			check("profile "+n, strValue(pm, "command"), strValue(pm, "backend"))
		}
	}
	// A rate_limited rule that switches backend without switching profile
	// keeps the issue's command; over the default command that is refused.
	if autos, ok := p.Front["automations"].([]any); ok {
		for _, a := range autos {
			am, _ := a.(map[string]any)
			pol := section(am, "policy")
			if to := strValue(pol, "switch_to_backend"); to != "" && strValue(pol, "switch_to_profile") == "" {
				check(fmt.Sprintf("automation %v (switch_to_backend over agent.command)", am["id"]), defaultCommand, to)
			}
		}
	}
	if len(bad) == 0 {
		return "", "", false
	}
	return strings.Join(bad, "; ") + "; the backend is now refused with a warning instead of applied",
		"set a backend only for wrapper commands, or point the profile at the other backend's command", true
}

// ruleHeadlessToken (note 10): a daemon run as a service no longer prints the
// ?token= URL to the journal, so an operator who copied it from there needs
// another source.
func ruleHeadlessToken(p upgradeProbe) (string, string, bool) {
	if len(p.SystemdUnits) == 0 || p.getenv("ITERVOX_API_TOKEN") != "" || p.getenv("ITERVOX_PRINT_TOKEN") != "" {
		return "", "", false
	}
	for _, f := range p.SecretFiles {
		if raw, err := os.ReadFile(f); err == nil && strings.Contains(string(raw), "ITERVOX_API_TOKEN=") {
			return "", "", false // pinned for the service
		}
	}
	if v, ok := section(p.Front, "server")["allow_unauthenticated"].(bool); ok && v {
		return "", "", false // no token at all
	}
	return fmt.Sprintf("itervox runs as a service (%s) with an auto-generated token, which is no longer printed on stderr (the journal)", p.SystemdUnits[0]),
		"read `<logs-dir>/api-token` (the startup log line names the path), pin `ITERVOX_API_TOKEN` in `.itervox/.env`, or set `ITERVOX_PRINT_TOKEN=1`", true
}

// ruleLegacyPIDFile (note 15): a daemon started by a pre-v0.2.1 binary wrote
// a two-field PID record and holds no lock, so the new `itervox stop` will
// not signal it.
func ruleLegacyPIDFile(p upgradeProbe) (string, string, bool) {
	pidPath := filepath.Join(filepath.Dir(p.WorkflowPath), ".itervox", "daemon.pid")
	raw, err := os.ReadFile(pidPath)
	if err != nil {
		return "", "", false
	}
	fields := strings.Split(strings.TrimSpace(string(raw)), "\t")
	if len(fields) >= 3 && fields[2] == pidRecordLockMarker {
		return "", "", false
	}
	return fmt.Sprintf("%s was written by a pre-v0.2.1 daemon (no pid lock)", pidPath),
		"stop that daemon with the old binary before upgrading, or run `itervox stop --legacy` after confirming the listed PIDs are itervox", true
}

// v020LogsDir is where a v0.2.0 daemon kept this project's logs and runtime
// state: ~/.itervox/logs/<kind>/<slug>, or the shared ~/.itervox/logs with
// no slug. v0.2.1 moved both to a directory keyed by the workflow path.
func v020LogsDir(p upgradeProbe) string {
	if p.Home == "" {
		return ""
	}
	base := filepath.Join(p.Home, ".itervox", "logs")
	tr := section(p.Front, "tracker")
	kind, slug := strValue(tr, "kind"), strValue(tr, "project_slug")
	if kind == "" || slug == "" {
		return base
	}
	safe := strings.NewReplacer("/", "_", "\\", "_", ":", "_", " ", "_").Replace(slug)
	return filepath.Join(base, kind, safe)
}

// runtimeStateIn returns the first daemon state file found directly in dir.
func runtimeStateIn(dir string) string {
	for _, f := range []string{"automation_queue.json", "history.json", "paused.json", "input_required.json", "auto_switched.json"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
			return f
		}
	}
	return ""
}

// ranBeforeV021 reports signs that a pre-v0.2.1 daemon ran this project:
// state in its v0.2.0 log directory, or a PID record without the lock
// marker. A fresh install has neither.
func ranBeforeV021(p upgradeProbe) bool {
	if logs := v020LogsDir(p); logs != "" && runtimeStateIn(logs) != "" {
		return true
	}
	_, _, legacy := ruleLegacyPIDFile(p)
	return legacy
}

// ruleLoopbackAuth (Changed, breaking, #48): before v0.2.1 a loopback daemon
// served the API without a token; now every bind requires one. Only an
// upgraded project is affected, since a fresh one never had it otherwise.
func ruleLoopbackAuth(p upgradeProbe) (string, string, bool) {
	srv := section(p.Front, "server")
	for _, k := range []string{"allow_unauthenticated", "allow_unauthenticated_lan"} {
		if v, ok := srv[k].(bool); ok && v {
			return "", "", false // auth is off on purpose
		}
	}
	switch strValue(srv, "host") {
	case "", "127.0.0.1", "localhost", "::1", "[::1]":
	default:
		return "", "", false // a non-loopback bind already required a token
	}
	if p.getenv("ITERVOX_API_TOKEN") != "" || !ranBeforeV021(p) {
		return "", "", false
	}
	return "this project ran a pre-v0.2.1 daemon on loopback, where the API needed no token; it now answers 401 without one, so local scripts, bookmarks and the Vite dev proxy need the token",
		"pin `ITERVOX_API_TOKEN` in `.itervox/.env` (or read `<logs-dir>/api-token`, rewritten on every start) and send it as `Authorization: Bearer`; or set `server.allow_unauthenticated: true` to opt out", true
}
