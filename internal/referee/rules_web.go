package referee

import (
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// The TypeScript, lint and JS test runner rules (Jest, Vitest, Detox,
// React and React Native projects). Like the core rules, each reads only
// added lines of the file kinds where the construct means what the rule
// says. The suppression-comment rules (ts_*, eslint_disable*,
// flow_suppress) match comments on purpose: the construct IS a comment, so
// they have no Comment prefixes and instead require the directive to open
// the line's first comment outside a string literal, which a prose mention
// ("// we dropped the @ts-ignore") or a quoted one does not. The hunk rules
// (webHunkRules) read a change with the lines above it in its diff hunk:
// the arrays and rules blocks a line sits in.

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
	// On a line read alone a numeric 0 counts only for a kebab-case or
	// plugin/rule key, so a rule option such as { max: 0 } is not read as a
	// rule; eslintRulesZero reads a 0 for any key inside a rules block.
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
		Prepare: tsDirective,
		Pattern: regexp.MustCompile(`^\s*@ts-ignore\b`),
	},
	{
		// In a test file @ts-expect-error is the usual way to call an API
		// with a wrong type on purpose, so only code outside tests counts.
		ID: "ts_expect_error", Message: "type error suppressed outside a test (@ts-expect-error)", Files: isJSNotTest,
		Prepare: tsDirective,
		Pattern: regexp.MustCompile(`^\s*@ts-expect-error\b`),
	},
	{
		ID: "ts_nocheck", Message: "type checking turned off for the whole file (@ts-nocheck)", Files: isJS,
		Prepare: pragmaDirective,
		Pattern: regexp.MustCompile(`^\s*@ts-nocheck\b`),
	},
	{
		ID: "ts_strict_off", Message: "type check loosened in tsconfig (strict / noImplicitAny / strictNullChecks / noEmitOnError ... turned off)", Files: isTSConfig, Comment: slashSlash,
		Pattern: tsLoosened,
	},
	{
		ID: "eslint_disable", Message: "lint rule suppressed for a line (eslint-disable-line / eslint-disable-next-line)", Files: isJS,
		Prepare: commentDirective,
		Pattern: regexp.MustCompile(`^\s*eslint-disable-(?:next-)?line(?:\s|\*/|$)`),
	},
	{
		// ESLint honours eslint-disable only in a block comment.
		ID: "eslint_disable_file", Message: "lint rules disabled to the end of the file or an eslint-enable (/* eslint-disable */)", Files: isJS,
		Prepare: blockDirective,
		Pattern: regexp.MustCompile(`^\s*eslint-disable(?:\s|\*/|$)`),
	},
	{
		// A 0 for any other key is read inside a rules block by
		// eslintRulesZero.
		ID: "eslint_rule_off", Message: eslintRuleOffMessage, Files: isESLintConfig, Comment: slashSlashHash,
		Pattern: eslintRuleOff,
	},
	{
		ID: "flow_suppress", Message: "Flow type error suppressed ($FlowFixMe / $FlowIgnore / @noflow)", Files: isJS,
		Prepare: commentDirective,
		Pattern: regexp.MustCompile(`^\s*\$Flow(?:FixMe|Ignore|ExpectedError|Issue)\b`),
	},
	{
		ID: "flow_suppress", Message: "Flow type error suppressed ($FlowFixMe / $FlowIgnore / @noflow)", Files: isJS,
		Prepare: pragmaDirective,
		Pattern: regexp.MustCompile(`^\s*@noflow\b`),
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
		// The jest command line; the testPathIgnorePatterns array of a
		// config is read by testPathsIgnored. A pattern naming test files,
		// not a directory such as /e2e/.
		ID: "test_path_ignored", Message: testPathIgnoreMessage, Files: isScript, Comment: hash,
		Pattern: regexp.MustCompile(`--testPathIgnorePatterns(?:=|\s+)["']?[^\s"']*(?:\\*\.(?:test|spec)\b|__tests__)`),
	},
	{
		ID: "test_path_ignored", Message: "test files excluded from the run (vitest --exclude)", Files: isScript, Comment: hash,
		Pattern: regexp.MustCompile(`\bvitest\b[^;&|\n]*\s--exclude(?:=|\s+)["']?[^\s"'*]*(?:\.(?:test|spec)\.|__tests__/)`),
	},
}

const (
	eslintRuleOffMessage  = `lint rule turned off in the ESLint config ("off" / 0)`
	testPathIgnoreMessage = "test files excluded from the run (testPathIgnorePatterns)"
	vitestExcludeMessage  = "test files excluded from the run (exclude)"
)

// webHunkRules read a change together with the lines ahead of it in its
// diff hunk.
var webHunkRules = []func(p string, h hunk) []finding{maxWarningsRaised, testPathsIgnored, eslintRulesZero}

// --- comment directives ---

// openComment finds a JS line's first comment outside string literals:
// its opener ("//" or "/*") and its text after the opener (extra slashes
// or stars dropped). A quote with no closing quote on the line (an
// apostrophe in JSX text) does not open a string.
func openComment(l string) (opener, text string, ok bool) {
	for i := 0; i < len(l); i++ {
		switch l[i] {
		case '"', '\'', '`':
			if j := closingQuote(l, i); j > 0 {
				i = j
			}
		case '/':
			if i+1 < len(l) && (l[i+1] == '/' || l[i+1] == '*') {
				op := l[i : i+2]
				return op, strings.TrimLeft(l[i+2:], op[1:]), true
			}
		}
	}
	return "", "", false
}

// closingQuote is the index of the quote closing the one at l[i], or -1.
func closingQuote(l string, i int) int {
	for j := i + 1; j < len(l); j++ {
		switch l[j] {
		case '\\':
			j++
		case l[i]:
			return j
		}
	}
	return -1
}

// commentDirective is the text of the line's first comment: a directive
// has to open it, so a quoted or prose mention is not one.
func commentDirective(l string) string {
	_, t, _ := openComment(l)
	return t
}

// blockDirective is the text of the line's first comment when that is a
// block comment.
func blockDirective(l string) string {
	if op, t, _ := openComment(l); op == "/*" {
		return t
	}
	return ""
}

// continuation is the text of a line inside a block comment: one that
// starts with a directive (nothing else starts with "@ts-") or with a
// "*". With lastLine, a "*" line counts only when it closes the comment.
func continuation(l string, lastLine bool) string {
	t := strings.TrimLeft(l, " \t")
	switch {
	case strings.HasPrefix(t, "@"):
		return t
	case strings.HasPrefix(t, "*") && !strings.HasPrefix(t, "*/"):
		if lastLine && !strings.Contains(t, "*/") {
			return ""
		}
		return strings.TrimLeft(t, "*")
	}
	return ""
}

// tsDirective: TypeScript reads @ts-ignore / @ts-expect-error at the start
// of a // comment, of a block comment, or of a multi-line block
// comment's last line (`   @ts-ignore */`).
func tsDirective(l string) string {
	if t := commentDirective(l); t != "" {
		return t
	}
	return continuation(l, true)
}

// pragmaDirective: @ts-nocheck and @noflow are read at the start of any
// line of a leading comment.
func pragmaDirective(l string) string {
	if t := commentDirective(l); t != "" {
		return t
	}
	return continuation(l, false)
}

// --- eslint --max-warnings ---

var (
	// scriptEntry is a "key": "value" line of package.json.
	scriptEntry = regexp.MustCompile(`^\s*"((?:[^"\\]|\\.)+)"\s*:\s*"((?:[^"\\]|\\.)*)"`)
	commandSep  = regexp.MustCompile(`&&|\|\||[;|]`)
	eslintWord  = regexp.MustCompile(`(?:^|[^\w-])eslint(?:[^\w-]|$)`)
)

// lintCommands are a script line's commands that run eslint or pass
// --max-warnings, in order.
func lintCommands(l string) []string {
	var out []string
	for _, c := range commandSep.Split(l, -1) {
		if eslintWord.MatchString(c) || maxWarnings.MatchString(c) {
			out = append(out, c)
		}
	}
	return out
}

// maxWarningsRaised compares one change of a script: each removed line
// with --max-warnings is paired with the added line that replaces it (the
// same package.json script key; else the added eslint line most like
// it), and each of its lint commands with the command at the same
// position there. A command whose limit was raised, made unlimited (-1)
// or dropped is a finding; an added line nothing was paired with (a new
// script, a dependency) is not compared.
func maxWarningsRaised(p string, h hunk) []finding {
	if !isScript(p) || len(h.removed) == 0 || len(h.added) == 0 {
		return nil
	}
	used := map[int]bool{}
	var out []finding
	for _, r := range h.removed {
		if isHashComment(r) || !maxWarnings.MatchString(r) {
			continue
		}
		i := pairedLine(p, r, h.added, used)
		if i < 0 {
			continue
		}
		used[i] = true
		if msg := maxWarningsLoosened(scriptCommand(p, r), scriptCommand(p, h.added[i])); msg != "" {
			out = append(out, finding{Rule: "eslint_max_warnings", Path: p, Line: h.firstNew + i, Message: msg, Text: h.added[i]})
		}
	}
	return out
}

// scriptCommand is the command a script line runs: a package.json
// entry's value, else the line.
func scriptCommand(p, l string) string {
	if isPackageJSON(p) {
		if m := scriptEntry.FindStringSubmatch(l); m != nil {
			return m[2]
		}
		return ""
	}
	return l
}

// pairedLine is the index of the added line that replaces removed, or -1.
func pairedLine(p, removed string, added []string, used map[int]bool) int {
	if isPackageJSON(p) {
		m := scriptEntry.FindStringSubmatch(removed)
		if m == nil {
			return -1
		}
		for i, a := range added {
			if am := scriptEntry.FindStringSubmatch(a); am != nil && am[1] == m[1] && !used[i] {
				return i
			}
		}
		return -1
	}
	words := map[string]bool{}
	for _, w := range strings.Fields(removed) {
		words[w] = true
	}
	best, score := -1, 0
	for i, a := range added {
		if used[i] || isHashComment(a) || len(lintCommands(a)) == 0 {
			continue
		}
		s := 0
		for _, w := range strings.Fields(a) {
			if words[w] {
				s++
			}
		}
		if s > score {
			best, score = i, s
		}
	}
	return best
}

// maxWarningsLoosened compares the lint commands of a removed and an
// added command line by position.
func maxWarningsLoosened(was, now string) string {
	before, after := lintCommands(was), lintCommands(now)
	for j := range min(len(before), len(after)) {
		m := maxWarnings.FindStringSubmatch(before[j])
		if m == nil {
			continue
		}
		old, _ := strconv.Atoi(m[1])
		if old < 0 { // unlimited already: nothing left to loosen
			continue
		}
		n := maxWarnings.FindStringSubmatch(after[j])
		if n == nil {
			return "eslint --max-warnings " + strconv.Itoa(old) + " removed: warnings no longer fail the run"
		}
		switch v, _ := strconv.Atoi(n[1]); {
		case v < 0:
			return "eslint --max-warnings raised from " + strconv.Itoa(old) + " to unlimited (" + n[1] + ")"
		case v > old:
			return "eslint --max-warnings raised from " + strconv.Itoa(old) + " to " + n[1]
		}
	}
	return ""
}

func isHashComment(l string) bool { return strings.HasPrefix(strings.TrimSpace(l), "#") }

// --- arrays and blocks of JS / JSON configs ---

// jsFrame is one open { or [ of a JS or JSON config.
type jsFrame struct {
	open  byte   // '{' or '['
	key   string // the key it is the value of ("" for an array element)
	items int    // values seen directly inside it so far
}

// jsValue is a string, number or identifier the scanner saw.
type jsValue struct {
	line  int    // index into the scanned lines
	key   string // the key it is the value of ("" inside an array)
	text  string // a string's contents, or the token
	str   bool
	index int       // its position in the enclosing array, -1 outside one
	in    []jsFrame // the frames open around it, outermost first; valid during the call
}

// scanJS reads lines of a JS or JSON config (a hunk's head-side lines)
// as a stream of keys, values, brackets and braces, skipping comments,
// and calls visit for each value. It does not parse JavaScript: it is
// enough to tell which array or object a literal sits in. Frames opened
// before the first line are unknown; a close with none open is ignored.
func scanJS(lines []string, visit func(jsValue)) {
	var stack []jsFrame
	inBlock := false
	key := ""
	prev := byte(',') // the last token's kind, for telling a regex from a division
	value := func(line int, text string, str bool) {
		v := jsValue{line: line, key: key, text: text, str: str, index: -1}
		if n := len(stack); n > 0 && stack[n-1].open == '[' {
			v.key, v.index = "", stack[n-1].items
			stack[n-1].items++
		}
		v.in = stack
		visit(v)
		key, prev = "", 'v'
	}
	for ln, l := range lines {
		for i := 0; i < len(l); i++ {
			c := l[i]
			if inBlock {
				if c == '*' && i+1 < len(l) && l[i+1] == '/' {
					inBlock = false
					i++
				}
				continue
			}
			switch {
			case c == ' ' || c == '\t' || c == '\r':
			case c == '/' && i+1 < len(l) && l[i+1] == '/':
				i = len(l)
			case c == '/' && i+1 < len(l) && l[i+1] == '*':
				inBlock = true
				i++
			case c == '"' || c == '\'' || c == '`':
				j := closingQuote(l, i)
				if j < 0 {
					j = len(l)
				}
				text := l[i+1 : j]
				if nextIs(l, j+1, ':') {
					key, prev = text, 'k'
				} else {
					value(ln, text, true)
				}
				i = j
			case c == '/' && strings.IndexByte("(,=:[!&|?{};", prev) >= 0:
				// A regex literal.
				if j := closingQuote(l, i); j > 0 {
					i = j
				}
				value(ln, "", false)
			case isWordByte(c) || c == '-' && i+1 < len(l) && l[i+1] >= '0' && l[i+1] <= '9':
				j := i + 1
				for j < len(l) && (isWordByte(l[j]) || l[j] == '.') {
					j++
				}
				if nextIs(l, j, ':') {
					key, prev = l[i:j], 'k'
				} else {
					value(ln, l[i:j], false)
				}
				i = j - 1
			case c == '{' || c == '[':
				if n := len(stack); n > 0 && stack[n-1].open == '[' {
					stack[n-1].items++
				}
				stack = append(stack, jsFrame{open: c, key: key})
				key, prev = "", c
			case c == '}' || c == ']':
				if n := len(stack); n > 0 {
					stack = stack[:n-1]
				}
				key, prev = "", 'v'
			case c == ',':
				key, prev = "", c
			default:
				if c != ':' {
					key = ""
				}
				prev = c
			}
		}
	}
}

func isWordByte(c byte) bool {
	return c == '_' || c == '$' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// nextIs reports whether the first non-blank byte of l from i is c (and
// not a "::").
func nextIs(l string, i int, c byte) bool {
	for ; i < len(l); i++ {
		if l[i] != ' ' && l[i] != '\t' {
			return l[i] == c && (i+1 == len(l) || l[i+1] != c)
		}
	}
	return false
}

// lineFindings turns the added-line indexes a hunk rule hit into
// findings, in line order, leaving out a line a line rule with the same
// ID already reports.
func lineFindings(p string, h hunk, hit map[int]bool, rule, msg string) []finding {
	var out []finding
	for i, l := range h.added {
		if hit[i] && !flaggedByLineRule(rule, p, l) {
			out = append(out, finding{Rule: rule, Path: p, Line: h.firstNew + i, Message: msg, Text: l})
		}
	}
	return out
}

func flaggedByLineRule(id, p, l string) bool {
	for _, r := range lineRules {
		if r.ID == id && r.Files(p) && r.match(l) {
			return true
		}
	}
	return false
}

var (
	// jestTestPattern: a testPathIgnorePatterns regex naming test files,
	// not a directory such as /e2e/.
	jestTestPattern = regexp.MustCompile(`\\*\.(?:test|spec)\b|__tests__`)
	// namedTestFile: one test file (no glob) in Vitest's exclude. A
	// coverage exclude of test files uses a glob, and is in
	// coverage.exclude anyway.
	namedTestFile = regexp.MustCompile(`^[^*]*(?:\.(?:test|spec)\.[cm]?[jt]sx?|__tests__/[^*]+)$`)
)

// testPathsIgnored reads the change's added lines in the arrays they sit
// in, single-line or across lines (as Prettier writes them), with the
// opener on an added line or a context line above: a test file pattern
// added to Jest's testPathIgnorePatterns (a Jest/Vitest config or
// package.json) or to Vitest's test exclude (not coverage.exclude).
func testPathsIgnored(p string, h hunk) []finding {
	jest, vitest := isTestRunnerConfig(p) || isPackageJSON(p), isVitestConfig(p)
	if !jest && !vitest || len(h.added) == 0 {
		return nil
	}
	nb := len(h.before)
	jestHit, vitestHit := map[int]bool{}, map[int]bool{}
	scanJS(slices.Concat(h.before, h.added), func(v jsValue) {
		n := len(v.in)
		if v.line < nb || !v.str || n == 0 || v.in[n-1].open != '[' {
			return
		}
		switch key := v.in[n-1].key; {
		case jest && key == "testPathIgnorePatterns" && jestTestPattern.MatchString(v.text):
			jestHit[v.line-nb] = true
		case vitest && key == "exclude" && !inKey(v.in, "coverage") && namedTestFile.MatchString(v.text):
			vitestHit[v.line-nb] = true
		}
	})
	return append(lineFindings(p, h, jestHit, "test_path_ignored", testPathIgnoreMessage),
		lineFindings(p, h, vitestHit, "test_path_ignored", vitestExcludeMessage)...)
}

func inKey(frames []jsFrame, key string) bool {
	for _, f := range frames {
		if f.key == key {
			return true
		}
	}
	return false
}

var (
	yamlRulesKey = regexp.MustCompile(`^(-\s+)?rules\s*:\s*(?:#.*)?$`)
	yamlRuleZero = regexp.MustCompile(`^(?:"[^"]+"|'[^']+'|[\w@/.-]+)\s*:\s*(?:\[\s*0\s*\]|0)\s*(?:#.*)?$`)
)

// eslintRulesZero: 0 or [0] for any key directly inside a rules block of
// an ESLint config ("semi": 0, eqeqeq: 0, curly: [0]), the block opened on
// an added line or a context line above. An option such as { max: 0 }
// sits in a deeper object, so it is not a rule.
func eslintRulesZero(p string, h hunk) []finding {
	if !isESLintConfig(p) || len(h.added) == 0 {
		return nil
	}
	nb := len(h.before)
	lines := slices.Concat(h.before, h.added)
	hit := map[int]bool{}
	yaml := hasExt(p, ".yml", ".yaml")
	if !yaml {
		scanJS(lines, func(v jsValue) {
			if v.line >= nb && !v.str && v.text == "0" && isRuleValue(v) {
				hit[v.line-nb] = true
			}
		})
	}
	if yaml || path.Base(p) == ".eslintrc" { // .eslintrc may be YAML or JSON
		for _, i := range yamlRulesZero(lines) {
			if i >= nb {
				hit[i-nb] = true
			}
		}
	}
	return lineFindings(p, h, hit, "eslint_rule_off", eslintRuleOffMessage)
}

// isRuleValue: the value of a key of a rules object, or the first element
// of an array that is.
func isRuleValue(v jsValue) bool {
	n := len(v.in)
	if v.index < 0 {
		return n >= 1 && v.in[n-1].open == '{' && v.in[n-1].key == "rules"
	}
	return v.index == 0 && n >= 2 && v.in[n-1].key != "" && v.in[n-2].open == '{' && v.in[n-2].key == "rules"
}

// yamlRulesZero is the indexes of the lines that set a rule to 0 or [0]
// directly under a YAML "rules:" key.
func yamlRulesZero(lines []string) []int {
	var out []int
	rules, child := -1, -1 // the rules key's column, its entries' column
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		ind := len(l) - len(strings.TrimLeft(l, " "))
		if rules >= 0 && ind <= rules {
			rules = -1
		}
		if rules < 0 {
			if m := yamlRulesKey.FindStringSubmatch(t); m != nil {
				rules, child = ind+len(m[1]), -1
			}
			continue
		}
		if child < 0 {
			child = ind
		}
		if ind == child && yamlRuleZero.MatchString(t) {
			out = append(out, i)
		}
	}
	return out
}
