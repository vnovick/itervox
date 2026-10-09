package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// upgradeFixture is one machine for the #82 rules: a project directory with
// a WORKFLOW.md, a home directory, an environment and the port state.
type upgradeFixture struct {
	front     string            // WORKFLOW.md front matter (without the --- lines)
	env       map[string]string // environment
	portInUse bool              // 127.0.0.1:8090 held by another process
	files     map[string]string // project-relative files to create
	homeFiles map[string]string // home-relative files to create
	unit      string            // installed systemd unit content ("" = none)
	secrets   string            // service EnvironmentFile content ("" = none)
}

func (f upgradeFixture) probe(t *testing.T) upgradeProbe {
	t.Helper()
	dir, home := t.TempDir(), t.TempDir()
	write := func(base, rel, body string) {
		p := filepath.Join(base, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
	}
	workflow := filepath.Join(dir, "WORKFLOW.md")
	write(dir, "WORKFLOW.md", "---\n"+f.front+"\n---\n\nPrompt.\n")
	for rel, body := range f.files {
		write(dir, rel, body)
	}
	for rel, body := range f.homeFiles {
		write(home, rel, body)
	}
	var units []string
	if f.unit != "" {
		write(dir, "itervox.service", f.unit)
		units = []string{filepath.Join(dir, "itervox.service")}
	}
	var secrets []string
	if f.secrets != "" {
		write(dir, "secrets.env", f.secrets)
		secrets = []string{filepath.Join(dir, "secrets.env")}
	}
	front, err := readRawFrontMatter(workflow)
	require.NoError(t, err)
	return upgradeProbe{
		WorkflowPath: workflow,
		Front:        front,
		Lookup: func(k string) (string, bool) {
			v, ok := f.env[k]
			return v, ok
		},
		Home:         home,
		PortInUse:    func(int) bool { return f.portInUse },
		SystemdUnits: units,
		SecretFiles:  secrets,
	}
}

// notesFor runs every rule and returns the notes that applied.
func notesFor(t *testing.T, f upgradeFixture) []string {
	t.Helper()
	var notes []string
	for _, fd := range checkUpgradeNotes(f.probe(t)) {
		notes = append(notes, fd.Note)
	}
	return notes
}

// cleanFront is a current v0.2.2 workflow with nothing left to change.
const cleanFront = "itervox_schema_version: 2\ntracker:\n  kind: github\n  project_slug: o/r\nserver:\n  port: 8091"

const currentUnit = "[Service]\nExecStart=/usr/local/bin/itervox --shutdown-grace 240s\nKillMode=mixed\nTimeoutStopSec=300\n"

// TestUpgradeDoctorAllClear (#82): a config with nothing to change prints
// only the all-clear line.
func TestUpgradeDoctorAllClear(t *testing.T) {
	p := upgradeFixture{front: cleanFront, portInUse: true,
		env:   map[string]string{"ITERVOX_API_TOKEN": "x"},
		files: map[string]string{".gitignore": ".WORKFLOW.md.lock\n", ".itervox/daemon.pid": "123\t/x/WORKFLOW.md\tflock\n"},
		unit:  currentUnit,
	}.probe(t)
	findings := checkUpgradeNotes(p)
	assert.Empty(t, findings)
	out := renderUpgradeReport("WORKFLOW.md", findings)
	assert.Equal(t, "upgrade: all clear — none of the v0.2.1 upgrade notes need a change in WORKFLOW.md or this environment.\n", out)
}

// TestUpgradeDoctorRules (#82): every rule, with a fixture where it applies
// and one where it does not. Each "applies" case must yield exactly that
// rule's note, so one rule cannot pass on another's finding.
func TestUpgradeDoctorRules(t *testing.T) {
	cases := []struct {
		note       string
		applies    upgradeFixture
		notApplies upgradeFixture
		alsoNotes  []string // other notes the applies fixture necessarily triggers
	}{
		{note: "note 5",
			applies:    upgradeFixture{front: "tracker:\n  kind: linear\nserver:\n  port: 0", homeFiles: map[string]string{".itervox/workspaces/ENG-1/.git": "gitdir: x"}},
			notApplies: upgradeFixture{front: "tracker:\n  kind: linear\nserver:\n  port: 0\nworkspace:\n  root: ~/.itervox/workspaces", homeFiles: map[string]string{".itervox/workspaces/ENG-1/.git": "gitdir: x"}}},
		{note: "note 5",
			applies:    upgradeFixture{front: "tracker:\n  kind: linear\nserver:\n  port: 0\n  allow_unauthenticated: true\n  allowed_hosts: [a.example.com]", homeFiles: map[string]string{".itervox/logs/paused.json": "{}"}},
			notApplies: upgradeFixture{front: "tracker:\n  kind: linear\n  project_slug: p\nserver:\n  port: 0", homeFiles: map[string]string{".itervox/logs/paused.json": "{}"}}},
		{note: "note 5", // v0.2.0 kept a slug project's state in logs/<kind>/<slug>
			applies:    upgradeFixture{front: cleanFront + "\n  allow_unauthenticated: true\n  allowed_hosts: [a.example.com]", homeFiles: map[string]string{".itervox/logs/github/o_r/history.json": "{}"}},
			notApplies: upgradeFixture{front: cleanFront, homeFiles: map[string]string{".itervox/logs/github/other_repo/history.json": "{}"}}},
		{note: "note 6",
			applies:    upgradeFixture{front: "tracker:\n  kind: github\n  project_slug: o/r", portInUse: true},
			notApplies: upgradeFixture{front: "tracker:\n  kind: github\n  project_slug: o/r", portInUse: false}},
		{note: "note 6",
			applies: upgradeFixture{front: "tracker:\n  kind: github\n  project_slug: o/r", portInUse: true},
			notApplies: upgradeFixture{front: "tracker:\n  kind: github\n  project_slug: o/r", portInUse: true,
				files: map[string]string{".itervox/dashboard_url": "http://127.0.0.1:8090/?token=x"}}},
		{note: "note 9",
			applies:    upgradeFixture{front: cleanFront + "\ndependencies:\n  auto_analyze: false"},
			notApplies: upgradeFixture{front: cleanFront + "\ndependencies:\n  analysis_mode: manual"}},
		{note: "note 10",
			applies:    upgradeFixture{front: cleanFront, unit: currentUnit},
			notApplies: upgradeFixture{front: cleanFront, unit: currentUnit, env: map[string]string{"ITERVOX_API_TOKEN": "pinned"}}},
		{note: "note 10",
			applies:    upgradeFixture{front: cleanFront, unit: currentUnit, secrets: "GITHUB_TOKEN=x\n"},
			notApplies: upgradeFixture{front: cleanFront, unit: currentUnit, secrets: "ITERVOX_API_TOKEN=pinned\n"}},
		{note: "note 11",
			applies:    upgradeFixture{front: cleanFront + "\nagent:\n  ssh_strict_host_by_host:\n    build1: Yes"},
			notApplies: upgradeFixture{front: cleanFront + "\nagent:\n  ssh_strict_host_checking: \"yes\"\n  ssh_strict_host_by_host:\n    build1: accept-new"}},
		{note: "note 12",
			applies:    upgradeFixture{front: cleanFront, files: map[string]string{".gitignore": "node_modules/\n"}},
			notApplies: upgradeFixture{front: cleanFront, files: map[string]string{".gitignore": "node_modules/\n.WORKFLOW.md.lock\n"}}},
		{note: "notes 13 and 14",
			applies:    upgradeFixture{front: cleanFront + "\n  allow_unauthenticated: true"},
			notApplies: upgradeFixture{front: cleanFront + "\n  allow_unauthenticated: true\n  allowed_hosts: [itervox.example.com]"}},
		{note: "note 15",
			applies:    upgradeFixture{front: cleanFront, files: map[string]string{".itervox/daemon.pid": "123\t/x/WORKFLOW.md\n"}},
			notApplies: upgradeFixture{front: cleanFront, files: map[string]string{".itervox/daemon.pid": "123\t/x/WORKFLOW.md\tflock\n"}},
			alsoNotes:  []string{"breaking (#48)"}}, // an old daemon ran here on loopback without a token
		{note: "notes 17 and 22",
			applies:    upgradeFixture{front: cleanFront, env: map[string]string{"ITERVOX_API_TOKEN": "x"}, unit: "[Service]\nExecStart=/usr/local/bin/itervox\nTimeoutStopSec=90\n"},
			notApplies: upgradeFixture{front: cleanFront, env: map[string]string{"ITERVOX_API_TOKEN": "x"}, unit: currentUnit}},
		{note: "note 19",
			applies:    upgradeFixture{front: cleanFront, env: map[string]string{"PORT": "3000"}},
			notApplies: upgradeFixture{front: cleanFront, env: map[string]string{"PORT": "3000", "ITERVOX_SERVER_PORT": "8091"}}},
		{note: "note 19", // set but empty is present, and invalid
			applies:    upgradeFixture{front: cleanFront, env: map[string]string{"PORT": ""}},
			notApplies: upgradeFixture{front: cleanFront, env: map[string]string{"PORT": "", "ITERVOX_SERVER_PORT": "8091"}}},
		{note: "note 19",
			applies:    upgradeFixture{front: cleanFront, env: map[string]string{"PORT": "70000"}},
			notApplies: upgradeFixture{front: cleanFront}},
		{note: "note 20",
			applies:    upgradeFixture{front: cleanFront, env: map[string]string{"ITERVOX_LOG_FORMAT": "json"}},
			notApplies: upgradeFixture{front: cleanFront, env: map[string]string{"ITERVOX_LOG_FORMAT": "text"}}},
		{note: "breaking",
			applies:    upgradeFixture{front: cleanFront + "\n  allow_unauthenticated_lan: false"},
			notApplies: upgradeFixture{front: cleanFront + "\n  allow_unauthenticated: false"}},
		{note: "upgrade notes (CORE-115)",
			applies:    upgradeFixture{front: cleanFront + "\nagent:\n  profiles:\n    impl:\n      command: claude\n      backend: codex"},
			notApplies: upgradeFixture{front: cleanFront + "\nagent:\n  profiles:\n    impl:\n      command: my-wrapper\n      backend: codex"}},
		{note: "upgrade notes (CORE-115)", // agent.command defaults to claude
			applies:    upgradeFixture{front: cleanFront + "\nagent:\n  backend: codex"},
			notApplies: upgradeFixture{front: cleanFront + "\nagent:\n  command: codex\n  backend: codex"}},
		{note: "upgrade notes (CORE-115)",
			applies:    upgradeFixture{front: cleanFront + "\nautomations:\n  - id: rl\n    profile: impl\n    trigger:\n      type: rate_limited\n    policy:\n      switch_to_backend: codex"},
			notApplies: upgradeFixture{front: cleanFront + "\nagent:\n  profiles:\n    codex-impl:\n      command: codex\nautomations:\n  - id: rl\n    profile: impl\n    trigger:\n      type: rate_limited\n    policy:\n      switch_to_profile: codex-impl\n      switch_to_backend: codex"}},
		{note: "upgrade notes (CORE-115)", // v0.2.0 accepted a switch profile whose command contradicts switch_to_backend
			applies:    upgradeFixture{front: cleanFront + "\nagent:\n  profiles:\n    fallback:\n      command: claude\nautomations:\n  - id: rl\n    profile: impl\n    trigger:\n      type: rate_limited\n    policy:\n      switch_to_profile: fallback\n      switch_to_backend: codex"},
			notApplies: upgradeFixture{front: cleanFront + "\nagent:\n  profiles:\n    fallback:\n      command: claude\nautomations:\n  - id: rl\n    profile: impl\n    trigger:\n      type: rate_limited\n    policy:\n      switch_to_profile: fallback\n      switch_to_backend: claude"}},
		{note: "breaking (#48)",
			applies:    upgradeFixture{front: cleanFront, homeFiles: map[string]string{".itervox/logs/github/o_r/paused.json": "{}"}},
			notApplies: upgradeFixture{front: cleanFront, homeFiles: map[string]string{".itervox/logs/github/o_r/paused.json": "{}"}, env: map[string]string{"ITERVOX_API_TOKEN": "pinned"}},
			alsoNotes:  []string{"note 5"}},
		{note: "breaking (#48)", // a non-loopback bind already required a token; slugless shared state is not this project's
			applies:    upgradeFixture{front: cleanFront, homeFiles: map[string]string{".itervox/logs/github/o_r/paused.json": "{}"}},
			notApplies: upgradeFixture{front: cleanFront + "\n  host: 0.0.0.0", homeFiles: map[string]string{".itervox/logs/github/o_r/paused.json": "{}"}},
			alsoNotes:  []string{"note 5"}},
		{note: "note 5", // another slugless project's v0.2.0 state: note 5 (the dir was shared), but not #48
			applies:    upgradeFixture{front: "tracker:\n  kind: github\nserver:\n  port: 8091", homeFiles: map[string]string{".itervox/logs/paused.json": "{}"}},
			notApplies: upgradeFixture{front: cleanFront}},
		{note: "breaking (#48)", // a fresh install never served without a token
			applies:    upgradeFixture{front: cleanFront, files: map[string]string{".itervox/daemon.pid": "123\t/x/WORKFLOW.md\n"}},
			notApplies: upgradeFixture{front: cleanFront},
			alsoNotes:  []string{"note 15"}},
	}
	for _, tc := range cases {
		t.Run(tc.note, func(t *testing.T) {
			assert.ElementsMatch(t, append([]string{tc.note}, tc.alsoNotes...), notesFor(t, tc.applies), "applies")
			assert.NotContains(t, notesFor(t, tc.notApplies), tc.note, "does not apply")
		})
	}
	// Every rule has at least one case.
	covered := map[string]bool{}
	for _, tc := range cases {
		covered[tc.note] = true
	}
	for _, r := range upgradeRules {
		assert.True(t, covered[r.Note], "rule %q has no fixture", r.Note)
	}
}

// TestUpgradeDoctorReportsFixAndLink (#82): each finding prints why, the
// fix and the link to its CHANGELOG note; the exit code is 1.
func TestUpgradeDoctorReportsFixAndLink(t *testing.T) {
	dir := t.TempDir()
	workflow := filepath.Join(dir, "WORKFLOW.md")
	require.NoError(t, os.WriteFile(workflow, []byte("---\n"+cleanFront+"\n  allow_unauthenticated_lan: true\n  allowed_hosts: [a.example.com]\n---\nPrompt.\n"), 0o644))
	t.Setenv("HOME", t.TempDir()) // no worktrees or state from the real home
	t.Setenv("PORT", "")
	t.Setenv("ITERVOX_SERVER_PORT", "")
	t.Setenv("ITERVOX_LOG_FORMAT", "")
	out, code := runUpgradeDoctor(workflow)
	assert.Equal(t, 1, code)
	assert.Contains(t, out, "upgrade: 1 v0.2.1 upgrade note(s) apply to "+workflow)
	assert.Contains(t, out, "[breaking] server.allow_unauthenticated_lan was renamed to server.allow_unauthenticated")
	assert.Contains(t, out, "  fix:  rename it to `server.allow_unauthenticated`")
	assert.Contains(t, out, "  see:  "+upgradeNotesURL+" (breaking)")

	// An unreadable workflow is exit 2, not a silent all clear.
	out, code = runUpgradeDoctor(filepath.Join(dir, "missing.md"))
	assert.Equal(t, 2, code)
	assert.True(t, strings.HasPrefix(out, "upgrade: "))
}

// TestUpgradeDoctorReadsConfigsThatNoLongerLoad (#82): the command must
// explain a config that v0.2.1 refuses to load, so it reads the front matter
// raw instead of through config.Load.
func TestUpgradeDoctorReadsConfigsThatNoLongerLoad(t *testing.T) {
	notes := notesFor(t, upgradeFixture{front: cleanFront + "\nagent:\n  ssh_strict_host_checking: strict\n  ssh_strict_host_by_host:\n    b1: Yes"})
	assert.Equal(t, []string{"note 11"}, notes)
}
