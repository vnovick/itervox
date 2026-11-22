package guardrails

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// auditBoth runs AuditRange over base..head and over the patch alone
// (AuditPatch, the hunk heuristic) and returns each one's
// test_path_ignored locations.
func auditBoth(t *testing.T, r *rig, base, head string) (exact, heuristic []string) {
	t.Helper()
	res, err := AuditRange(context.Background(), r.work, base, head)
	require.NoError(t, err)
	patch := git(t, r.work, "diff", fmt.Sprintf("-U%d", AuditContext), "--no-renames", "--no-color", "--src-prefix=a/", "--dst-prefix=b/", base, head, "--")
	found, err := AuditPatch([]byte(patch))
	require.NoError(t, err)
	pick := func(fs []AuditFinding) []string {
		out := []string{}
		for _, f := range fs {
			if f.Rule == "test_path_ignored" {
				out = append(out, fmt.Sprintf("%s:%d", f.Path, f.Line))
			}
		}
		return out
	}
	return pick(res.Findings), pick(found)
}

// commitPair commits path at base then at head and returns both commits.
func commitPair(t *testing.T, r *rig, path, base, head string) (string, string) {
	t.Helper()
	r.writeCommit(path, base, "base")
	b := strings.TrimSpace(git(t, r.work, "rev-parse", "HEAD"))
	r.writeCommit(path, head, "head")
	return b, strings.TrimSpace(git(t, r.work, "rev-parse", "HEAD"))
}

// A test file pattern appended to a testPathIgnorePatterns array opened
// more than AuditContext lines above the change: the hunk heuristic never
// sees the opener, the whole-file read does.
func TestTestConfigArrayOpenedFarAbove(t *testing.T) {
	r := newRig(t, nil)
	var entries strings.Builder
	for i := range 30 {
		fmt.Fprintf(&entries, "    '/fixtures/case-%02d/',\n", i)
	}
	cfg := "module.exports = {\n  testPathIgnorePatterns: [\n" + entries.String() + "%s  ],\n};\n"
	base, head := commitPair(t, r, "jest.config.js", fmt.Sprintf(cfg, ""), fmt.Sprintf(cfg, "    'src/checkout\\\\.test\\\\.ts',\n"))
	exact, heuristic := auditBoth(t, r, base, head)
	assert.Equal(t, []string{"jest.config.js:33"}, exact)
	assert.Empty(t, heuristic, "the heuristic cannot see an opener 30 lines up (why the whole file is read)")
}

// A template literal that spans lines inside the exclude array, holding a
// "]" of its own: the line heuristic closes the array there; the
// tokenizer reads the template whole.
func TestTestConfigMultiLineTemplateLiteral(t *testing.T) {
	r := newRig(t, nil)
	cfg := "import { defineConfig } from 'vitest/config';\n\nexport default defineConfig({\n  test: {\n    exclude: [\n      `generated/\n]`,\n%s    ],\n  },\n});\n"
	base, head := commitPair(t, r, "vitest.config.ts", fmt.Sprintf(cfg, ""), fmt.Sprintf(cfg, "      `src/flaky.test.ts`,\n"))
	exact, heuristic := auditBoth(t, r, base, head)
	assert.Equal(t, []string{"vitest.config.ts:8"}, exact)
	assert.Empty(t, heuristic, "the heuristic loses the array at the template's ]")
}

// coverage.exclude and an exclude outside the test block (optimizeDeps)
// are not test excludes; test.exclude is. The heuristic counts every
// exclude outside coverage.
func TestTestConfigCoverageAndOtherExcludesAreNotTestExcludes(t *testing.T) {
	r := newRig(t, nil)
	cfg := "export default defineConfig({\n  optimizeDeps: { exclude: [%s] },\n  test: {\n    coverage: {\n      exclude: [\n%s      ],\n    },\n    exclude: [\n%s    ],\n  },\n});\n"
	base, head := commitPair(t, r, "vite.config.ts",
		fmt.Sprintf(cfg, "", "", ""),
		fmt.Sprintf(cfg, "'src/dep.test.ts'", "        'src/main.test.tsx',\n", "        'src/flaky.test.ts',\n"))
	exact, heuristic := auditBoth(t, r, base, head)
	assert.Equal(t, []string{"vite.config.ts:10"}, exact, "only test.exclude")
	assert.Contains(t, heuristic, "vite.config.ts:2", "the heuristic reads optimizeDeps.exclude as a test exclude")
}

// defineConfig wrappers: a function config (`({ mode }) => ({ … })`), a
// workspace project list, and a `satisfies` object.
func TestTestConfigDefineConfigWrappers(t *testing.T) {
	cases := map[string]string{
		"vitest.config.ts":    "import { defineConfig } from 'vitest/config';\nexport default defineConfig(({ mode }) => ({\n  plugins: [react()],\n  test: { environment: mode === 'ci' ? 'node' : 'jsdom', exclude: [%s] },\n}));\n",
		"vitest.workspace.ts": "export default defineWorkspace([\n  'packages/*',\n  { test: { name: 'unit', exclude: [%s] } },\n]);\n",
		"vite.config.mts":     "const config = {\n  test: {\n    exclude: [/* none */ %s],\n  },\n} satisfies UserConfig;\nexport default config;\n",
	}
	for path, cfg := range cases {
		t.Run(path, func(t *testing.T) {
			r := newRig(t, nil)
			base, head := commitPair(t, r, path, fmt.Sprintf(cfg, "'node_modules'"), fmt.Sprintf(cfg, "'node_modules', \"src/flaky.test.ts\""))
			exact, _ := auditBoth(t, r, base, head)
			require.Len(t, exact, 1, "%v", exact)
			assert.True(t, strings.HasPrefix(exact[0], path+":"), exact[0])
		})
	}
}

// package.json: only the "jest" key is Jest's config.
func TestTestConfigPackageJSONJestKey(t *testing.T) {
	r := newRig(t, nil)
	pkg := "{\n  \"name\": \"app\",\n  \"other\": { \"testPathIgnorePatterns\": [%s] },\n  \"jest\": {\n    \"testPathIgnorePatterns\": [\n      \"/node_modules/\"%s\n    ]\n  }\n}\n"
	base, head := commitPair(t, r, "package.json", fmt.Sprintf(pkg, "", ""),
		fmt.Sprintf(pkg, `"a\\.test\\.ts"`, ",\n      \"<rootDir>/src/__tests__/slow\""))
	exact, heuristic := auditBoth(t, r, base, head)
	assert.Equal(t, []string{"package.json:7"}, exact)
	assert.Contains(t, heuristic, "package.json:3", "the heuristic reads any testPathIgnorePatterns key")
}

// Removing an entry, or moving one within the array, is not a finding.
func TestTestConfigRemovalAndMoveAreNotFindings(t *testing.T) {
	r := newRig(t, nil)
	base, head := commitPair(t, r, "jest.config.ts",
		"export default {\n  testPathIgnorePatterns: [\n    'src/a\\\\.test\\\\.ts',\n    '/e2e/',\n    'src/b\\\\.test\\\\.ts',\n  ],\n};\n",
		"export default {\n  testPathIgnorePatterns: [\n    '/e2e/',\n    'src/b\\\\.test\\\\.ts',\n  ],\n};\n")
	exact, _ := auditBoth(t, r, base, head)
	assert.Empty(t, exact, "a removed entry")

	base, head = commitPair(t, r, "jest.config.mts",
		"export default {\n  testPathIgnorePatterns: [\n    '/e2e/',\n    'src/b\\\\.test\\\\.ts',\n  ],\n};\n",
		"export default {\n  testPathIgnorePatterns: [\n    'src/b\\\\.test\\\\.ts',\n    '/e2e/',\n  ],\n};\n")
	exact, _ = auditBoth(t, r, base, head)
	assert.Empty(t, exact, "a moved entry")
}

// testMatch narrowed (an entry dropped while testMatch stays) is a
// finding at the head's first testMatch entry; dropping testMatch
// altogether (Jest's default) is not.
func TestTestConfigTestMatchNarrowed(t *testing.T) {
	r := newRig(t, nil)
	base, head := commitPair(t, r, "jest.config.js",
		"module.exports = {\n  testMatch: ['**/*.test.ts', '**/*.spec.ts'],\n};\n",
		"module.exports = {\n  testMatch: ['**/*.test.ts'],\n};\n")
	exact, _ := auditBoth(t, r, base, head)
	assert.Equal(t, []string{"jest.config.js:2"}, exact)

	base, head = commitPair(t, r, "jest.config.cjs",
		"module.exports = {\n  testMatch: ['**/*.test.ts'],\n};\n",
		"module.exports = {\n  verbose: true,\n};\n")
	exact, _ = auditBoth(t, r, base, head)
	assert.Empty(t, exact)
}

// Fallback: a config the tokenizer cannot read (an unterminated string at
// the head) keeps the hunk heuristic, and so does a patch audited without
// its repository; an untracked new config is read whole.
func TestTestConfigFallsBackToTheHunkHeuristic(t *testing.T) {
	r := newRig(t, nil)
	cfg := "module.exports = {\n  testPathIgnorePatterns: [\n    '/e2e/',\n%s  ],\n};\n%s"
	base, head := commitPair(t, r, "jest.config.js", fmt.Sprintf(cfg, "", ""),
		fmt.Sprintf(cfg, "    'src/flaky\\\\.test\\\\.ts',\n", "const broken = 'unterminated\n"))
	exact, heuristic := auditBoth(t, r, base, head)
	assert.Equal(t, []string{"jest.config.js:4"}, exact, "the unreadable config falls back to the heuristic")
	assert.Equal(t, heuristic, exact)
	_, ok := testConfigFindings("jest.config.js", nil, []byte(fmt.Sprintf(cfg, "", "const broken = 'unterminated\n")))
	assert.False(t, ok)

	// An untracked config in the working tree: read whole, the array
	// opener far above the entry.
	var entries strings.Builder
	for i := range 30 {
		fmt.Fprintf(&entries, "      'fixtures/%02d',\n", i)
	}
	body := "export default defineConfig({\n  test: {\n    exclude: [\n" + entries.String() + "      'src/flaky.test.ts',\n    ],\n  },\n});\n"
	require.NoError(t, os.WriteFile(filepath.Join(r.work, "vitest.config.ts"), []byte(body), 0o644))
	res, err := AuditRange(context.Background(), r.work, head, "")
	require.NoError(t, err)
	var got []string
	for _, f := range res.Findings {
		got = append(got, fmt.Sprintf("%s:%d %s", f.Path, f.Line, f.Rule))
	}
	assert.Equal(t, []string{"vitest.config.ts:34 test_path_ignored"}, got)
}

// The tokenizer: comments, regex literals, nested template substitutions
// and escapes do not break bracket tracking; keys are read from object
// members only.
func TestJSConfigArraysTokenizer(t *testing.T) {
	src := "// testPathIgnorePatterns: ['x.test.ts']\n" +
		"/* exclude: [ */\n" +
		"const re = /[\\]'\"]/g;\n" +
		"export default {\n" +
		"  test: {\n" +
		"    exclude: [\n" +
		"      `a/${name ? `b${'}'}` : 'c'}/d.test.ts`,\n" +
		"      'it\\'s.test.ts', // trailing\n" +
		"      ...(ci ? ['ci.test.ts'] : []),\n" +
		"    ],\n" +
		"    'quoted key': ['q'],\n" +
		"  },\n" +
		"};\n"
	got, err := jsConfigArrays(src)
	require.NoError(t, err)
	var flat []string
	for _, s := range got {
		flat = append(flat, fmt.Sprintf("%d %s %s", s.line, strings.Join(s.keys, "."), s.value))
	}
	assert.Equal(t, []string{
		"7 .test.exclude a/${name ? `b${'}'}` : 'c'}/d.test.ts",
		"8 .test.exclude it's.test.ts",
		"9 .test.exclude ci.test.ts",
		"11 .test.quoted key q",
	}, flat)

	for _, bad := range []string{"x = 'open\n", "x = `open", "/* open", "x = [1, 2", "x = {]}"} {
		_, err := jsConfigArrays(bad)
		assert.Error(t, err, "%q", bad)
	}
}
