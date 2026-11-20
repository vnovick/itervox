package referee

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type webCase struct {
	rule, path, line string
	want             bool
}

// webLineCases: for each TypeScript, lint and JS test runner rule, the
// added lines it flags, the same text in a file kind where it means
// something else, and the comment or prose mentions it must not flag.
var webLineCases = []webCase{
	// ts_ignore
	{"ts_ignore", "web/src/a.ts", `// @ts-ignore`, true},
	{"ts_ignore", "web/src/App.tsx", `    {/* @ts-ignore */}`, true},
	{"ts_ignore", "src/a.mts", `const x: number = y; /// @ts-ignore`, true},
	{"ts_ignore", "web/src/a.test.ts", `  // @ts-ignore -- bad typings`, true},
	{"ts_ignore", "docs/typescript.md", `// @ts-ignore`, false},
	{"ts_ignore", "web/src/a.ts", `// we dropped the @ts-ignore here`, false},
	{"ts_ignore", "web/src/a.ts", ` * @ts-ignore is banned`, false},
	// ts_expect_error
	{"ts_expect_error", "web/src/a.ts", `  // @ts-expect-error library types are wrong`, true},
	{"ts_expect_error", "web/src/a.jsx", `{/* @ts-expect-error */}`, true},
	{"ts_expect_error", "web/src/a.test.ts", `  // @ts-expect-error calling with a wrong type on purpose`, false},
	{"ts_expect_error", "web/src/__tests__/a.ts", `  // @ts-expect-error on purpose`, false},
	{"ts_expect_error", "docs/a.md", `// @ts-expect-error`, false},
	{"ts_expect_error", "web/src/a.ts", `// prefer @ts-expect-error over ignore`, false},
	// ts_nocheck
	{"ts_nocheck", "web/src/a.ts", `// @ts-nocheck`, true},
	{"ts_nocheck", "web/src/a.js", ` * @ts-nocheck`, true},
	{"ts_nocheck", "README.md", `// @ts-nocheck`, false},
	{"ts_nocheck", "web/src/a.ts", `// never add @ts-nocheck`, false},
	// ts_strict_off
	{"ts_strict_off", "tsconfig.json", `    "strict": false,`, true},
	{"ts_strict_off", "web/tsconfig.app.json", `    "noImplicitAny": false`, true},
	{"ts_strict_off", "tsconfig.base.json", `"strictNullChecks": false,`, true},
	{"ts_strict_off", "tsconfig.json", `    "noEmitOnError": false,`, true},
	{"ts_strict_off", "jsconfig.json", `    "checkJs": false,`, true},
	{"ts_strict_off", "tsconfig.json", `    "noCheck": true,`, true},
	{"ts_strict_off", "tsconfig.json", `    "strict": true,`, false},
	{"ts_strict_off", "tsconfig.json", `    "skipLibCheck": true,`, false},
	{"ts_strict_off", "tsconfig.json", `    // "strict": false,`, false},
	{"ts_strict_off", "package.json", `    "strict": false,`, false},
	{"ts_strict_off", "src/settings.json", `"strictNullChecks": false`, false},
	// eslint_disable
	{"eslint_disable", "web/src/a.ts", `  // eslint-disable-next-line @typescript-eslint/no-explicit-any`, true},
	{"eslint_disable", "web/src/a.tsx", `  useEffect(load, []); // eslint-disable-line react-hooks/exhaustive-deps`, true},
	{"eslint_disable", "web/src/a.jsx", `      {/* eslint-disable-next-line react/no-danger */}`, true},
	{"eslint_disable", "docs/lint.md", `// eslint-disable-next-line`, false},
	{"eslint_disable", "web/src/a.ts", `// do not add eslint-disable-next-line here`, false},
	{"eslint_disable", "web/src/a.ts", `/* eslint-disable */`, false},
	// eslint_disable_file
	{"eslint_disable_file", "web/src/a.ts", `/* eslint-disable */`, true},
	{"eslint_disable_file", "web/src/a.js", `/* eslint-disable react-hooks/rules-of-hooks */`, true},
	{"eslint_disable_file", "web/src/a.ts", `/* eslint-enable */`, false},
	{"eslint_disable_file", "web/src/a.ts", `// eslint-disable`, false},
	{"eslint_disable_file", "web/src/a.ts", `// no /* eslint-disable */ blocks, see CONTRIBUTING`, false},
	{"eslint_disable_file", "web/src/a.css", `/* eslint-disable */`, false},
	{"eslint_disable_file", "web/src/a.ts", ` * eslint-disable is banned`, false},
	// eslint_rule_off
	{"eslint_rule_off", "web/eslint.config.js", `      'react-hooks/rules-of-hooks': 'off',`, true},
	{"eslint_rule_off", ".eslintrc.json", `    "react-hooks/exhaustive-deps": 0,`, true},
	{"eslint_rule_off", ".eslintrc.yml", `  tailwindcss/no-custom-classname: off`, true},
	{"eslint_rule_off", ".eslintrc.cjs", `    'tailwindcss/classnames-order': ['off'],`, true},
	{"eslint_rule_off", "eslint.config.mjs", `    rules: { eqeqeq: 'off' },`, true},
	{"eslint_rule_off", ".eslintrc.json", `    "@typescript-eslint/no-explicit-any": [0],`, true},
	{"eslint_rule_off", ".eslintrc.json", `    "react-hooks/exhaustive-deps": "error",`, false},
	{"eslint_rule_off", ".eslintrc.json", `    "no-multiple-empty-lines": ["error", { "max": 0 }],`, false},
	{"eslint_rule_off", "eslint.config.js", `    // 'react-hooks/rules-of-hooks': 'off',`, false},
	{"eslint_rule_off", ".eslintrc.yml", `  # tailwindcss/no-custom-classname: off`, false},
	{"eslint_rule_off", "src/flags.ts", `  'react-hooks/rules-of-hooks': 'off',`, false},
	{"eslint_rule_off", "eslint.config.js", `      offset: 3,`, false},
	// flow_suppress
	{"flow_suppress", "app/Screen.js", `  // $FlowFixMe[incompatible-type]`, true},
	{"flow_suppress", "app/Screen.js", `      {/* $FlowIgnore */}`, true},
	{"flow_suppress", "app/Screen.js", ` * @noflow`, true},
	{"flow_suppress", "app/Screen.js", `// @noflow`, true},
	{"flow_suppress", "docs/flow.md", `// $FlowFixMe`, false},
	{"flow_suppress", "app/Screen.js", `// fixed the old $FlowFixMe`, false},
	{"flow_suppress", "app/Screen.js", `// @flow`, false},
	// check_or_true
	{"check_or_true", "package.json", `    "lint": "eslint . || true",`, true},
	{"check_or_true", "package.json", `    "typecheck": "tsc --noEmit || exit 0",`, true},
	{"check_or_true", "Makefile", "\tpnpm run lint || true", true},
	{"check_or_true", "scripts/ci.sh", `npx jest --ci || :`, true},
	{"check_or_true", ".husky/pre-commit", `npx prettier --check . || true`, true},
	{"check_or_true", "scripts/ci.sh", `rm -rf dist || true`, false},
	{"check_or_true", "scripts/ci.sh", `npx prettier --write . || true`, false},
	{"check_or_true", "scripts/ci.sh", `npm run build:latest || true`, false},
	{"check_or_true", "scripts/ci.sh", `eslint . || exit 1`, false},
	{"check_or_true", "scripts/ci.sh", `# eslint . || true`, false},
	{"check_or_true", ".github/workflows/ci.yml", `      - run: eslint . || true`, false}, // ci_or_true's
	{"check_or_true", "src/run.ts", `exec("eslint . || true")`, false},
	// script_stubbed
	{"script_stubbed", "package.json", `    "test": "true",`, true},
	{"script_stubbed", "web/package.json", `    "typecheck": "exit 0",`, true},
	{"script_stubbed", "package.json", `    "lint": "echo \"lint skipped\"",`, true},
	{"script_stubbed", "package.json", `    "test": "echo \"Error: no test specified\" && exit 1"`, false},
	{"script_stubbed", "package.json", `    "test": "vitest run",`, false},
	{"script_stubbed", "package.json", `    "build": "true",`, false},
	{"script_stubbed", "composer.json", `    "test": "true",`, false},
	// test_pass_with_no_tests
	{"test_pass_with_no_tests", "package.json", `    "test": "jest --passWithNoTests",`, true},
	{"test_pass_with_no_tests", ".github/workflows/ci.yml", `      - run: npx vitest run --passWithNoTests`, true},
	{"test_pass_with_no_tests", "jest.config.ts", `  passWithNoTests: true,`, true},
	{"test_pass_with_no_tests", "web/vitest.config.ts", `    passWithNoTests: true,`, true},
	{"test_pass_with_no_tests", "jest.config.js", `  passWithNoTests: false,`, false},
	{"test_pass_with_no_tests", "package.json", `    "test": "jest --passWithNoTests=false",`, false},
	{"test_pass_with_no_tests", "jest.config.js", `  // passWithNoTests: true,`, false},
	{"test_pass_with_no_tests", "src/a.test.ts", `  passWithNoTests: true,`, false},
	// snapshot_update
	{"snapshot_update", ".github/workflows/ci.yml", `      - run: npx jest --ci -u`, true},
	{"snapshot_update", ".github/workflows/ci.yml", `        run: pnpm test -- --updateSnapshot`, true},
	{"snapshot_update", ".github/workflows/e2e.yml", `      - run: npx playwright test --update-snapshots`, true},
	{"snapshot_update", ".github/workflows/ci.yml", `      - run: npx vitest run --update`, true},
	{"snapshot_update", "package.json", `    "test": "jest -u",`, true},
	{"snapshot_update", "package.json", `    "test:ci": "vitest run --update",`, true},
	{"snapshot_update", "package.json", `    "test:update": "jest -u",`, false},
	{"snapshot_update", ".github/workflows/ci.yml", `      - run: npx jest --ci`, false},
	{"snapshot_update", ".github/workflows/ci.yml", `      - run: sudo apt-get update -u && npx jest`, false},
	{"snapshot_update", ".github/workflows/ci.yml", `      # - run: npx jest -u`, false},
	{"snapshot_update", "scripts/update.sh", `npx jest -u`, false},
	// test_path_ignored
	{"test_path_ignored", "jest.config.js", `  testPathIgnorePatterns: ['/node_modules/', 'src/flaky\\.test\\.ts'],`, true},
	{"test_path_ignored", "package.json", `    "testPathIgnorePatterns": ["<rootDir>/src/__tests__/slow"],`, true},
	{"test_path_ignored", "package.json", `    "test": "jest --testPathIgnorePatterns=checkout.spec.ts",`, true},
	{"test_path_ignored", "web/vitest.config.ts", `    exclude: [...configDefaults.exclude, 'src/flaky.test.ts'],`, true},
	{"test_path_ignored", ".github/workflows/ci.yml", `      - run: npx vitest run --exclude src/flaky.test.ts`, true},
	{"test_path_ignored", "jest.config.js", `  testPathIgnorePatterns: ['/node_modules/', '/e2e/'],`, false},
	{"test_path_ignored", "web/vitest.config.ts", `      exclude: ['**/*.test.ts', 'src/main.tsx'],`, false},
	{"test_path_ignored", "jest.config.js", `  // testPathIgnorePatterns: ['a\\.test\\.ts'],`, false},
	{"test_path_ignored", "tsconfig.json", `  "exclude": ["src/flaky.test.ts"],`, false},
	{"test_path_ignored", "src/config.ts", `  testPathIgnorePatterns: ['a.test.ts'],`, false},
}

// TestAuditWebLineRules: each case is an added line.
func TestAuditWebLineRules(t *testing.T) {
	for _, c := range webLineCases {
		t.Run(c.rule+"/"+c.path+"/"+c.line, func(t *testing.T) {
			found, err := auditPatch(onePatch(c.path, nil, []string{c.line}))
			require.NoError(t, err)
			var rules []string
			for _, f := range found {
				rules = append(rules, f.Rule)
			}
			if c.want {
				assert.Contains(t, rules, c.rule, "%s in %s", c.line, c.path)
			} else {
				assert.NotContains(t, rules, c.rule, "%s in %s", c.line, c.path)
			}
		})
	}
}

// TestAuditWebRulesIgnoreRemovedLines: every line a web rule flags when
// added is not a finding when it is removed (the change takes it away).
func TestAuditWebRulesIgnoreRemovedLines(t *testing.T) {
	n := 0
	for _, c := range webLineCases {
		if !c.want {
			continue
		}
		n++
		found, err := auditPatch(onePatch(c.path, []string{c.line}, nil))
		require.NoError(t, err)
		assert.Empty(t, found, "removed %s in %s", c.line, c.path)
	}
	assert.Greater(t, n, 40)
}

// TestAuditWebRulesAllHaveCases: every web rule ID has a positive and a
// negative case.
func TestAuditWebRulesAllHaveCases(t *testing.T) {
	pos, neg := map[string]bool{}, map[string]bool{}
	for _, c := range webLineCases {
		if c.want {
			pos[c.rule] = true
		} else {
			neg[c.rule] = true
		}
	}
	for _, r := range webLineRules {
		assert.True(t, pos[r.ID], "%s has no positive case", r.ID)
		assert.True(t, neg[r.ID], "%s has no negative case", r.ID)
	}
}

// TestAuditMaxWarningsRaised: --max-warnings raised, made unlimited or
// dropped from the eslint command in the same hunk of a script.
func TestAuditMaxWarningsRaised(t *testing.T) {
	cases := []struct {
		name, path     string
		removed, added []string
		want           string
	}{
		{"raised", "package.json", []string{`    "lint": "eslint . --max-warnings 0",`}, []string{`    "lint": "eslint . --max-warnings 25",`}, "raised from 0 to 25"},
		{"equals form", "web/package.json", []string{`"lint": "eslint --max-warnings=0 src"`}, []string{`"lint": "eslint --max-warnings=3 src"`}, "raised from 0 to 3"},
		{"unlimited", ".github/workflows/ci.yml", []string{`      - run: npx eslint . --max-warnings 10`}, []string{`      - run: npx eslint . --max-warnings -1`}, "raised from 10 to unlimited (-1)"},
		{"dropped", "Makefile", []string{"\teslint --max-warnings 0 web/src"}, []string{"\teslint web/src"}, "--max-warnings 0 removed"},
		{"lowered", "package.json", []string{`"lint": "eslint . --max-warnings 10"`}, []string{`"lint": "eslint . --max-warnings 0"`}, ""},
		{"from unlimited", "package.json", []string{`"lint": "eslint . --max-warnings -1"`}, []string{`"lint": "eslint . --max-warnings 0"`}, ""},
		{"unchanged", "package.json", []string{`"lint": "eslint . --max-warnings 0"`}, []string{`"lint": "eslint . --max-warnings 0 --cache"`}, ""},
		{"only added", "package.json", nil, []string{`"lint": "eslint . --max-warnings 50"`}, ""},
		{"only removed", "package.json", []string{`"lint": "eslint . --max-warnings 0"`}, nil, ""},
		{"not a script", "docs/lint.md", []string{"eslint --max-warnings 0"}, []string{"eslint --max-warnings 99"}, ""},
		{"comment", "scripts/lint.sh", []string{"eslint --max-warnings 0 ."}, []string{"# eslint --max-warnings 99 .", "eslint --max-warnings 0 ."}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			found, err := auditPatch(onePatch(c.path, c.removed, c.added))
			require.NoError(t, err)
			var hits []finding
			for _, f := range found {
				if f.Rule == "eslint_max_warnings" {
					hits = append(hits, f)
				}
			}
			if c.want == "" {
				assert.Empty(t, hits)
				return
			}
			require.Len(t, hits, 1, "%+v", found)
			assert.Contains(t, hits[0].Message, c.want)
			assert.Equal(t, 1, hits[0].Line)
		})
	}
}

// TestAuditCoverageLoweredJSRunners: the coverage_lowered rule reads Jest's
// coverageThreshold (jest.config.* and the package.json "jest" key) and
// Vitest's coverage.thresholds, including several keys on one line and
// Jest's negative (uncovered-count) thresholds.
func TestAuditCoverageLoweredJSRunners(t *testing.T) {
	cases := []struct {
		name, path     string
		removed, added []string
		want           string
	}{
		{"package.json jest", "package.json", []string{`      "global": { "branches": 90, "lines": 95 }`}, []string{`      "global": { "branches": 90, "lines": 70 }`}, "lines lowered from 95 to 70"},
		{"jest.config.ts", "jest.config.ts", []string{`    global: { statements: 85 },`}, []string{`    global: { statements: 50 },`}, "statements lowered from 85 to 50"},
		{"vitest thresholds", "web/vitest.config.mts", []string{`      thresholds: { lines: 80, functions: 80 },`}, []string{`      thresholds: { lines: 80, functions: 40 },`}, "functions lowered from 80 to 40"},
		{"vite config", "vite.config.ts", []string{`        branches: 75,`}, []string{`        branches: 0,`}, "branches lowered from 75 to 0"},
		{"jest uncovered count", "jest.config.js", []string{`    statements: -10,`}, []string{`    statements: -40,`}, "statements lowered from -10 to -40"},
		{"jest uncovered tightened", "jest.config.js", []string{`    statements: -40,`}, []string{`    statements: -10,`}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			found, err := auditPatch(onePatch(c.path, c.removed, c.added))
			require.NoError(t, err)
			var msgs []string
			for _, f := range found {
				if f.Rule == "coverage_lowered" {
					msgs = append(msgs, f.Message)
				}
			}
			if c.want == "" {
				assert.Empty(t, msgs)
				return
			}
			require.Len(t, msgs, 1, "%+v", found)
			assert.Contains(t, msgs[0], c.want)
		})
	}
}

// TestAuditDeletedDetoxTest: a Detox e2e spec (login.e2e.ts) is a test
// file, so deleting it is a finding.
func TestAuditDeletedDetoxTest(t *testing.T) {
	assert.Len(t, deletedTests([]byte("D\x00e2e/login.e2e.ts\x00")), 1)
	assert.Len(t, deletedTests([]byte("D\x00e2e/login.e2e.js\x00")), 1)
	assert.Empty(t, deletedTests([]byte("D\x00e2e/helpers.ts\x00")))
}

// TestAuditWebRulesOnRepo: on a real repository, a TypeScript change that
// adds @ts-ignore, turns off strict and an ESLint hooks rule exits 20 with
// those findings; the same files changed harmlessly exit 0.
func TestAuditWebRulesOnRepo(t *testing.T) {
	r := newTestRepo(t)
	r.commit("base", map[string]string{
		"tsconfig.json":    "{\n  \"compilerOptions\": {\n    \"strict\": true\n  }\n}\n",
		"eslint.config.js": "export default [{\n  rules: {\n    'react-hooks/rules-of-hooks': 'error',\n  },\n}];\n",
		"src/App.tsx":      "export const App = () => null;\n",
	})
	r.git("checkout", "-q", "-b", "clean")
	r.commit("clean", map[string]string{"src/App.tsx": "// The app.\nexport const App = () => null;\n"})
	code, out, errOut := referee(t, nil, "audit", "--dir", r.dir, "--base", "main")
	assert.Equal(t, 0, code, out+errOut)

	r.git("checkout", "-q", "-b", "weaken", "main")
	r.commit("weaken", map[string]string{
		"tsconfig.json":    "{\n  \"compilerOptions\": {\n    \"strict\": false\n  }\n}\n",
		"eslint.config.js": "export default [{\n  rules: {\n    'react-hooks/rules-of-hooks': 'off',\n  },\n}];\n",
		"src/App.tsx":      "// @ts-ignore\nexport const App = () => null;\n",
	})
	code, out, errOut = referee(t, nil, "audit", "--dir", r.dir, "--base", "main", "--json")
	require.Equal(t, 20, code, out+errOut)
	var rep auditReport
	require.NoError(t, json.Unmarshal([]byte(out), &rep))
	got := map[string]finding{}
	for _, f := range rep.Findings {
		got[f.Rule] = f
	}
	require.Len(t, rep.Findings, 3, "%+v", rep.Findings)
	assert.Equal(t, finding{Rule: "eslint_rule_off", Path: "eslint.config.js", Line: 3, Message: got["eslint_rule_off"].Message, Text: "    'react-hooks/rules-of-hooks': 'off',"}, got["eslint_rule_off"])
	assert.Equal(t, "src/App.tsx", got["ts_ignore"].Path)
	assert.Equal(t, 1, got["ts_ignore"].Line)
	assert.Equal(t, "tsconfig.json", got["ts_strict_off"].Path)
	assert.Equal(t, 3, got["ts_strict_off"].Line)
}
