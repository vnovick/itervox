package guardrails

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The rule unit tests moved here from internal/referee with the rules
// (audit_rules_web.go); these names keep them as they were written.
func auditPatch(patch []byte) ([]AuditFinding, error) { return AuditPatch(patch) }

func deletedTests(status []byte) []AuditFinding { return AuditDeletedTests(status) }

type finding = AuditFinding

// onePatch is a -U0 patch removing and adding lines in path.
func onePatch(path string, removed, added []string) []byte {
	s := fmt.Sprintf("diff --git a/%s b/%s\nindex 1111111..2222222 100644\n--- a/%s\n+++ b/%s\n@@ -1,%d +1,%d @@\n", path, path, path, path, len(removed), len(added))
	for _, l := range removed {
		s += "-" + l + "\n"
	}
	for _, l := range added {
		s += "+" + l + "\n"
	}
	return []byte(s)
}

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
	{"ts_ignore", "web/src/a.ts", `const s = "// @ts-ignore"`, false},
	{"ts_ignore", "web/src/a.ts", "const s = `/* @ts-ignore */`", false},
	{"ts_ignore", "web/src/a.ts", `// see the // @ts-ignore below`, false},
	{"ts_ignore", "web/src/a.ts", `   @ts-ignore */`, true},
	{"ts_ignore", "web/src/a.ts", ` * @ts-ignore */`, true},
	{"ts_ignore", "web/src/a.ts", `const s = 'x'; // @ts-ignore`, true},
	{"ts_ignore", "web/src/App.tsx", `      <p>Don't</p> {/* @ts-ignore */}`, true},
	// ts_expect_error
	{"ts_expect_error", "web/src/a.ts", `  // @ts-expect-error library types are wrong`, true},
	{"ts_expect_error", "web/src/a.jsx", `{/* @ts-expect-error */}`, true},
	{"ts_expect_error", "web/src/a.test.ts", `  // @ts-expect-error calling with a wrong type on purpose`, false},
	{"ts_expect_error", "web/src/__tests__/a.ts", `  // @ts-expect-error on purpose`, false},
	{"ts_expect_error", "docs/a.md", `// @ts-expect-error`, false},
	{"ts_expect_error", "web/src/a.ts", `// prefer @ts-expect-error over ignore`, false},
	{"ts_expect_error", "web/src/a.ts", `throw new Error('// @ts-expect-error')`, false},
	{"ts_expect_error", "web/src/a.ts", `     @ts-expect-error wrong upstream types */`, true},
	// ts_nocheck
	{"ts_nocheck", "web/src/a.ts", `// @ts-nocheck`, true},
	{"ts_nocheck", "web/src/a.js", ` * @ts-nocheck`, true},
	{"ts_nocheck", "README.md", `// @ts-nocheck`, false},
	{"ts_nocheck", "web/src/a.ts", `// never add @ts-nocheck`, false},
	{"ts_nocheck", "web/src/a.ts", `log("// @ts-nocheck")`, false},
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
	{"eslint_disable", "web/src/a.ts", `// we removed the // eslint-disable-line here`, false},
	{"eslint_disable", "web/src/a.ts", `const s = "// eslint-disable-next-line"`, false},
	// eslint_disable_file
	{"eslint_disable_file", "web/src/a.ts", `/* eslint-disable */`, true},
	{"eslint_disable_file", "web/src/a.js", `/* eslint-disable react-hooks/rules-of-hooks */`, true},
	{"eslint_disable_file", "web/src/a.ts", `/* eslint-enable */`, false},
	{"eslint_disable_file", "web/src/a.ts", `// eslint-disable`, false},
	{"eslint_disable_file", "web/src/a.ts", `// no /* eslint-disable */ blocks, see CONTRIBUTING`, false},
	{"eslint_disable_file", "web/src/a.css", `/* eslint-disable */`, false},
	{"eslint_disable_file", "web/src/a.ts", ` * eslint-disable is banned`, false},
	{"eslint_disable_file", "web/src/a.ts", `const banner = "/* eslint-disable */"`, false},
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
	{"eslint_rule_off", "eslint.config.js", `    rules: { semi: 0, curly: [0] },`, true},
	{"eslint_rule_off", ".eslintrc.json", `    "max": 0,`, false},
	{"eslint_rule_off", "eslint.config.js", `    rules: { 'max-len': ['error', { max: 0 }] },`, false},
	// flow_suppress
	{"flow_suppress", "app/Screen.js", `  // $FlowFixMe[incompatible-type]`, true},
	{"flow_suppress", "app/Screen.js", `      {/* $FlowIgnore */}`, true},
	{"flow_suppress", "app/Screen.js", ` * @noflow`, true},
	{"flow_suppress", "app/Screen.js", `// @noflow`, true},
	{"flow_suppress", "docs/flow.md", `// $FlowFixMe`, false},
	{"flow_suppress", "app/Screen.js", `// fixed the old $FlowFixMe`, false},
	{"flow_suppress", "app/Screen.js", `// @flow`, false},
	{"flow_suppress", "app/Screen.js", `const s = "// $FlowFixMe"`, false},
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
	{"test_path_ignored", "web/vitest.config.ts", `    coverage: { exclude: ['src/flaky.test.ts'] },`, false},
	{"test_path_ignored", "web/vitest.config.ts", `    coverage: { provider: 'v8', exclude: ['src/main.test.tsx'] }, exclude: ['src/b.test.ts'],`, true},
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

// ctxPatch is a patch of one hunk whose lines carry their own diff
// prefix: ' ' context, '-' removed, '+' added (as `git diff -U<n>`
// writes them).
func ctxPatch(path string, lines ...string) []byte {
	s := fmt.Sprintf("diff --git a/%s b/%s\nindex 1111111..2222222 100644\n--- a/%s\n+++ b/%s\n@@ -1 +1 @@\n", path, path, path, path)
	for _, l := range lines {
		s += l + "\n"
	}
	return []byte(s)
}

type ruleHit struct {
	line int
	msg  string
}

func hitsOf(t *testing.T, patch []byte, rule string) []ruleHit {
	t.Helper()
	found, err := auditPatch(patch)
	require.NoError(t, err)
	var out []ruleHit
	for _, f := range found {
		if f.Rule == rule {
			out = append(out, ruleHit{f.Line, f.Message})
		}
	}
	return out
}

// TestAuditMaxWarningsPairsByCommand: each removed --max-warnings is
// compared with the same package.json script (or the same command of the
// matching script line), so an unrelated eslint line added in the same
// hunk (a new script, a dependency) is not "--max-warnings removed", and
// every removed value is compared, not only the first.
func TestAuditMaxWarningsPairsByCommand(t *testing.T) {
	cases := []struct {
		name, path     string
		removed, added []string
		want           []ruleHit
	}{
		{"new script after a comma-only change", "package.json",
			[]string{`    "lint": "eslint . --max-warnings 0"`},
			[]string{`    "lint": "eslint . --max-warnings 0",`, `    "lint:fix": "eslint . --fix"`}, nil},
		{"dependency line", "package.json",
			[]string{`    "lint": "eslint . --max-warnings 0"`},
			[]string{`    "lint": "eslint . --max-warnings 0",`, `    "eslint-plugin-react": "^7.37.0"`}, nil},
		{"second removed value", "package.json",
			[]string{`    "lint": "eslint src --max-warnings 0",`, `    "lint:ci": "eslint . --max-warnings 0"`},
			[]string{`    "lint": "eslint src --max-warnings 0",`, `    "lint:ci": "eslint . --max-warnings 30"`},
			[]ruleHit{{2, "eslint --max-warnings raised from 0 to 30"}}},
		{"removed from its own script", "package.json",
			[]string{`    "lint": "eslint . --max-warnings 0",`, `    "test": "vitest run"`},
			[]string{`    "test": "vitest run",`, `    "lint": "eslint ."`},
			[]ruleHit{{2, "eslint --max-warnings 0 removed: warnings no longer fail the run"}}},
		{"second command of a line", "scripts/lint.sh",
			[]string{`eslint web --max-warnings 0 && eslint docs --max-warnings 5`},
			[]string{`eslint web --max-warnings 0 && eslint docs --max-warnings 50`},
			[]ruleHit{{1, "eslint --max-warnings raised from 5 to 50"}}},
		{"another eslint line added before", ".github/workflows/ci.yml",
			[]string{`      - run: npx eslint . --max-warnings 0`},
			[]string{`      - run: npx eslint --fix scripts/`, `      - run: npx eslint . --max-warnings 0 --cache`}, nil},
		{"dropped on the matching line", ".github/workflows/ci.yml",
			[]string{`      - run: npx eslint . --max-warnings 0`},
			[]string{`      - run: npx prettier --check .`, `      - run: npx eslint .`},
			[]ruleHit{{2, "eslint --max-warnings 0 removed: warnings no longer fail the run"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, hitsOf(t, onePatch(c.path, c.removed, c.added), "eslint_max_warnings"))
		})
	}
}

// TestAuditTestPathIgnoredMultiLine: the multi-line array form (as
// Prettier writes it) of testPathIgnorePatterns and of Vitest's test
// exclude, including one entry added to an existing array (its opener is
// a context line), and never coverage.exclude.
func TestAuditTestPathIgnoredMultiLine(t *testing.T) {
	cases := []struct {
		name, path string
		lines      []string
		want       []int
	}{
		{"new jest array", "jest.config.js", []string{
			"+  testPathIgnorePatterns: [",
			"+    '/node_modules/',",
			`+    'src/flaky\\.test\\.ts',`,
			"+  ],",
		}, []int{3}},
		{"entry added to a jest array", "jest.config.js", []string{
			" module.exports = {",
			"   testPathIgnorePatterns: [",
			"     '/node_modules/',",
			"+    '<rootDir>/src/__tests__/slow',",
			"   ],",
		}, []int{4}},
		{"package.json jest key", "package.json", []string{
			`   "jest": {`,
			`     "testPathIgnorePatterns": [`,
			`-      "/node_modules/"`,
			`+      "/node_modules/",`,
			`+      "checkout\\.spec\\.ts"`,
			`     ]`,
		}, []int{4}},
		{"directory entry", "jest.config.js", []string{
			"   testPathIgnorePatterns: [",
			"+    '/e2e/',",
			"   ],",
		}, nil},
		{"after the array closes", "jest.config.js", []string{
			"   testPathIgnorePatterns: ['/node_modules/'],",
			"+  testMatch: ['**/*.test.ts'],",
		}, nil},
		{"vitest test exclude", "web/vitest.config.ts", []string{
			" export default defineConfig({",
			"   test: {",
			"     exclude: [",
			"       ...configDefaults.exclude,",
			"+      'src/flaky.test.ts',",
			"     ],",
		}, []int{5}},
		{"vitest coverage exclude", "web/vitest.config.ts", []string{
			"   test: {",
			"     coverage: {",
			"       provider: 'v8',",
			"       exclude: [",
			"+        'src/main.test.tsx',",
			"+        'src/__tests__/fixtures/data.ts',",
			"       ],",
			"     },",
		}, nil},
		{"test exclude after the coverage block", "web/vitest.config.ts", []string{
			"   test: {",
			"     coverage: {",
			"       exclude: ['src/main.tsx'],",
			"     },",
			"+    exclude: [",
			"+      'src/flaky.test.ts',",
			"+    ],",
		}, []int{6}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var lines []int
			for _, h := range hitsOf(t, ctxPatch(c.path, c.lines...), "test_path_ignored") {
				lines = append(lines, h.line)
			}
			assert.Equal(t, c.want, lines)
		})
	}
}

// TestAuditESLintRuleZeroInRulesBlock: 0 / [0] turns any rule off inside a
// rules block (core rules such as semi, eqeqeq, curly included), tracked
// across the hunk's lines; an option such as { max: 0 } and a 0 outside
// a rules block are not rules.
func TestAuditESLintRuleZeroInRulesBlock(t *testing.T) {
	cases := []struct {
		name, path string
		lines      []string
		want       []int
	}{
		{"new rules block", "eslint.config.js", []string{
			"+  {",
			"+    rules: {",
			"+      semi: 0,",
			"+      eqeqeq: 0,",
			"+      curly: [0],",
			"+      'no-multiple-empty-lines': ['error', { max: 0 }],",
			"+      'max-len': ['error', {",
			"+        code: 0,",
			"+      }],",
			"+      quotes: [",
			"+        0,",
			"+      ],",
			"+    },",
			"+  },",
		}, []int{3, 4, 5, 11}},
		{"json rule in an existing block", ".eslintrc.json", []string{
			` {`,
			`   "rules": {`,
			`-    "semi": 2,`,
			`+    "semi": 0,`,
			`     "eqeqeq": "error"`,
		}, []int{3}},
		{"plugin rule reported once", ".eslintrc.json", []string{
			`   "rules": {`,
			`+    "react-hooks/exhaustive-deps": 0,`,
		}, []int{2}},
		{"yaml", ".eslintrc.yml", []string{
			" rules:",
			"+  semi: 0",
			"+  curly: [0]",
			"+  max-len: [error, {code: 0}]",
		}, []int{2, 3}},
		{"yaml overrides item", ".eslintrc.yaml", []string{
			" overrides:",
			"   - files: ['*.ts']",
			"     rules:",
			"+      eqeqeq: 0",
			"+  - files: ['*.js']",
			"+    retries: 0",
		}, []int{4}},
		{"outside a rules block", "eslint.config.js", []string{
			"   settings: {",
			"+    retries: 0,",
			"   },",
			"+  languageOptions: { ecmaVersion: 0 },",
		}, nil},
		{"commented out", "eslint.config.js", []string{
			"   rules: {",
			"+    // semi: 0,",
			"+    /* eqeqeq: 0, */",
		}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var lines []int
			for _, h := range hitsOf(t, ctxPatch(c.path, c.lines...), "eslint_rule_off") {
				lines = append(lines, h.line)
			}
			assert.Equal(t, c.want, lines)
		})
	}
}

// TestAuditContextLinesKeepLineNumbers: context lines count in the head
// version's line numbers and are never findings themselves, and a "+++"
// line inside a hunk is an added line, not a file header.
func TestAuditContextLinesKeepLineNumbers(t *testing.T) {
	patch := []byte("diff --git a/src/a.ts b/src/a.ts\n--- a/src/a.ts\n+++ b/src/a.ts\n@@ -10,4 +10,6 @@\n // @ts-ignore\n-const a = 1;\n+// @ts-ignore\n+const a = 1;\n const b = 2;\n+++ x // @ts-nocheck\n")
	found, err := auditPatch(patch)
	require.NoError(t, err)
	require.Len(t, found, 2, "%+v", found)
	assert.Equal(t, finding{Rule: "ts_ignore", Path: "src/a.ts", Line: 11, Message: found[0].Message, Text: "// @ts-ignore"}, found[0])
	assert.Equal(t, finding{Rule: "ts_nocheck", Path: "src/a.ts", Line: 14, Message: found[1].Message, Text: "++ x // @ts-nocheck"}, found[1])
}
