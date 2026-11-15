package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
)

// writeLanesWorkflow writes a WORKFLOW.md with the given guardrails block
// and an "impl" profile carrying profileExtra, and returns its path.
func writeLanesWorkflow(t *testing.T, guardrailsBlock, profileExtra string) string {
	t.Helper()
	t.Setenv(config.UserSettingsEnv, "off")
	dir := t.TempDir()
	workflowPath := filepath.Join(dir, "WORKFLOW.md")
	content := "---\nitervox_schema_version: 2\ntracker:\n  kind: linear\n  api_key: key\n  project_slug: proj\n" +
		guardrailsBlock +
		"agent:\n  command: claude\n  profiles:\n    impl:\n      command: claude\n" + profileExtra +
		testProfileFileFields(t, dir, "impl") + "---\n\nPrompt.\n"
	require.NoError(t, os.WriteFile(workflowPath, []byte(content), 0o644))
	return workflowPath
}

// loadLanesWorkflow loads writeLanesWorkflow's file the way doctor does.
func loadLanesWorkflow(t *testing.T, guardrailsBlock, profileExtra string) *config.Config {
	t.Helper()
	cfg, err := config.Load(writeLanesWorkflow(t, guardrailsBlock, profileExtra))
	require.NoError(t, err)
	return cfg
}

func renderLanes(c LanesCheck) string {
	var b strings.Builder
	renderLanesCheck(&b, c)
	return b.String()
}

// TestDoctorLanesWarnsMakefileOutsideHumanLane: a verify check that runs
// `make` is only as trustworthy as the Makefile; when the human lane does
// not cover it, doctor names the profile, check, command and file. With the
// default lane (which has Makefile) the same check is fine, while an npm
// check still warns about package.json; `go test` never warns. Warnings do
// not change the exit code.
func TestDoctorLanesWarnsMakefileOutsideHumanLane(t *testing.T) {
	verify := "      verify:\n" +
		"        - name: test\n          run: make test\n" +
		"        - name: web\n          run: \"cd web && pnpm test\"\n" +
		"        - name: unit\n          run: \"go test ./... 2>&1 | tail -n 40\"\n"

	cfg := loadLanesWorkflow(t, "guardrails:\n  lanes:\n    human: [\".github/**\", \"WORKFLOW.md\"]\n", verify)
	c := checkLanes(cfg)
	out := renderLanes(c)
	assert.Contains(t, out, "\nLanes\n  human: .github/**, WORKFLOW.md\n  second_opinion: off\n")
	assert.NotContains(t, out, "(default)")
	assert.Contains(t, out, "WARNING: impl: verify check test runs `make test`, defined by Makefile, which the agent can change without a human; add it to guardrails.lanes.human\n")
	assert.Contains(t, out, "WARNING: impl: verify check web runs `cd web && pnpm test`, defined by web/package.json, which the agent can change without a human; add it to guardrails.lanes.human\n")
	assert.NotContains(t, out, "verify check unit", "go test runs the code under test, not a definition file")
	assert.Equal(t, 0, doctorExitCode(DoctorReport{SchemaPassed: true, Lanes: c}), "warnings do not change the exit code")

	// The default human lane has Makefile (matched by the real lane
	// matcher), so only the package.json warning remains.
	cfg = loadLanesWorkflow(t, "", verify)
	out = renderLanes(checkLanes(cfg))
	assert.Contains(t, out, "  human: "+strings.Join(config.DefaultGuardrailHumanLane, ", ")+" (default)\n")
	assert.NotContains(t, out, "verify check test runs")
	assert.Contains(t, out, "verify check web runs `cd web && pnpm test`, defined by web/package.json")

	// A lane glob that covers the file at any depth silences the warning.
	cfg = loadLanesWorkflow(t, "guardrails:\n  lanes:\n    human: [Makefile, package.json]\n    second_opinion: [\"src/auth/**\"]\n", verify)
	out = renderLanes(checkLanes(cfg))
	assert.NotContains(t, out, "WARNING")
	assert.Contains(t, out, "  second_opinion: src/auth/**\n")
}

// TestDoctorLanesOff: `guardrails.lanes.human: []` shows as off and warns,
// and so does turning the guardrails off altogether; neither changes the
// exit code.
func TestDoctorLanesOff(t *testing.T) {
	cfg := loadLanesWorkflow(t, "guardrails:\n  lanes:\n    human: []\n", "")
	c := checkLanes(cfg)
	out := renderLanes(c)
	assert.Contains(t, out, "  human: off\n")
	assert.Contains(t, out, "WARNING: the human lane is off (guardrails.lanes.human: [])")
	assert.Equal(t, 0, doctorExitCode(DoctorReport{SchemaPassed: true, Lanes: c}))

	cfg = loadLanesWorkflow(t, "guardrails:\n  enabled: false\n", "")
	out = renderLanes(checkLanes(cfg))
	assert.Contains(t, out, "WARNING: guardrails.enabled is false, so no lane applies")

	cfg = loadLanesWorkflow(t, "", "")
	out = renderLanes(checkLanes(cfg))
	assert.NotContains(t, out, "WARNING", "the default lane on: nothing to warn about")

	assert.Empty(t, renderLanes(checkLanes(nil)), "no configuration: no section")
}

// TestDoctorLanesSelfReportedEvidence: a require_evidence name no verify
// check runs is backed only by the agent's own evidence file, so doctor
// says so (info, not a warning). "ci" is the pull request's CI checks and a
// name in verify is daemon-run; neither is listed.
func TestDoctorLanesSelfReportedEvidence(t *testing.T) {
	cfg := loadLanesWorkflow(t, "",
		"      require_evidence: [test, lint, ci, docs]\n      verify:\n        - name: test\n          run: go test ./...\n")
	c := checkLanes(cfg)
	require.Len(t, c.SelfReported, 1)
	assert.Equal(t, []string{"lint", "docs"}, c.SelfReported[0].Checks)
	out := renderLanes(c)
	assert.Contains(t, out, "  impl: require_evidence lint, docs are agent-reported, not daemon-run")
	assert.NotContains(t, out, "WARNING")
	assert.Equal(t, 0, doctorExitCode(DoctorReport{SchemaPassed: true, Lanes: c}))
}

// TestVerifyCommandFiles pins the rough shell reading behind the warning.
func TestVerifyCommandFiles(t *testing.T) {
	for run, want := range map[string][]string{
		"make test":                       {"Makefile"},
		"CI=1 make -j4 check":             {"Makefile"},
		"cd web && npm test":              {"web/package.json"},
		"go test ./... && go vet ./...":   nil,
		"./scripts/verify.sh":             {"scripts/verify.sh"},
		"bash scripts/ci.sh --fast":       {"scripts/ci.sh"},
		"python -m pytest -q":             {"pytest.ini", "pyproject.toml", "setup.cfg", "tox.ini", "conftest.py"},
		"golangci-lint run | tail -n 20":  nil,
		"cd /abs && make":                 {"Makefile"},
		"sh /opt/ci/check.sh":             nil,
		"yarn lint; (cd docs && just ci)": {"package.json", "docs/justfile"},
	} {
		assert.Equal(t, want, verifyCommandFiles(run), run)
	}
}

// TestConfigShowListsLanes: `config show` ends with the same Lanes summary
// as doctor, after "How agents run".
func TestConfigShowListsLanes(t *testing.T) {
	path := writeLanesWorkflow(t, "guardrails:\n  lanes:\n    human: []\n", "      verify:\n        - name: test\n          run: make test\n")
	var out, errOut strings.Builder
	require.Equal(t, 0, configShow(path, false, &out, &errOut), errOut.String())
	s := out.String()
	runs := strings.Index(s, "\nHow agents run\n")
	lanes := strings.Index(s, "\nLanes\n")
	require.NotEqual(t, -1, runs)
	require.Greater(t, lanes, runs, "Lanes follows How agents run")
	assert.Contains(t, s[lanes:], "  human: off\n")
	assert.Contains(t, s[lanes:], "WARNING: the human lane is off")
	assert.Contains(t, s[lanes:], "verify check test runs `make test`, defined by Makefile")
}
