package adapters

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func load(t *testing.T, name string, c Config) Tool {
	t.Helper()
	if c.Guard == "" {
		c.Guard = "scripts/deploy-guard.mjs"
	}
	tool, err := Load(name, c)
	require.NoError(t, err)
	return tool
}

func list(s ...string) *[]string { return &s }

func names(reads []EnvRead) []string {
	out := []string{}
	for _, r := range reads {
		out = append(out, r.Name)
	}
	return out
}

// TestAdapterConvexFixture: the built-in convex adapter finds every
// CONVEX_* word in the installed JS files (a property read, a destructured
// read, a bracket read, a message), with its first file:line, and nothing
// else: not HOME, not a longer word that contains the prefix, not a
// lowercase name, not a file that is not JS.
func TestAdapterConvexFixture(t *testing.T) {
	tool := load(t, "convex", Config{})
	assert.Equal(t, "convex", tool.Name())
	assert.Equal(t, "node_modules/convex", tool.InstallRoot())
	assert.True(t, tool.Detect("testdata/convex/repo"))
	assert.False(t, tool.Detect("testdata/none/repo"), "convex-helpers is not convex")
	assert.True(t, tool.DeployVar("CONVEX_DEPLOYMENT"))
	assert.False(t, tool.DeployVar("VERCEL_TOKEN"))

	reads, err := tool.EnvReads("testdata/convex/install")
	require.NoError(t, err)
	assert.Equal(t, []string{"CONVEX_DEPLOYMENT", "CONVEX_DEPLOY_KEY", "CONVEX_SELF_HOSTED_URL"}, names(reads))
	assert.Equal(t, EnvRead{Name: "CONVEX_DEPLOYMENT", Path: "dist/cli.bundle.cjs", Line: 3, Count: 2}, reads[0])
	assert.Equal(t, 4, reads[1].Line)
	assert.Equal(t, 5, reads[2].Line)

	var probes []string
	for _, p := range tool.Probes() {
		probes = append(probes, p.Text)
		assert.Equal(t, "convex", p.Argv[0])
	}
	assert.Equal(t, []string{"convex --version", "convex deploy --help"}, probes)
}

// TestAdapterVercelFixture: the same for vercel, on a minified bundle
// where the variables are read through an alias of process.env.
func TestAdapterVercelFixture(t *testing.T) {
	tool := load(t, "vercel", Config{})
	assert.Equal(t, "node_modules/vercel", tool.InstallRoot())
	assert.True(t, tool.Detect("testdata/vercel/repo"))
	assert.False(t, tool.Detect("testdata/convex/repo"))
	reads, err := tool.EnvReads("testdata/vercel/install")
	require.NoError(t, err)
	assert.Equal(t, []string{"VERCEL_ORG_ID", "VERCEL_PROJECT_ID", "VERCEL_TOKEN"}, names(reads))
	assert.Equal(t, EnvRead{Name: "VERCEL_ORG_ID", Path: "dist/index.js", Line: 2, Count: 2}, reads[0])
}

// TestAdapterInngestFixture: the inngest adapter scans the SDK
// (node_modules/inngest) and finds every INNGEST_* word in its JS files
// (an alias of process.env, a bracket read, a destructured read, a
// message), with its first file:line: INNGEST_SIGNING_KEY and the longer
// INNGEST_SIGNING_KEY_FALLBACK are separate words, and a longer word that
// contains the prefix, a lowercase name, NODE_ENV and a README are not
// mentions. A dependency on the SDK or on the CLI shows it is used; the
// probes run the CLI.
func TestAdapterInngestFixture(t *testing.T) {
	tool := load(t, "inngest", Config{})
	assert.Equal(t, "inngest", tool.Name())
	assert.Equal(t, "node_modules/inngest", tool.InstallRoot())
	assert.True(t, tool.Detect("testdata/inngest/repo"), "the SDK")
	assert.True(t, tool.Detect("testdata/inngest/repo-cli"), "the CLI alone")
	assert.False(t, tool.Detect("testdata/none/repo"), "inngest-fixture-helpers is not inngest")
	assert.False(t, tool.Detect("testdata/supabase/repo"))
	assert.True(t, tool.DeployVar("INNGEST_EVENT_KEY"))
	assert.False(t, tool.DeployVar("SUPABASE_ACCESS_TOKEN"))

	reads, err := tool.EnvReads("testdata/inngest/install")
	require.NoError(t, err)
	assert.Equal(t, []string{"INNGEST_BASE_URL", "INNGEST_DEV", "INNGEST_ENV", "INNGEST_EVENT_KEY", "INNGEST_SIGNING_KEY", "INNGEST_SIGNING_KEY_FALLBACK"}, names(reads))
	assert.Equal(t, EnvRead{Name: "INNGEST_SIGNING_KEY", Path: "components/Inngest.js", Line: 4, Count: 2}, reads[4])
	assert.Equal(t, EnvRead{Name: "INNGEST_SIGNING_KEY_FALLBACK", Path: "components/Inngest.js", Line: 5, Count: 1}, reads[5])
	assert.Equal(t, 6, reads[2].Line)
	assert.Equal(t, 7, reads[0].Line)

	var probes []string
	for _, p := range tool.Probes() {
		probes = append(probes, p.Text)
		assert.Equal(t, "inngest-cli", p.Argv[0])
	}
	assert.Equal(t, []string{"inngest-cli --help", "inngest-cli dev --help"}, probes)
}

// TestAdapterSupabaseFixture: the supabase adapter scans the CLI's npm
// package for SUPABASE_* words, and is detected by a dependency on the CLI
// or by supabase/config.toml alone (a project with the CLI installed
// elsewhere). The client library (@supabase/supabase-js) is not the CLI.
func TestAdapterSupabaseFixture(t *testing.T) {
	tool := load(t, "supabase", Config{})
	assert.Equal(t, "supabase", tool.Name())
	assert.Equal(t, "node_modules/supabase", tool.InstallRoot())
	assert.True(t, tool.Detect("testdata/supabase/repo"), "the CLI dependency")
	assert.True(t, tool.Detect("testdata/supabase/repo-config"), "supabase/config.toml")
	assert.False(t, tool.Detect("testdata/none/repo"), "@supabase/supabase-js is not the CLI")
	assert.False(t, tool.Detect("testdata/inngest/repo"))
	assert.True(t, tool.DeployVar("SUPABASE_DB_PASSWORD"))
	assert.False(t, tool.DeployVar("INNGEST_EVENT_KEY"))

	reads, err := tool.EnvReads("testdata/supabase/install")
	require.NoError(t, err)
	assert.Equal(t, []string{"SUPABASE_ACCESS_TOKEN", "SUPABASE_DB_PASSWORD", "SUPABASE_PROJECT_ID"}, names(reads))
	assert.Equal(t, EnvRead{Name: "SUPABASE_ACCESS_TOKEN", Path: "scripts/postinstall.js", Line: 3, Count: 2}, reads[0])
	assert.Equal(t, 4, reads[1].Line)
	assert.Equal(t, 5, reads[2].Line)

	var probes []string
	for _, p := range tool.Probes() {
		probes = append(probes, p.Text)
		assert.Equal(t, "supabase", p.Argv[0])
	}
	assert.Equal(t, []string{"supabase --version", "supabase db push --help", "supabase functions deploy --help"}, probes)
}

// TestAdapterBuiltinOverrides: a built-in's install, deploy_vars and
// probes can be replaced in the policy.
func TestAdapterBuiltinOverrides(t *testing.T) {
	tool := load(t, "convex", Config{Install: "packages/app/node_modules/convex", DeployVars: list("CONVEX_DEPLOY_KEY"), Probes: list("convex --help")})
	assert.Equal(t, "packages/app/node_modules/convex", tool.InstallRoot())
	reads, err := tool.EnvReads("testdata/convex/install")
	require.NoError(t, err)
	assert.Equal(t, []string{"CONVEX_DEPLOY_KEY"}, names(reads))
	require.Len(t, tool.Probes(), 1)
	assert.Equal(t, []string{"convex", "--help"}, tool.Probes()[0].Argv)

	none := load(t, "vercel", Config{Probes: list()})
	assert.Empty(t, none.Probes())
}

// TestAdapterGenericFixture: a generic adapter from the policy alone, with
// an env_pattern: only names the pattern captures, that match deploy_vars,
// in files env_scan matches (dist/**/*.js, not docs/).
func TestAdapterGenericFixture(t *testing.T) {
	tool := load(t, "mycli", Config{
		EnvScan:    "node_modules/mycli/dist/**/*.js",
		EnvPattern: `process\.env(?:\.|\[["'])([A-Z_][A-Z0-9_]*)`,
		DeployVars: list("MYCLI_*"),
		Probes:     list("mycli deploy --dry-run", "mycli --help"),
	})
	assert.Equal(t, "node_modules/mycli/dist", tool.InstallRoot())
	reads, err := tool.EnvReads("testdata/mycli/install/dist")
	require.NoError(t, err)
	assert.Equal(t, []string{"MYCLI_REGION", "MYCLI_TARGET", "MYCLI_TOKEN"}, names(reads))
	assert.Equal(t, EnvRead{Name: "MYCLI_TOKEN", Path: "lib/util.js", Line: 1, Count: 1}, reads[2])
	assert.Len(t, tool.Probes(), 2)
	assert.Equal(t, []string{"mycli", "deploy", "--dry-run"}, tool.Probes()[0].Argv)

	// Word mode (no env_pattern) also counts the comment mention; an explicit
	// install widens the root and env_scan is matched below it.
	words := load(t, "mycli", Config{Install: "node_modules/mycli", EnvScan: "node_modules/mycli/dist/**/*.js", DeployVars: list("MYCLI_*")})
	assert.Equal(t, "node_modules/mycli", words.InstallRoot())
	reads, err = words.EnvReads("testdata/mycli/install")
	require.NoError(t, err)
	assert.Equal(t, []string{"MYCLI_MENTIONED_ONLY", "MYCLI_REGION", "MYCLI_TARGET", "MYCLI_TOKEN"}, names(reads))
	assert.Equal(t, "dist/cli.js", reads[2].Path)

	assert.False(t, words.Detect("testdata/mycli"), "no node_modules/mycli under the fixture repo")
	assert.True(t, load(t, "mycli", Config{EnvScan: "install/dist/*.js", DeployVars: list("MYCLI_*")}).Detect("testdata/mycli"))
}

// TestAdapterSymlinkedInstallRoot: pnpm installs node_modules/<pkg> as a
// symlink into its store; the root is followed.
func TestAdapterSymlinkedInstallRoot(t *testing.T) {
	abs, err := filepath.Abs("testdata/convex/install")
	require.NoError(t, err)
	link := filepath.Join(t.TempDir(), "convex")
	require.NoError(t, os.Symlink(abs, link))
	reads, err := load(t, "convex", Config{}).EnvReads(link)
	require.NoError(t, err)
	assert.Len(t, reads, 3)
}

// TestAdapterEnvReadsFailClosed: an install that is missing, not a
// directory, or has no file to scan is an error, never an empty result.
func TestAdapterEnvReadsFailClosed(t *testing.T) {
	tool := load(t, "convex", Config{})
	empty := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(empty, "README.md"), []byte("CONVEX_X\n"), 0o644))
	for name, root := range map[string]string{
		"missing":      filepath.Join(t.TempDir(), "nope"),
		"a file":       "testdata/convex/install/package.json",
		"no js inside": empty,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := tool.EnvReads(root)
			assert.Error(t, err)
		})
	}
}

// TestAdapterLoadRejects: every malformed tools: entry is an error, so a
// typo cannot turn the check off.
func TestAdapterLoadRejects(t *testing.T) {
	g := "scripts/guard.mjs"
	generic := func(c Config) Config {
		c.Guard = g
		if c.EnvScan == "" {
			c.EnvScan = "node_modules/mycli/dist/**/*.js"
		}
		if c.DeployVars == nil {
			c.DeployVars = list("MYCLI_*")
		}
		return c
	}
	cases := map[string]struct {
		name string
		c    Config
	}{
		"bad name":                     {"My CLI", Config{Guard: g}},
		"no guard":                     {"convex", Config{}},
		"absolute guard":               {"convex", Config{Guard: "/etc/guard.mjs"}},
		"escaping guard":               {"convex", Config{Guard: "../guard.mjs"}},
		"unclean guard":                {"convex", Config{Guard: "scripts//guard.mjs"}},
		"escaping install":             {"convex", Config{Guard: g, Install: "node_modules/../../x"}},
		"builtin with env_scan":        {"convex", Config{Guard: g, EnvScan: "node_modules/convex/**/*.js"}},
		"builtin with command":         {"vercel", Config{Guard: g, Command: "vc"}},
		"builtin empty deploy_vars":    {"convex", Config{Guard: g, DeployVars: list()}},
		"unknown tool without scan":    {"mycli", Config{Guard: g, DeployVars: list("MYCLI_*")}},
		"generic without deploy_vars":  {"mycli", Config{Guard: g, EnvScan: "node_modules/mycli/*.js"}},
		"generic bad deploy var":       {"mycli", generic(Config{DeployVars: list("MYCLI-*")})},
		"generic pattern no group":     {"mycli", generic(Config{EnvPattern: `process\.env\.[A-Z_]+`})},
		"generic bad pattern":          {"mycli", generic(Config{EnvPattern: `(`})},
		"generic bad glob":             {"mycli", generic(Config{EnvScan: "node_modules/mycli/[x/*.js"})},
		"generic partial **":           {"mycli", generic(Config{EnvScan: "node_modules/mycli/a**/*.js"})},
		"generic scan at root":         {"mycli", generic(Config{EnvScan: "**/*.js"})},
		"generic scan outside install": {"mycli", generic(Config{Install: "node_modules/other"})},
		"generic bad command":          {"mycli", generic(Config{Command: "my cli"})},
		"probe with a shell operator":  {"mycli", generic(Config{Probes: list("mycli --help; curl x")})},
		"probe with a variable":        {"mycli", generic(Config{Probes: list("mycli $TARGET")})},
		"probe with quotes":            {"mycli", generic(Config{Probes: list(`mycli "deploy"`)})},
		"probe of another command":     {"mycli", generic(Config{Probes: list("npx mycli --help")})},
		"empty probe":                  {"mycli", generic(Config{Probes: list(" ")})},
		"builtin probe of another cmd": {"convex", Config{Guard: g, Probes: list("vercel --help")}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Load(c.name, c.c)
			assert.Error(t, err)
		})
	}
}

// TestAdapterMentionsWholeWords: a guard classifies a name only as a whole
// word, so a longer name does not classify its prefix.
func TestAdapterMentionsWholeWords(t *testing.T) {
	m := Mentions([]byte("const known = ['CONVEX_DEPLOY_KEY', \"CONVEX_URL\"];\n// $CONVEX_SITE\n"))
	assert.True(t, m["CONVEX_DEPLOY_KEY"])
	assert.True(t, m["CONVEX_URL"])
	assert.True(t, m["$CONVEX_SITE"], "$ is part of a JS identifier")
	assert.False(t, m["CONVEX_DEPLOY"])
	assert.False(t, m["CONVEX_SITE"])
}

// TestAdapterBuiltinsAreValid: every built-in loads with its defaults.
func TestAdapterBuiltinsAreValid(t *testing.T) {
	assert.Equal(t, []string{"convex", "inngest", "supabase", "vercel"}, BuiltinNames())
	for _, a := range Builtins() {
		assert.NotEmpty(t, a.Probes(), a.Name())
		assert.True(t, strings.HasPrefix(a.InstallRoot(), "node_modules/"), a.Name())
		// Every default probe only asks for a version or help text: nothing
		// that deploys, pushes a migration or sends an event.
		for _, p := range a.Probes() {
			last := p.Argv[len(p.Argv)-1]
			assert.True(t, last == "--help" || last == "--version", "%s probe %q is not --help/--version", a.Name(), p.Text)
		}
	}
	// Each built-in loads from a tools: entry, install override included.
	for _, n := range BuiltinNames() {
		_, err := Load(n, Config{Guard: "scripts/guard.mjs", Install: "apps/web/node_modules/" + n})
		assert.NoError(t, err, n)
	}
}

// TestAdaptersImportGuard keeps the package where CLAUDE.md puts it: its
// production files import the standard library (and at most gitexec), and
// nothing but internal/referee imports it, so the daemon never does.
func TestAdaptersImportGuard(t *testing.T) {
	const self = "github.com/vnovick/itervox/internal/referee/adapters"
	allowed := map[string]bool{"github.com/vnovick/itervox/internal/gitexec": true}
	referee := filepath.Clean("../../../internal/referee")
	selfDir := filepath.Join(referee, "adapters")
	for _, root := range []string{"../../../cmd", "../../../internal"} {
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "node_modules" || d.Name() == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(p, ".go") {
				return nil
			}
			f, err := parser.ParseFile(token.NewFileSet(), p, nil, parser.ImportsOnly)
			require.NoError(t, err)
			dir := filepath.Dir(p)
			for _, imp := range f.Imports {
				ip, _ := strconv.Unquote(imp.Path.Value)
				if dir == selfDir && !strings.HasSuffix(p, "_test.go") {
					first, _, _ := strings.Cut(ip, "/")
					assert.True(t, !strings.Contains(first, ".") || allowed[ip], "%s imports %s: adapters may import only the standard library and gitexec", p, ip)
				}
				if ip == self {
					assert.Equal(t, referee, dir, "%s imports the referee adapters; only internal/referee may", p)
				}
			}
			return nil
		})
		require.NoError(t, err)
	}
}
