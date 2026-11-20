package referee

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	envPolicy = "tools:\n  convex:\n    guard: scripts/lib/deploy-target.mjs\n"
	// The guard classifies the two variables the old CLI reads.
	envGuard = "// Decides which deployment a deploy may target.\n" +
		"export const TARGET_VARS = ['CONVEX_DEPLOYMENT', 'CONVEX_DEPLOY_KEY'];\n"
	oldCLI = "const d = process.env.CONVEX_DEPLOYMENT;\nconst k = process.env.CONVEX_DEPLOY_KEY;\n"
	// The new version also reads a deploy-target variable the guard does
	// not know: a lockfile bump changes where a deploy goes.
	newCLI = "const d = process.env.CONVEX_DEPLOYMENT;\nconst k = process.env.CONVEX_DEPLOY_KEY;\n" +
		"const t = process.env.CONVEX_TARGET_URL || d;\n"
)

// envRepo is a repository whose base branch has the policy and the guard,
// on a branch "bump" whose lockfile moves the tool to a new version. The
// install (node_modules, gitignored) is written separately: it is what CI
// installed from the head's lockfile.
func envRepo(t *testing.T, policy string) testRepo {
	t.Helper()
	r := newTestRepo(t)
	files := map[string]string{
		".gitignore":                    "node_modules/\n",
		"package.json":                  `{"dependencies":{"convex":"^1.0.0"}}` + "\n",
		"package-lock.json":             `{"packages":{"node_modules/convex":{"version":"1.0.0"}}}` + "\n",
		"scripts/lib/deploy-target.mjs": envGuard,
	}
	if policy != "" {
		files[".referee.yml"] = policy
	}
	r.commit("base", files)
	r.git("checkout", "-q", "-b", "bump")
	r.commit("bump convex", map[string]string{"package-lock.json": `{"packages":{"node_modules/convex":{"version":"1.1.0"}}}` + "\n"})
	return r
}

func (r testRepo) install(cli string) {
	r.t.Helper()
	r.write(map[string]string{
		"node_modules/convex/package.json":        `{"name":"convex"}` + "\n",
		"node_modules/convex/dist/cli.bundle.cjs": cli,
	})
}

// TestEnvcheckLockfileBumpReadsUnclassifiedVar is the motivating case: no
// CI file changed, but the installed CLI now reads CONVEX_TARGET_URL, which
// the guard does not classify. Exit 20, with the tool, the variable and
// the file:line where the install mentions it.
func TestEnvcheckLockfileBumpReadsUnclassifiedVar(t *testing.T) {
	r := envRepo(t, envPolicy)
	r.install(newCLI)
	code, out, errOut := referee(t, nil, "envcheck", "--dir", r.dir, "--base", "main")
	require.Equal(t, exitHuman, code, "stdout=%s stderr=%s", out, errOut)
	assert.Contains(t, out, "envcheck: 1 unclassified deploy-target variable(s)")
	assert.Contains(t, out, "node_modules/convex/dist/cli.bundle.cjs:3  [convex] convex mentions CONVEX_TARGET_URL")
	assert.Contains(t, out, "add CONVEX_TARGET_URL to scripts/lib/deploy-target.mjs")

	code, out, _ = referee(t, nil, "envcheck", "--dir", r.dir, "--base", "main", "--json")
	require.Equal(t, exitHuman, code)
	var rep envReport
	require.NoError(t, json.Unmarshal([]byte(out), &rep))
	assert.Equal(t, 1, rep.Version)
	assert.Equal(t, exitHuman, rep.ExitCode)
	require.Len(t, rep.Findings, 1)
	assert.Equal(t, envFinding{
		Tool: "convex", Variable: "CONVEX_TARGET_URL", Path: "node_modules/convex/dist/cli.bundle.cjs", Line: 3,
		Guard:   "scripts/lib/deploy-target.mjs",
		Message: "convex mentions CONVEX_TARGET_URL, a deploy-target variable the guard on the base ref does not classify",
	}, rep.Findings[0])
	require.Len(t, rep.Tools, 1)
	assert.Equal(t, []string{"CONVEX_DEPLOYMENT", "CONVEX_DEPLOY_KEY", "CONVEX_TARGET_URL"}, rep.Tools[0].Variables)
	assert.Equal(t, "convex --version", rep.Tools[0].Probes[0].Text)
	assert.Equal(t, rep.Policy, ".referee.yml@"+rep.BaseCommit[:12])
}

// TestEnvcheckClassifiedVarsPass: the old install, or a new one whose
// variable the base guard already classifies, passes.
func TestEnvcheckClassifiedVarsPass(t *testing.T) {
	r := envRepo(t, envPolicy)
	r.install(oldCLI)
	code, out, errOut := referee(t, nil, "envcheck", "--dir", r.dir, "--base", "main")
	require.Equal(t, exitOK, code, "stdout=%s stderr=%s", out, errOut)
	assert.Contains(t, out, "every deploy-target variable is classified")
	assert.Contains(t, out, "convex: 2 deploy-target variable(s) in node_modules/convex, guard scripts/lib/deploy-target.mjs")

	// The guard learns the variable on the base branch first; then the bump passes.
	r.git("checkout", "-q", "main")
	r.commit("classify", map[string]string{"scripts/lib/deploy-target.mjs": envGuard + "// CONVEX_TARGET_URL: refused unless it names the staging host\n"})
	r.git("checkout", "-q", "bump")
	r.git("rebase", "-q", "main")
	r.install(newCLI)
	code, out, errOut = referee(t, nil, "envcheck", "--dir", r.dir, "--base", "main")
	require.Equal(t, exitOK, code, "stdout=%s stderr=%s", out, errOut)
}

// TestEnvcheckGuardFromBaseAndHead: the guard is read at the base ref and
// at the head. Classifying the variable only in the pull request does not
// pass (it is the change under review), and a pull request that drops a
// classification from its guard fails even though the base still has it.
func TestEnvcheckGuardFromBaseAndHead(t *testing.T) {
	r := envRepo(t, envPolicy)
	r.commit("classify in the PR", map[string]string{"scripts/lib/deploy-target.mjs": envGuard + "// CONVEX_TARGET_URL\n"})
	r.install(newCLI)
	code, out, _ := referee(t, nil, "envcheck", "--dir", r.dir, "--base", "main")
	assert.Equal(t, exitHuman, code, out)
	assert.Contains(t, out, "CONVEX_TARGET_URL")

	r2 := envRepo(t, envPolicy)
	r2.commit("weaken the guard", map[string]string{"scripts/lib/deploy-target.mjs": "export const TARGET_VARS = ['CONVEX_DEPLOYMENT'];\n"})
	r2.install(oldCLI)
	code, out, _ = referee(t, nil, "envcheck", "--dir", r2.dir, "--base", "main")
	assert.Equal(t, exitHuman, code, out)
	assert.Contains(t, out, "convex mentions CONVEX_DEPLOY_KEY; the guard classifies it on the base ref but no longer at the head")
}

// TestEnvcheckPolicyFromBaseOnly: tools: is read from the base ref. A
// pull request cannot add, drop or repoint a tool for its own check.
func TestEnvcheckPolicyFromBaseOnly(t *testing.T) {
	// No policy on the base; the PR adds one: nothing is checked (exit 0), and
	// the note says convex is used but not configured.
	r := envRepo(t, "")
	r.commit("add a policy", map[string]string{".referee.yml": envPolicy})
	r.install(newCLI)
	code, out, errOut := referee(t, nil, "envcheck", "--dir", r.dir, "--base", "main")
	require.Equal(t, exitOK, code, "stdout=%s stderr=%s", out, errOut)
	assert.Contains(t, out, "envcheck: no tools: configured (policy: none (.referee.yml not on the base ref)); nothing to check")
	assert.Contains(t, out, "note: convex looks used here but is not under tools:")

	// The base configures convex; the PR deletes tools: and points nothing anywhere.
	r2 := envRepo(t, envPolicy)
	r2.commit("drop tools", map[string]string{".referee.yml": "human: []\n"})
	r2.install(newCLI)
	code, _, _ = referee(t, nil, "envcheck", "--dir", r2.dir, "--base", "main")
	assert.Equal(t, exitHuman, code)

	// A policy with lanes but no tools: exit 0.
	r3 := envRepo(t, "human: [\"deploy/**\"]\n")
	r3.install(newCLI)
	code, out, _ = referee(t, nil, "envcheck", "--dir", r3.dir, "--base", "main")
	assert.Equal(t, exitOK, code)
	assert.Contains(t, out, "no tools: configured")
}

// TestEnvcheckGenericAdapter: a tool with no Go code, from env_scan,
// env_pattern, deploy_vars and probes alone.
func TestEnvcheckGenericAdapter(t *testing.T) {
	policy := "tools:\n  mycli:\n    guard: scripts/lib/deploy-target.mjs\n" +
		"    env_scan: node_modules/mycli/dist/**/*.js\n" +
		"    env_pattern: 'process\\.env\\.([A-Z_][A-Z0-9_]*)'\n" +
		"    deploy_vars: [MYCLI_*]\n" +
		"    probes:\n      - mycli deploy --dry-run\n      - mycli --help\n"
	r := envRepo(t, policy)
	r.write(map[string]string{
		"node_modules/mycli/dist/cli.js":     "const a = process.env.MYCLI_TARGET;\nconst h = process.env.HOME;\n",
		"node_modules/mycli/dist/lib/net.js": "\nconst r = process.env.CONVEX_DEPLOYMENT || process.env.MYCLI_REGION;\n",
	})
	code, out, errOut := referee(t, nil, "envcheck", "--dir", r.dir, "--base", "main", "--json")
	require.Equal(t, exitHuman, code, "stdout=%s stderr=%s", out, errOut)
	var rep envReport
	require.NoError(t, json.Unmarshal([]byte(out), &rep))
	require.Len(t, rep.Findings, 2)
	assert.Equal(t, "MYCLI_REGION", rep.Findings[0].Variable)
	assert.Equal(t, "node_modules/mycli/dist/lib/net.js", rep.Findings[0].Path)
	assert.Equal(t, 2, rep.Findings[0].Line)
	assert.Equal(t, "MYCLI_TARGET", rep.Findings[1].Variable)
	assert.Equal(t, []string{"mycli", "deploy", "--dry-run"}, rep.Tools[0].Probes[0].Argv)
}

// TestEnvcheckStepSummary: GITHUB_STEP_SUMMARY gets the findings.
func TestEnvcheckStepSummary(t *testing.T) {
	r := envRepo(t, envPolicy)
	r.install(newCLI)
	summary := filepath.Join(t.TempDir(), "summary.md")
	code, _, _ := referee(t, map[string]string{"GITHUB_STEP_SUMMARY": summary}, "envcheck", "--dir", r.dir, "--base", "main")
	require.Equal(t, exitHuman, code)
	raw, err := os.ReadFile(summary)
	require.NoError(t, err)
	assert.Contains(t, string(raw), "### referee envcheck")
	assert.Contains(t, string(raw), "`CONVEX_TARGET_URL`")
}

// TestEnvcheckFailsClosed: a configured tool whose install or guard
// cannot be read, or a malformed tools: entry, is an error (2), never 0.
func TestEnvcheckFailsClosed(t *testing.T) {
	cases := map[string]struct {
		policy  string
		install bool
		head    map[string]string
	}{
		"install root missing":      {policy: envPolicy},
		"guard not on base":         {policy: "tools:\n  convex:\n    guard: scripts/missing.mjs\n", install: true},
		"guard deleted at head":     {policy: envPolicy, install: true, head: map[string]string{"scripts/lib/deploy-target.mjs": ""}},
		"guard is a directory":      {policy: "tools:\n  convex:\n    guard: scripts/lib\n", install: true},
		"unknown tools key":         {policy: "tools:\n  convex:\n    guard: scripts/lib/deploy-target.mjs\n    gaurd: x\n", install: true},
		"tool without guard":        {policy: "tools:\n  convex: {}\n", install: true},
		"unknown generic tool":      {policy: "tools:\n  mycli:\n    guard: scripts/lib/deploy-target.mjs\n", install: true},
		"tools is a list":           {policy: "tools: [convex]\n", install: true},
		"malformed probe":           {policy: envPolicy + "    probes: ['convex deploy && rm x']\n", install: true},
		"malformed lanes in policy": {policy: "human: [\"[x\"]\n" + envPolicy, install: true},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			r := envRepo(t, c.policy)
			if c.head != nil {
				r.commit("head change", c.head)
			}
			if c.install {
				r.install(oldCLI)
			}
			code, out, errOut := referee(t, nil, "envcheck", "--dir", r.dir, "--base", "main")
			assert.Equal(t, exitError, code, "stdout=%s stderr=%s", out, errOut)
			assert.Contains(t, errOut, "referee envcheck:")
			t.Log(errOut)
		})
	}
}

// TestEnvcheckPolicyWithToolsStillWorksForLanes: tools: is part of the
// policy shape, so lanes accepts a policy that has it.
func TestEnvcheckPolicyWithToolsStillWorksForLanes(t *testing.T) {
	r := envRepo(t, envPolicy)
	code, out, errOut := referee(t, nil, "lanes", "--dir", r.dir, "--base", "main")
	assert.Equal(t, exitOK, code, "stdout=%s stderr=%s", out, errOut)
}

// builtinEnvcheck runs envcheck end to end for one built-in adapter: the
// base guard classifies `classified` and mentions a longer name that starts
// with `unclassified` (which must not count), the head's lockfile bumps
// pkg, and the install mentions both. Exit 20 with exactly one finding, for
// `unclassified` at its file:line; once the base guard learns it, exit 0.
func builtinEnvcheck(t *testing.T, tool, pkg, file, cli, classified, unclassified string, line int) {
	t.Helper()
	policy := "tools:\n  " + tool + ":\n    guard: scripts/lib/deploy-target.mjs\n"
	guard := "export const TARGET_VARS = ['" + classified + "', '" + unclassified + "_SUFFIX'];\n"
	r := newTestRepo(t)
	r.commit("base", map[string]string{
		".gitignore":                    "node_modules/\n",
		".referee.yml":                  policy,
		"package.json":                  `{"devDependencies":{"` + pkg + `":"^1.0.0"}}` + "\n",
		"package-lock.json":             `{"packages":{"node_modules/` + pkg + `":{"version":"1.0.0"}}}` + "\n",
		"scripts/lib/deploy-target.mjs": guard,
	})
	r.git("checkout", "-q", "-b", "bump")
	r.commit("bump "+pkg, map[string]string{"package-lock.json": `{"packages":{"node_modules/` + pkg + `":{"version":"1.1.0"}}}` + "\n"})
	r.write(map[string]string{
		"node_modules/" + pkg + "/package.json": `{"name":"` + pkg + `"}` + "\n",
		"node_modules/" + pkg + "/" + file:      cli,
	})

	code, out, errOut := referee(t, nil, "envcheck", "--dir", r.dir, "--base", "main", "--json")
	require.Equal(t, exitHuman, code, "stdout=%s stderr=%s", out, errOut)
	var rep envReport
	require.NoError(t, json.Unmarshal([]byte(out), &rep))
	require.Len(t, rep.Findings, 1)
	assert.Equal(t, envFinding{
		Tool: tool, Variable: unclassified, Path: "node_modules/" + pkg + "/" + file, Line: line,
		Guard:   "scripts/lib/deploy-target.mjs",
		Message: tool + " mentions " + unclassified + ", a deploy-target variable the guard on the base ref does not classify",
	}, rep.Findings[0])
	require.Len(t, rep.Tools, 1)
	assert.Equal(t, "node_modules/"+pkg, rep.Tools[0].Install)
	assert.Equal(t, []string{classified, unclassified}, rep.Tools[0].Variables)
	assert.Empty(t, rep.Notes, "a configured tool is not also a note")

	r.git("checkout", "-q", "main")
	r.commit("classify", map[string]string{"scripts/lib/deploy-target.mjs": guard + "// " + unclassified + "\n"})
	r.git("checkout", "-q", "bump")
	r.git("rebase", "-q", "main")
	code, out, errOut = referee(t, nil, "envcheck", "--dir", r.dir, "--base", "main")
	require.Equal(t, exitOK, code, "stdout=%s stderr=%s", out, errOut)
	assert.Contains(t, out, "every deploy-target variable is classified")
}

// TestEnvcheckInngestAdapter: the built-in inngest adapter, end to end.
// The guard's INNGEST_SIGNING_KEY_SUFFIX does not classify
// INNGEST_SIGNING_KEY.
func TestEnvcheckInngestAdapter(t *testing.T) {
	builtinEnvcheck(t, "inngest", "inngest", "dist/components/Inngest.js",
		"const env = process.env;\nconst k = env.INNGEST_EVENT_KEY;\nconst s = env.INNGEST_SIGNING_KEY;\nconst n = env.NODE_ENV;\n",
		"INNGEST_EVENT_KEY", "INNGEST_SIGNING_KEY", 3)
}

// TestEnvcheckSupabaseAdapter: the built-in supabase adapter, end to end.
// The guard's SUPABASE_DB_PASSWORD_SUFFIX does not classify
// SUPABASE_DB_PASSWORD.
func TestEnvcheckSupabaseAdapter(t *testing.T) {
	builtinEnvcheck(t, "supabase", "supabase", "scripts/postinstall.js",
		"const t = process.env.SUPABASE_ACCESS_TOKEN;\nconst h = process.env.HOME;\nconst p = process.env.SUPABASE_DB_PASSWORD;\n",
		"SUPABASE_ACCESS_TOKEN", "SUPABASE_DB_PASSWORD", 3)
}

// TestEnvcheckNotesUnconfiguredBuiltins: a repository that uses Inngest
// (through its CLI alone) and Supabase (through supabase/config.toml
// alone) gets a note for each when tools: leaves them out.
func TestEnvcheckNotesUnconfiguredBuiltins(t *testing.T) {
	r := envRepo(t, envPolicy)
	r.install(oldCLI)
	r.write(map[string]string{
		"package.json":         `{"dependencies":{"convex":"^1.0.0"},"devDependencies":{"inngest-cli":"^1.0.0"}}` + "\n",
		"supabase/config.toml": "project_id = \"fixture\"\n",
	})
	code, out, errOut := referee(t, nil, "envcheck", "--dir", r.dir, "--base", "main")
	require.Equal(t, exitOK, code, "stdout=%s stderr=%s", out, errOut)
	assert.Contains(t, out, "note: inngest looks used here but is not under tools:")
	assert.Contains(t, out, "note: supabase looks used here but is not under tools:")
	assert.NotContains(t, out, "note: vercel")
}
