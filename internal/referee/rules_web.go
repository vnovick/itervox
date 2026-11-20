package referee

import (
	"regexp"
	"strconv"
	"strings"
)

// The TypeScript, lint and JS test runner rules (Jest, Vitest, Detox,
// React and React Native projects). Like the core rules, each reads only
// added lines of the file kinds where the construct means what the rule
// says. The suppression-comment rules (ts_*, eslint_disable*,
// flow_suppress) match comments on purpose: the construct IS a comment, so
// they have no Comment prefixes and instead require the directive to open
// the comment, which a prose mention ("// we dropped the @ts-ignore") does
// not.

// isTSConfig reports whether p is a TypeScript (or JS) project file.
func isTSConfig(p string) bool { return baseMatch(p, "tsconfig*.json", "jsconfig*.json") }

// isESLintConfig reports whether p is an ESLint config file (legacy or
// flat).
func isESLintConfig(p string) bool {
	return baseMatch(p, ".eslintrc", ".eslintrc.*", "eslint.config.*")
}

// isTestRunnerConfig reports whether p is a Jest or Vitest config file.
func isTestRunnerConfig(p string) bool {
	return baseMatch(p, "jest.config.*", "vitest.config.*", "vite.config.*", "vitest.workspace.*")
}

func isVitestConfig(p string) bool {
	return baseMatch(p, "vitest.config.*", "vite.config.*", "vitest.workspace.*")
}

func isPackageJSON(p string) bool { return baseMatch(p, "package.json") }

// isNonWorkflowScript is a script outside CI workflows, where ci_or_true
// does not already look.
func isNonWorkflowScript(p string) bool { return isScript(p) && !isWorkflow(p) }

func isScriptOrRunnerConfig(p string) bool { return isScript(p) || isTestRunnerConfig(p) }

func isJSNotTest(p string) bool { return isJS(p) && !isTestFile(p) }

var (
	slashSlashHash = []string{"//", "/*", "*", "#"}

	// tsLoosened: a type check turned off in tsconfig. Each of these is
	// off by default or under "strict", so writing the loosening value
	// explicitly is only ever done to override a stricter setting.
	tsLoosened = regexp.MustCompile(`"(?:strict|noImplicitAny|strictNullChecks|strictFunctionTypes|strictBindCallApply|strictPropertyInitialization|strictBuiltinIteratorReturn|noImplicitThis|alwaysStrict|useUnknownInCatchVariables|noImplicitReturns|noImplicitOverride|noUncheckedIndexedAccess|exactOptionalPropertyTypes|noFallthroughCasesInSwitch|noUnusedLocals|noUnusedParameters|noPropertyAccessFromIndexSignature|noEmitOnError|checkJs)"\s*:\s*false\b|"(?:noCheck|allowUnreachableCode|allowUnusedLabels)"\s*:\s*true\b`)

	// eslintRuleOff: "rule": "off" / 0 / ["off", ...] in an ESLint config.
	// A numeric 0 counts only for a kebab-case or plugin/rule key, so a rule
	// option such as { max: 0 } is not read as a rule.
	eslintRuleOff = regexp.MustCompile(`(?:^|[\s{,])(?:["'][^"']+["']|[A-Za-z@][\w@/-]*)\s*:\s*(?:\[\s*)?["']?off["']?\s*(?:[,}\]]|//|#|$)|(?:["'][^"']*[-/][^"']*["']|[A-Za-z@][\w@]*[-/][\w@/-]*)\s*:\s*(?:\[\s*)?0\s*(?:[,}\]]|//|#|$)`)

	// checkCommand is a lint, type or test command: one the rules below
	// look for "|| true" or an empty stand-in after.
	// prettier and biome count only when they check: --write formats.
	checkCommand = `(?:\b(?:eslint|tsc|vue-tsc|stylelint|jest|vitest|mocha|detox|playwright)\b|\bprettier\s[^;&|\n]*--(?:check|list-different)\b|\bbiome\s+(?:check|lint|ci)\b|\b(?:npm|pnpm|yarn|bun)(?:\s+run)?\s+(?:[\w-]+:)*(?:lint|test|typecheck|type-check|tsc|check)(?::[\w-]+)*\b)`

	checkOrTrue = regexp.MustCompile(checkCommand + `[^;&|\n]*\|\|\s*(?:true\b|:(?:[\s"']|$)|exit\s+0\b)`)

	// testRunner is a Jest/Vitest/Playwright/Detox run, directly or as the
	// package's test script.
	testRunner = `(?:\b(?:jest|vitest|playwright\s+test|react-scripts\s+test|detox\s+test)\b|\b(?:npm|pnpm|yarn|bun)(?:\s+run)?\s+test(?::[\w-]+)?\b)`

	snapshotFlags = `\s(?:-u|--updateSnapshot|--update-snapshots|--update|--ci=false|--no-ci)(?:[\s"'=\\]|$)`

	maxWarnings = regexp.MustCompile(`--max-warnings(?:=|\s+)["']?(-?\d+)`)
)

var webLineRules = []lineRule{
	{
		ID: "ts_ignore", Message: "type error suppressed (@ts-ignore)", Files: isJS,
		Pattern: regexp.MustCompile(`(?:/{2,}|/\*+)\s*@ts-ignore\b`),
	},
	{
		// In a test file @ts-expect-error is the usual way to call an API
		// with a wrong type on purpose, so only code outside tests counts.
		ID: "ts_expect_error", Message: "type error suppressed outside a test (@ts-expect-error)", Files: isJSNotTest,
		Pattern: regexp.MustCompile(`(?:/{2,}|/\*+)\s*@ts-expect-error\b`),
	},
	{
		ID: "ts_nocheck", Message: "type checking turned off for the whole file (@ts-nocheck)", Files: isJS,
		Pattern: regexp.MustCompile(`(?:^\s*\*|/{2,}|/\*+)\s*@ts-nocheck\b`),
	},
	{
		ID: "ts_strict_off", Message: "type check loosened in tsconfig (strict / noImplicitAny / strictNullChecks / noEmitOnError ... turned off)", Files: isTSConfig, Comment: slashSlash,
		Pattern: tsLoosened,
	},
	{
		ID: "eslint_disable", Message: "lint rule suppressed for a line (eslint-disable-line / eslint-disable-next-line)", Files: isJS,
		Pattern: regexp.MustCompile(`(?://|/\*)\s*eslint-disable-(?:next-)?line(?:\s|\*/|$)`),
	},
	{
		// ESLint honours eslint-disable only in a block comment.
		ID: "eslint_disable_file", Message: "lint rules disabled to the end of the file or an eslint-enable (/* eslint-disable */)", Files: isJS,
		Prepare: dropLineComment,
		Pattern: regexp.MustCompile(`/\*+\s*eslint-disable(?:\s|\*/|$)`),
	},
	{
		ID: "eslint_rule_off", Message: `lint rule turned off in the ESLint config ("off" / 0)`, Files: isESLintConfig, Comment: slashSlashHash,
		Pattern: eslintRuleOff,
	},
	{
		ID: "flow_suppress", Message: "Flow type error suppressed ($FlowFixMe / $FlowIgnore / @noflow)", Files: isJS,
		Pattern: regexp.MustCompile(`(?:/{2,}|/\*+)\s*\$Flow(?:FixMe|Ignore|ExpectedError|Issue)\b|(?:^\s*\*|/{2,}|/\*+)\s*@noflow\b`),
	},
	{
		ID: "check_or_true", Message: "exit status of a lint, type or test command discarded (|| true)", Files: isNonWorkflowScript, Comment: hash,
		Pattern: checkOrTrue,
	},
	{
		ID: "script_stubbed", Message: "package.json check script replaced by a command that always passes", Files: isPackageJSON,
		Pattern: regexp.MustCompile(`^\s*"(?:test|lint|typecheck|type-check|check|tsc|test:ci|lint:ci|ci)"\s*:\s*"\s*(?:true|:|exit 0|echo\b(?:[^"&|;\\]|\\.)*)\s*"\s*,?\s*$`),
	},
	{
		ID: "test_pass_with_no_tests", Message: "test run passes when it finds no tests (passWithNoTests)", Files: isScriptOrRunnerConfig, Comment: slashSlashHash,
		Pattern: regexp.MustCompile(`(?i)--pass-?with-?no-?tests(?:=true)?(?:[\s"'\\]|$)|\bpass-?with-?no-?tests["']?\s*:\s*true\b`),
	},
	{
		ID: "snapshot_update", Message: "snapshots rewritten in CI instead of compared (-u / --updateSnapshot / --ci=false)", Files: isWorkflow, Comment: hash,
		Pattern: regexp.MustCompile(testRunner + `[^;&|\n]*` + snapshotFlags),
	},
	{
		// A separate "test:update" script is normal; only the scripts CI
		// runs count.
		ID: "snapshot_update", Message: "snapshots rewritten in CI instead of compared (-u / --updateSnapshot / --ci=false)", Files: isPackageJSON,
		Pattern: regexp.MustCompile(`^\s*"(?:test|test:ci|ci)"\s*:\s*"[^"]*` + testRunner + `[^;&|"]*` + snapshotFlags),
	},
	{
		// A pattern naming test files, not a directory such as /e2e/.
		ID: "test_path_ignored", Message: "test files excluded from the run (testPathIgnorePatterns)", Files: isScriptOrRunnerConfig, Comment: slashSlashHash,
		Pattern: regexp.MustCompile(`testPathIgnorePatterns\b.*(?:\\*\.(?:test|spec)\b|__tests__)`),
	},
	{
		// One named test file (no glob): a coverage exclude of test files
		// uses a glob, so it does not count.
		ID: "test_path_ignored", Message: "test files excluded from the run (exclude)", Files: isVitestConfig, Comment: slashSlash,
		Pattern: regexp.MustCompile(`\bexclude["']?\s*:\s*\[[^\]]*["'][^"'*]*(?:\.(?:test|spec)\.[cm]?[jt]sx?|__tests__/[^"'*]+)["']`),
	},
	{
		ID: "test_path_ignored", Message: "test files excluded from the run (vitest --exclude)", Files: isScript, Comment: hash,
		Pattern: regexp.MustCompile(`\bvitest\b[^;&|\n]*\s--exclude(?:=|\s+)["']?[^\s"'*]*(?:\.(?:test|spec)\.|__tests__/)`),
	},
}

// maxWarningsRaised compares one hunk of a script: an eslint
// --max-warnings set higher (or unlimited, -1) than the removed line set,
// or dropped from an eslint command that had it.
func maxWarningsRaised(p string, h hunk) []finding {
	if !isScript(p) || len(h.removed) == 0 || len(h.added) == 0 {
		return nil
	}
	was, had := -2, false
	for _, l := range h.removed {
		if m := maxWarnings.FindStringSubmatch(l); m != nil && !isHashComment(l) {
			was, _ = strconv.Atoi(m[1])
			had = true
			break
		}
	}
	if !had || was < 0 { // unlimited already: nothing left to loosen
		return nil
	}
	var out []finding
	for i, l := range h.added {
		if isHashComment(l) {
			continue
		}
		msg := ""
		if m := maxWarnings.FindStringSubmatch(l); m != nil {
			now, _ := strconv.Atoi(m[1])
			switch {
			case now < 0:
				msg = "eslint --max-warnings raised from " + strconv.Itoa(was) + " to unlimited (" + m[1] + ")"
			case now > was:
				msg = "eslint --max-warnings raised from " + strconv.Itoa(was) + " to " + m[1]
			}
		} else if strings.Contains(l, "eslint") {
			msg = "eslint --max-warnings " + strconv.Itoa(was) + " removed: warnings no longer fail the run"
		}
		if msg != "" {
			out = append(out, finding{Rule: "eslint_max_warnings", Path: p, Line: h.firstNew + i, Message: msg, Text: l})
		}
	}
	return out
}

// dropLineComment skips a line that is a // comment: a block directive
// quoted inside one is not a directive.
func dropLineComment(l string) string {
	if strings.HasPrefix(strings.TrimSpace(l), "//") {
		return ""
	}
	return l
}

func isHashComment(l string) bool { return strings.HasPrefix(strings.TrimSpace(l), "#") }
