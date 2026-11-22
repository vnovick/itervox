package guardrails

import (
	"bytes"
	"context"
	"os"
	"path"
	"strings"

	"github.com/vnovick/itervox/internal/config"
)

// test_path_ignored on whole files. The hunk rule testPathsIgnored reads a
// change with at most AuditContext lines above it, so an array opened
// further up, or a template literal that spans lines, is out of its sight.
// Where the repository is at hand (AuditRange: the pre-push lane reasons
// and the Lanes view; `referee audit`), each changed Jest/Vitest config and
// package.json is read whole at the head and at the base instead, through
// bounded `git show` reads, and its test-path arrays are compared:
//
//   - Jest's testPathIgnorePatterns (a jest.config.*, a Vitest config, or
//     the "jest" key of package.json), and Vitest's test.exclude (the
//     exclude of a test block, not coverage.exclude): an entry at the head
//     that the base does not have and that names test files;
//   - Jest's testMatch: an entry the base has and the head does not, while
//     the head still sets testMatch (the run was narrowed).
//
// A file that cannot be read whole (larger than maxAuditConfigBytes, a git
// error, or a file the tokenizer cannot read) keeps the hunk rule, as does
// a patch audited without its repository (AuditPatch).

// Bounds on the configs one audit reads whole.
const (
	maxAuditConfigFiles = 64
	maxAuditConfigBytes = 1 << 20
)

const testMatchNarrowedMessage = "test files no longer run (testMatch narrowed)"

// testConfigEntry is one test-path array entry of a config file.
type testConfigEntry struct {
	kind  string // "jest_ignore", "vitest_exclude", "jest_match"
	value string
	line  int
}

// testConfigEntries reads the test-path arrays of a test runner config or
// package.json. ok is false when the file cannot be read exactly.
func testConfigEntries(p string, data []byte) (entries []testConfigEntry, ok bool) {
	if bytes.IndexByte(data, 0) >= 0 {
		return nil, false
	}
	isJSON := path.Ext(p) == ".json"
	if isJSON {
		// JSON (package.json, jest.config.json) must parse as JSON(C)
		// (config.ParseJSONC, encoding/json underneath); the tokenizer
		// below, whose string escapes are JSON's plus JS's own, then gives
		// the line numbers.
		if _, err := config.ParseJSONC(data); err != nil {
			return nil, false
		}
	}
	strs, err := jsConfigArrays(string(data))
	if err != nil {
		return nil, false
	}
	pkg, vitest := isPackageJSON(p), isVitestConfig(p)
	for _, s := range strs {
		n := len(s.keys)
		key := s.keys[n-1]
		if pkg && (n < 3 || s.opens[0] != '{' || s.keys[1] != "jest") {
			continue // only package.json's "jest" config
		}
		switch {
		case key == "testPathIgnorePatterns":
			entries = append(entries, testConfigEntry{"jest_ignore", s.value, s.line})
		case key == "testMatch" && !vitest:
			entries = append(entries, testConfigEntry{"jest_match", s.value, s.line})
		case vitest && key == "exclude" && n >= 2 && s.opens[n-2] == '{' && s.keys[n-2] == "test":
			entries = append(entries, testConfigEntry{"vitest_exclude", s.value, s.line})
		}
	}
	return entries, true
}

// testConfigFindings compares a config's head and base test-path arrays
// (base nil: the file is new). ok is false when either version cannot be
// read exactly; the caller then keeps the hunk rule.
func testConfigFindings(p string, base, head []byte) (out []AuditFinding, ok bool) {
	headEntries, ok := testConfigEntries(p, head)
	if !ok {
		return nil, false
	}
	var baseEntries []testConfigEntry
	if base != nil {
		if baseEntries, ok = testConfigEntries(p, base); !ok {
			return nil, false
		}
	}
	had := map[string]bool{}
	for _, e := range baseEntries {
		had[e.kind+"\x00"+e.value] = true
	}
	has := map[string]bool{}
	firstMatch := 0
	for _, e := range headEntries {
		has[e.kind+"\x00"+e.value] = true
		if e.kind == "jest_match" && firstMatch == 0 {
			firstMatch = e.line
		}
	}
	lines := strings.Split(string(head), "\n")
	text := func(n int) string {
		if n >= 1 && n <= len(lines) {
			return strings.TrimRight(lines[n-1], "\r")
		}
		return ""
	}
	add := func(line int, msg string) {
		t := text(line)
		if flaggedByLineRule("test_path_ignored", p, t) {
			return
		}
		out = append(out, AuditFinding{Rule: "test_path_ignored", Path: p, Line: line, Message: msg, Text: t})
	}
	for _, e := range headEntries {
		if had[e.kind+"\x00"+e.value] {
			continue
		}
		switch {
		case e.kind == "jest_ignore" && jestTestPattern.MatchString(e.value):
			add(e.line, testPathIgnoreMessage)
		case e.kind == "vitest_exclude" && namedTestFile.MatchString(e.value):
			add(e.line, vitestExcludeMessage)
		}
	}
	if firstMatch > 0 {
		for _, e := range baseEntries {
			if e.kind == "jest_match" && !has[e.kind+"\x00"+e.value] {
				add(firstMatch, testMatchNarrowedMessage)
				break
			}
		}
	}
	return out, true
}

// isTestPathConfig: a file whose test-path arrays testConfigFindings
// reads.
func isTestPathConfig(p string) bool { return isTestRunnerConfig(p) || isPackageJSON(p) }

// testConfigAudit is the whole-file result for a diff's configs.
type testConfigAudit struct {
	findings []AuditFinding
	exact    map[string]bool // the paths read whole (the hunk rule is skipped)
}

// auditTestConfigs reads each changed (added or modified) test runner
// config and package.json of the diff from..to (to "" is dir's working
// tree) at both ends, within maxAuditConfigFiles and maxAuditConfigBytes
// each, and compares their test-path arrays. status is the diff's `git
// diff --name-status -z --no-renames` output.
func auditTestConfigs(ctx context.Context, dir, from, to string, status []byte) testConfigAudit {
	res := testConfigAudit{exact: map[string]bool{}}
	fields := bytes.Split(status, []byte{0})
	read := 0
	for i := 0; i+1 < len(fields); i += 2 {
		st, p := string(fields[i]), string(fields[i+1])
		if (st != "M" && st != "A" && st != "T") || !isTestPathConfig(p) {
			continue
		}
		if read == maxAuditConfigFiles || ctx.Err() != nil {
			break
		}
		read++
		head, ok := readHeadConfig(ctx, dir, to, p)
		if !ok {
			continue
		}
		var base []byte
		if st != "A" {
			if base, ok = gitShowBounded(ctx, dir, from, p); !ok {
				continue
			}
		}
		found, ok := testConfigFindings(p, base, head)
		if !ok {
			continue
		}
		res.exact[p] = true
		res.findings = append(res.findings, found...)
	}
	return res
}

// readHeadConfig is p at to, or in dir's working tree for to "" (a
// regular file only, read through an os.Root of dir).
func readHeadConfig(ctx context.Context, dir, to, p string) ([]byte, bool) {
	if to != "" {
		return gitShowBounded(ctx, dir, to, p)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, false
	}
	defer func() { _ = root.Close() }()
	data, ok := readUntracked(root, p, maxAuditConfigBytes)
	return data, ok && data != nil
}

// gitShowBounded is `git show <rev>:<path>`, at most maxAuditConfigBytes.
func gitShowBounded(ctx context.Context, dir, rev, p string) ([]byte, bool) {
	out, truncated, err := boundedGit(ctx, dir, maxAuditConfigBytes, "show", "--no-textconv", rev+":"+p)
	if err != nil || truncated {
		return nil, false
	}
	return out, true
}

// AuditDiff is the audit of a diff whose repository is at hand: the
// deleted test files of status, the line and hunk rules on patch
// (AuditPatch), and test_path_ignored on the whole of each changed test
// runner config (auditTestConfigs), which replaces the hunk heuristic for
// the configs it could read. from and to are the diff's ends (to "" is
// dir's working tree); status and patch are its `git diff --name-status
// -z --no-renames` and `git diff -U<AuditContext>` outputs.
func AuditDiff(ctx context.Context, dir, from, to string, status, patch []byte) ([]AuditFinding, error) {
	cfgs := auditTestConfigs(ctx, dir, from, to, status)
	found, err := auditPatchWith(patch, cfgs.exact)
	if err != nil {
		return nil, err
	}
	return append(append(AuditDeletedTests(status), found...), cfgs.findings...), nil
}
