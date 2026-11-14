package main

import (
	"path"
	"regexp"
	"strconv"
	"strings"
)

// The audit rules: changes in a diff that weaken what CI checks. They are
// deliberately conservative: each looks only at added lines of the file
// kinds where the construct means what the rule says, and skips comment
// lines, so a finding is worth a human's look.

// finding is one weakening change.
type finding struct {
	Rule    string `json:"rule"`
	Path    string `json:"path"`
	Line    int    `json:"line,omitempty"` // in the head version; 0 for a deleted file
	Message string `json:"message"`
	Text    string `json:"text,omitempty"`
}

// fileKind reports whether a repository path is one a rule applies to.
type fileKind func(p string) bool

// lineRule flags an added line matching Pattern in a file of kind Files.
type lineRule struct {
	ID      string
	Message string
	Files   fileKind
	// Comment is the line-comment prefix of these files: an added line
	// that starts with one is skipped.
	Comment []string
	// Prepare rewrites the line before matching ("" means skip it).
	Prepare func(string) string
	Pattern *regexp.Regexp
}

var (
	jsExts     = []string{".js", ".jsx", ".ts", ".tsx", ".mjs", ".cjs", ".mts", ".cts"}
	slashSlash = []string{"//", "/*", "*"}
	hash       = []string{"#"}
)

func hasExt(p string, exts ...string) bool {
	for _, e := range exts {
		if strings.HasSuffix(p, e) {
			return true
		}
	}
	return false
}

// baseMatch reports whether p's base name matches one of the globs.
func baseMatch(p string, globs ...string) bool {
	b := path.Base(p)
	for _, g := range globs {
		if ok, _ := path.Match(g, b); ok {
			return true
		}
	}
	return false
}

var jsTestFile = regexp.MustCompile(`\.(test|spec)\.[cm]?[jt]sx?$`)

// isTestFile reports whether p is a test file by the common conventions.
func isTestFile(p string) bool {
	switch {
	case strings.HasSuffix(p, "_test.go"),
		jsTestFile.MatchString(p),
		baseMatch(p, "test_*.py", "*_test.py", "*_spec.rb", "*Test.java", "*Tests.java", "*Test.kt", "*Tests.kt"),
		strings.Contains("/"+p, "/__tests__/"):
		return true
	}
	return false
}

func isGoTest(p string) bool { return strings.HasSuffix(p, "_test.go") }
func isJS(p string) bool     { return hasExt(p, jsExts...) }
func isPython(p string) bool { return strings.HasSuffix(p, ".py") }

// isWorkflow reports whether p is a CI pipeline definition.
func isWorkflow(p string) bool {
	if ok, _ := path.Match(".github/workflows/*", p); ok && hasExt(p, ".yml", ".yaml") {
		return true
	}
	if strings.HasPrefix(p, ".github/actions/") && baseMatch(p, "action.yml", "action.yaml") {
		return true
	}
	return p == ".gitlab-ci.yml" || p == ".circleci/config.yml"
}

// isScript reports whether p is a file that runs git commands in CI or in
// a hook: shell scripts, Makefiles, package.json scripts, workflows and
// git hook managers' config.
func isScript(p string) bool {
	return isWorkflow(p) || hasExt(p, ".sh", ".bash", ".zsh", ".mk") ||
		baseMatch(p, "Makefile", "GNUmakefile", "makefile", "package.json", "lefthook.yml", ".pre-commit-config.yaml") ||
		strings.HasPrefix(p, ".husky/")
}

// isCoverageConfig reports whether p is a common coverage-threshold file.
func isCoverageConfig(p string) bool {
	return baseMatch(p, "jest.config.*", "vitest.config.*", "vite.config.*", "karma.conf.*",
		".nycrc", ".nycrc.*", ".c8rc", ".c8rc.*", "package.json",
		".coveragerc", "setup.cfg", "tox.ini", "pytest.ini", "pyproject.toml",
		"codecov.yml", ".codecov.yml", "codecov.yaml", ".codecov.yaml", ".simplecov")
}

// commitMessageArg matches a commit message argument, so text inside a
// message ("fix the -n flag") is never read as a flag. A short-flag
// cluster ending in m (-am, -nm) keeps its other flags.
var commitMessageArg = regexp.MustCompile(`(\s-[a-zA-Z]*m|\s--message)(?:=|\s*)(?:"(?:[^"\\]|\\.)*"|'[^']*'|\\"(?:[^"\\]|\\[^"])*\\"|\S+)`)

func stripCommitMessages(line string) string {
	return commitMessageArg.ReplaceAllString(line, "$1 MSG")
}

// lineRules is the table of added-line rules.
var lineRules = []lineRule{
	{
		ID: "go_skip", Message: "test skip added (t.Skip)", Files: isGoTest, Comment: slashSlash,
		Pattern: regexp.MustCompile(`\b[tb]\.Skip(?:Now|f)?\(`),
	},
	{
		ID: "js_skip", Message: "test skip added (.skip / xit)", Files: isJS, Comment: slashSlash,
		Pattern: regexp.MustCompile(`\b(?:it|test|describe|context|suite)\.skip\s*\(|(?:^|[^\w.$])x(?:it|test|describe)\s*\(`),
	},
	{
		ID: "js_only", Message: "focused test added (.only): every other test stops running", Files: isJS, Comment: slashSlash,
		Pattern: regexp.MustCompile(`\b(?:it|test|describe|context|suite)\.only\s*\(`),
	},
	{
		ID: "py_skip", Message: "test skip added (@pytest.mark.skip / @unittest.skip)", Files: isPython, Comment: hash,
		Pattern: regexp.MustCompile(`^\s*@(?:pytest\.mark\.skip(?:if)?|unittest\.skip(?:If|Unless)?)\b`),
	},
	{
		ID: "ci_continue_on_error", Message: "failure ignored in a CI workflow (continue-on-error / allow_failure: true)", Files: isWorkflow, Comment: hash,
		Pattern: regexp.MustCompile(`^[\s-]*(?:continue-on-error|allow_failure)\s*:\s*["']?true["']?\s*(?:#.*)?$`),
	},
	{
		ID: "ci_or_true", Message: "exit status discarded in a CI workflow (|| true)", Files: isWorkflow, Comment: hash,
		Pattern: regexp.MustCompile(`\|\|\s*true\b`),
	},
	{
		ID: "git_no_verify", Message: "git hooks bypassed (--no-verify / git commit -n)", Files: isScript, Comment: hash,
		Prepare: stripCommitMessages,
		// --no-verify on commit or push; -n (or a short-flag cluster with
		// n) on commit only: `git push -n` is --dry-run.
		Pattern: regexp.MustCompile(`\bgit\b(?:\s+-[cC]\s+\S+)*\s+(?:(?:commit|push)\b[^;&|\n]*\s--no-verify\b|commit\b[^;&|\n]*\s-[a-zA-Z]*n[a-zA-Z]*(?:[\s"']|$))`),
	},
}

// match applies the rule to one added line.
func (r lineRule) match(line string) bool {
	trimmed := strings.TrimSpace(line)
	for _, c := range r.Comment {
		if strings.HasPrefix(trimmed, c) {
			return false
		}
	}
	if r.Prepare != nil {
		line = r.Prepare(line)
	}
	return r.Pattern.MatchString(line)
}

// thresholdKey matches a coverage threshold setting and its number:
// fail_under = 90, --cov-fail-under=90, "branches": 80, target: 80%.
// (codecov's threshold is an allowed drop, so raising it weakens: it is
// deliberately not a key here rather than compared the wrong way round.)
var thresholdKey = regexp.MustCompile(`(?i)(?:^|[^\w-])(-{0,2}cov-fail-under|fail[_-]under|branches|functions|lines|statements|target|minimum_coverage)["']?\s*(?:[:=]\s*|\s+)["']?(-?\d+(?:\.\d+)?)`)

// thresholds are the coverage thresholds a set of lines sets, by key.
func thresholds(lines []string) map[string]float64 {
	out := map[string]float64{}
	for _, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "#") || strings.HasPrefix(strings.TrimSpace(l), "//") {
			continue
		}
		for _, m := range thresholdKey.FindAllStringSubmatch(l, -1) {
			key := strings.ToLower(strings.ReplaceAll(strings.TrimLeft(m[1], "-"), "_", "-"))
			v, err := strconv.ParseFloat(m[2], 64)
			if _, seen := out[key]; err == nil && !seen {
				out[key] = v
			}
		}
	}
	return out
}

// lowered compares one hunk of a coverage config file: a threshold whose
// removed line set a higher value than the added line that replaces it.
func lowered(p string, h hunk) []finding {
	if !isCoverageConfig(p) || len(h.removed) == 0 || len(h.added) == 0 {
		return nil
	}
	before := thresholds(h.removed)
	var out []finding
	for i, l := range h.added {
		for key, now := range thresholds([]string{l}) {
			if was, ok := before[key]; ok && now < was {
				out = append(out, finding{
					Rule: "coverage_lowered", Path: p, Line: h.firstNew + i,
					Message: "coverage threshold " + key + " lowered from " + strconv.FormatFloat(was, 'f', -1, 64) + " to " + strconv.FormatFloat(now, 'f', -1, 64),
					Text:    l,
				})
			}
		}
	}
	return out
}
