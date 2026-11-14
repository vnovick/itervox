package main

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// onePatch is a -U0 patch adding lines to path.
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

// TestAuditLineRules is the table of added-line rules: each rule has
// positive cases and negative cases (the near misses it must not flag).
func TestAuditLineRules(t *testing.T) {
	cases := []struct {
		rule, path, line string
		want             bool
	}{
		// go_skip
		{"go_skip", "pkg/a_test.go", `	t.Skip("flaky")`, true},
		{"go_skip", "pkg/a_test.go", `	t.SkipNow()`, true},
		{"go_skip", "pkg/a_test.go", `	b.Skipf("no %s", x)`, true},
		{"go_skip", "pkg/a.go", `	t.Skip("not a test file")`, false},
		{"go_skip", "pkg/a_test.go", `	// t.Skip("commented out")`, false},
		{"go_skip", "pkg/a_test.go", `	if testing.Short() { return }`, false},
		// js_skip
		{"js_skip", "web/a.test.ts", `it.skip('works', () => {})`, true},
		{"js_skip", "web/a.test.tsx", `  describe.skip("suite", () => {`, true},
		{"js_skip", "web/a.spec.js", `xit("works", () => {})`, true},
		{"js_skip", "e2e/login.ts", `test.skip(({ browserName }) => browserName === 'webkit')`, true},
		{"js_skip", "web/a.test.ts", `// it.skip('commented')`, false},
		{"js_skip", "web/a.test.ts", `const exit = () => {}`, false},
		{"js_skip", "web/a.test.ts", `foo.xit(1)`, false},
		{"js_skip", "docs/a.md", `it.skip('not js')`, false},
		// js_only
		{"js_only", "web/a.test.ts", `it.only('works', () => {})`, true},
		{"js_only", "web/a.test.ts", `test.describe.only('suite', () => {})`, true},
		{"js_only", "web/a.ts", `const x = list.only(1)`, false},
		{"js_only", "web/a.test.ts", ` * it.only in a doc comment`, false},
		// py_skip
		{"py_skip", "tests/test_a.py", `@pytest.mark.skip(reason="flaky")`, true},
		{"py_skip", "tests/test_a.py", `    @pytest.mark.skipif(sys.platform == "win32", reason="x")`, true},
		{"py_skip", "tests/test_a.py", `@unittest.skip("later")`, true},
		{"py_skip", "tests/test_a.py", `# @pytest.mark.skip`, false},
		{"py_skip", "tests/test_a.py", `pytest.mark.skip is documented here`, false},
		// ci_continue_on_error
		{"ci_continue_on_error", ".github/workflows/ci.yml", `    continue-on-error: true`, true},
		{"ci_continue_on_error", ".github/workflows/ci.yaml", `      - continue-on-error: "true"`, true},
		{"ci_continue_on_error", ".gitlab-ci.yml", `  allow_failure: true`, true},
		{"ci_continue_on_error", ".github/workflows/ci.yml", `    continue-on-error: false`, false},
		{"ci_continue_on_error", ".github/workflows/ci.yml", `    continue-on-error: ${{ matrix.experimental }}`, false},
		{"ci_continue_on_error", "config/app.yml", `continue-on-error: true`, false},
		{"ci_continue_on_error", ".github/workflows/ci.yml", `    # continue-on-error: true`, false},
		// ci_or_true
		{"ci_or_true", ".github/workflows/ci.yml", `      - run: make test || true`, true},
		{"ci_or_true", ".github/workflows/ci.yml", `          go vet ./... ||true`, true},
		{"ci_or_true", ".github/workflows/ci.yml", `      - run: make test || exit 1`, false},
		{"ci_or_true", ".github/workflows/ci.yml", `      # a || true would hide failures`, false},
		{"ci_or_true", "scripts/build.sh", `rm x || true`, false},
		// git_no_verify
		{"git_no_verify", "scripts/release.sh", `git commit --no-verify -m "release"`, true},
		{"git_no_verify", "scripts/release.sh", `git push --no-verify origin HEAD`, true},
		{"git_no_verify", "scripts/release.sh", `git commit -n -m "wip"`, true},
		{"git_no_verify", "scripts/release.sh", `git -C repo commit -anm wip`, true},
		{"git_no_verify", "Makefile", "\tgit commit -q -n -m done", true},
		{"git_no_verify", "package.json", `    "ship": "git commit -m \"wip\" --no-verify && git push",`, true},
		{"git_no_verify", ".github/workflows/bot.yml", `        run: git push --no-verify`, true},
		{"git_no_verify", "scripts/release.sh", `git push -n origin main`, false},
		{"git_no_verify", "scripts/release.sh", `git commit -m "add the -n flag to the CLI"`, false},
		{"git_no_verify", "scripts/release.sh", `git commit -am 'fix --no-verify docs'`, false},
		{"git_no_verify", "scripts/release.sh", `git commit --amend --no-edit`, false},
		{"git_no_verify", "scripts/release.sh", `# git commit --no-verify`, false},
		{"git_no_verify", "src/main.go", `exec("git", "commit", "--no-verify")`, false},
		{"git_no_verify", "scripts/release.sh", `git log -n 5 && git commit -m x`, false},
	}
	for _, c := range cases {
		t.Run(c.rule+"/"+c.line, func(t *testing.T) {
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

// TestAuditCoverageLowered: a threshold lowered in the same hunk of a
// coverage config is a finding; raised, unchanged, another key, or a file
// that is not coverage config is not.
func TestAuditCoverageLowered(t *testing.T) {
	cases := []struct {
		name, path       string
		removed, added   []string
		want             bool
		wantMessageSnips string
	}{
		{"jest", "jest.config.js", []string{"      branches: 80,"}, []string{"      branches: 60,"}, true, "branches lowered from 80 to 60"},
		{"vitest json-ish", "web/vitest.config.ts", []string{`        "lines": 90,`}, []string{`        "lines": 89.5,`}, true, "lines lowered from 90 to 89.5"},
		{"coveragerc", ".coveragerc", []string{"fail_under = 90"}, []string{"fail_under = 70"}, true, "fail-under lowered"},
		{"pyproject", "pyproject.toml", []string{"fail-under = 85"}, []string{"fail_under = 80"}, true, "fail-under lowered"},
		{"pytest addopts", "pytest.ini", []string{"addopts = --cov=app --cov-fail-under=90"}, []string{"addopts = --cov=app --cov-fail-under=50"}, true, "cov-fail-under lowered"},
		{"codecov target", "codecov.yml", []string{"        target: 80%"}, []string{"        target: 75%"}, true, "target lowered"},
		{"raised", "jest.config.js", []string{"branches: 60,"}, []string{"branches: 80,"}, false, ""},
		{"unchanged", ".coveragerc", []string{"fail_under = 90"}, []string{"fail_under = 90 "}, false, ""},
		{"other key", "jest.config.js", []string{"branches: 80,"}, []string{"functions: 60,"}, false, ""},
		{"not coverage config", "src/config.ts", []string{"lines: 80,"}, []string{"lines: 10,"}, false, ""},
		{"pyproject target-version", "pyproject.toml", []string{`target-version = "py312"`}, []string{`target-version = "py39"`}, false, ""},
		{"comment", ".coveragerc", []string{"# fail_under = 90"}, []string{"# fail_under = 10"}, false, ""},
		{"only removed", "jest.config.js", []string{"branches: 80,"}, nil, false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			found, err := auditPatch(onePatch(c.path, c.removed, c.added))
			require.NoError(t, err)
			var hit *finding
			for i := range found {
				if found[i].Rule == "coverage_lowered" {
					hit = &found[i]
				}
			}
			if !c.want {
				assert.Nil(t, hit, "%+v", found)
				return
			}
			require.NotNil(t, hit, "%+v", found)
			assert.Contains(t, hit.Message, c.wantMessageSnips)
			assert.Equal(t, 1, hit.Line)
		})
	}
}

// TestAuditDeletedTests: a deleted test file (a rename, with --no-renames,
// is a deletion) is a finding; a deleted non-test file or an added or
// modified test file is not.
func TestAuditDeletedTests(t *testing.T) {
	cases := []struct {
		status, path string
		want         bool
	}{
		{"D", "internal/x/x_test.go", true},
		{"D", "web/src/App.test.tsx", true},
		{"D", "web/src/a.spec.mjs", true},
		{"D", "tests/test_api.py", true},
		{"D", "app/api_test.py", true},
		{"D", "spec/models/user_spec.rb", true},
		{"D", "src/test/java/FooTest.java", true},
		{"D", "web/src/__tests__/util.js", true},
		{"D", "internal/x/x.go", false},
		{"D", "docs/testing.md", false},
		{"D", "src/latest.ts", false},
		{"A", "internal/x/x_test.go", false},
		{"M", "internal/x/x_test.go", false},
	}
	for _, c := range cases {
		got := deletedTests([]byte(c.status + "\x00" + c.path + "\x00"))
		assert.Equal(t, c.want, len(got) == 1, "%s %s: %+v", c.status, c.path, got)
	}
}

// TestAuditCmdOnRepo: on a real repository, audit reports a renamed test
// file and an added skip (exit 20) and nothing on a clean change (exit 0).
func TestAuditCmdOnRepo(t *testing.T) {
	r := newTestRepo(t)
	r.commit("base", map[string]string{
		"calc.go":      "package calc\n\nfunc Add(a, b int) int { return a + b }\n",
		"calc_test.go": "package calc\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {}\n",
	})
	r.git("checkout", "-q", "-b", "clean")
	r.commit("code", map[string]string{"calc.go": "package calc\n\n// Add adds.\nfunc Add(a, b int) int { return a + b }\n"})
	code, out, errOut := referee(t, nil, "audit", "--dir", r.dir, "--base", "main")
	assert.Equal(t, 0, code, out+errOut)
	assert.Contains(t, out, "audit: no findings")

	r.git("checkout", "-q", "-b", "weaken")
	r.git("mv", "calc_test.go", "calc_old_test.go.bak")
	r.commit("skip", map[string]string{"more_test.go": "package calc\n\nimport \"testing\"\n\nfunc TestMore(t *testing.T) {\n\tt.Skip(\"later\")\n}\n"})
	code, out, errOut = referee(t, nil, "audit", "--dir", r.dir, "--base", "main", "--json")
	require.Equal(t, 20, code, out+errOut)
	var rep auditReport
	require.NoError(t, json.Unmarshal([]byte(out), &rep))
	require.Len(t, rep.Findings, 2, "%+v", rep.Findings)
	assert.Equal(t, finding{Rule: "test_deleted", Path: "calc_test.go", Message: "test file deleted or renamed"}, rep.Findings[0])
	assert.Equal(t, "go_skip", rep.Findings[1].Rule)
	assert.Equal(t, "more_test.go", rep.Findings[1].Path)
	assert.Equal(t, 6, rep.Findings[1].Line)
}
