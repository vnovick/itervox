package referee

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/attest"
)

const (
	testRefereeVersion = "0.3.4-test"
	testRefereeCommit  = "abcdef0123456789abcdef0123456789abcdef01"
)

// selfRecordRepo is a repository whose main has a policy and whose
// checked-out branch changes a workflow (human lane) and adds a t.Skip
// (an audit finding), plus a key pair outside it.
type selfRecordRepo struct {
	testRepo
	key, pub, records string
	policy            string // the policy file's content on main
}

func newSelfRecordRepo(t *testing.T) selfRecordRepo {
	t.Helper()
	oldV, oldC := buildVersion, buildCommit
	t.Cleanup(func() { buildVersion, buildCommit = oldV, oldC })
	SetBuild(testRefereeVersion, testRefereeCommit)

	r := selfRecordRepo{testRepo: newTestRepo(t), policy: "second_opinion:\n  - \"lib/**\"\n"}
	r.commit("base", map[string]string{".referee.yml": r.policy, "a_test.go": "package a\n"})
	r.git("checkout", "-q", "-b", "change")
	r.commit("change", map[string]string{
		".github/workflows/ci.yml": "on: push\n",
		"a_test.go":                "package a\n\nfunc TestA(t *testing.T) { t.Skip(\"later\") }\n",
	})
	keys := t.TempDir()
	code, _, errOut := referee(t, nil, "keygen", "--out", keys)
	require.Equal(t, 0, code, errOut)
	r.key, r.pub = filepath.Join(keys, attest.KeyFileName), filepath.Join(keys, attest.PublicKeyFileName)
	r.records = filepath.Join(t.TempDir(), "referee.jsonl")
	return r
}

func (r selfRecordRepo) rev(ref string) string {
	return strings.TrimSpace(r.git("rev-parse", ref))
}

// refereeRecords reads path's referee records.
func refereeRecords(t *testing.T, path string) []attest.Record {
	t.Helper()
	recs, err := attest.ReadFile(path)
	require.NoError(t, err)
	var out []attest.Record
	for _, rec := range recs {
		if rec.Kind == attest.KindReferee {
			out = append(out, rec)
		}
	}
	return out
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// TestSelfRecordLanesAndAudit: lanes and audit with --key and --record each
// append one signed referee record naming the build, ruleset, policy hash at
// the base, base and head commits, the verdict and the exit code, and no
// file content; verify accepts both against the real base and head.
func TestSelfRecordLanesAndAudit(t *testing.T) {
	r := newSelfRecordRepo(t)
	code, out, errOut := referee(t, nil, "lanes", "--dir", r.dir, "--base", "main", "--key", r.key, "--record", r.records)
	require.Equal(t, exitHuman, code, out+errOut)
	code, out, errOut = referee(t, nil, "audit", "--dir", r.dir, "--base", "main", "--key", r.key, "--record", r.records)
	require.Equal(t, exitHuman, code, out+errOut)

	recs := refereeRecords(t, r.records)
	require.Len(t, recs, 2)
	wantPolicy := attest.RefereePolicy{Path: ".referee.yml", State: attest.PolicyPresent, SHA256: sha256Hex(r.policy)}
	for i, want := range []attest.Referee{
		{Command: "lanes", Lane: "human"},
		{Command: "audit", Findings: 1},
	} {
		got := recs[i]
		assert.Equal(t, exitHuman, got.ExitCode, want.Command)
		assert.False(t, got.StartedAt.IsZero(), want.Command)
		require.NotNil(t, got.Referee)
		ref := *got.Referee
		assert.Equal(t, want.Command, ref.Command)
		assert.Equal(t, want.Lane, ref.Lane)
		assert.Equal(t, want.Findings, ref.Findings)
		assert.Equal(t, testRefereeVersion, ref.Version)
		assert.Equal(t, testRefereeCommit, ref.Commit)
		assert.Equal(t, rulesetVersion, ref.Ruleset)
		assert.Equal(t, "main", ref.Base)
		assert.Equal(t, r.rev("main"), ref.BaseCommit)
		assert.Equal(t, r.rev("HEAD"), ref.HeadCommit)
		assert.Equal(t, wantPolicy, ref.Policy)
	}
	raw, err := os.ReadFile(r.records)
	require.NoError(t, err)
	for _, content := range []string{"lib/**", "t.Skip", "on: push"} {
		assert.NotContains(t, string(raw), content, "a referee record holds no file content")
	}

	code, out, errOut = referee(t, nil, "verify", r.records, "--pubkey", r.pub, "--dir", r.dir, "--base", "main", "--head", "HEAD",
		"--expect-referee-commit", testRefereeCommit[:10], "--expect-ruleset", rulesetVersion, "--expect-exit", "20")
	require.Equal(t, 0, code, out+errOut)
	assert.Contains(t, out, "ok   referee lanes")
	assert.Contains(t, out, "ok   referee audit")
	assert.Contains(t, out, "all 2 records verify")
}

// TestSelfRecordPolicyAbsentAndErrors: with no policy on the base the
// record says "absent"; a lanes run that fails (exit 2) is still recorded
// with its exit code, and verify --expect-exit 0 refuses it.
func TestSelfRecordPolicyAbsentAndErrors(t *testing.T) {
	r := newSelfRecordRepo(t)
	code, out, errOut := referee(t, nil, "lanes", "--dir", r.dir, "--base", "main", "--policy", "none.yml", "--key", r.key, "--record", r.records)
	require.Equal(t, exitHuman, code, out+errOut)
	code, _, _ = referee(t, nil, "lanes", "--dir", r.dir, "--base", "main", "--head", "no-such-ref", "--key", r.key, "--record", r.records)
	require.Equal(t, exitError, code)

	recs := refereeRecords(t, r.records)
	require.Len(t, recs, 2)
	assert.Equal(t, attest.RefereePolicy{Path: "none.yml", State: attest.PolicyAbsent}, recs[0].Referee.Policy)
	assert.Equal(t, exitError, recs[1].ExitCode)
	assert.Empty(t, recs[1].Referee.Lane)
	assert.Equal(t, r.rev("main"), recs[1].Referee.BaseCommit, "the base still resolves")
	assert.Empty(t, recs[1].Referee.HeadCommit)

	code, out, errOut = referee(t, nil, "verify", r.records, "--pubkey", r.pub)
	require.Equal(t, 0, code, "a recorded error is a genuine record: "+out+errOut)
	code, out, _ = referee(t, nil, "verify", r.records, "--pubkey", r.pub, "--expect-exit", "0")
	assert.Equal(t, exitFailed, code)
	assert.Contains(t, out, "exit code 2, expected 0")
}

// TestSelfRecordRun: run appends the check, the seal and a referee record
// bound to the check by its signature, with the check's exit code; with
// --base it records the base commit and the policy hash there.
func TestSelfRecordRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("checks are bash command lines")
	}
	r := newSelfRecordRepo(t)
	code, out, errOut := referee(t, nil, "run", "--dir", r.dir, "--name", "test", "--key", r.key, "--out", r.records, "--base", "main", "--", "exit 3")
	require.Equal(t, 3, code, out+errOut)
	code, out, errOut = referee(t, nil, "run", "--dir", r.dir, "--name", "lint", "--key", r.key, "--out", r.records, "--", "true")
	require.Equal(t, 0, code, out+errOut)

	recs, err := attest.ReadFile(r.records)
	require.NoError(t, err)
	require.Len(t, recs, 6, "check, seal and referee record per run")
	kinds := []string{}
	for _, rec := range recs {
		kinds = append(kinds, rec.Kind)
	}
	assert.Equal(t, []string{"check", "seal", "referee", "check", "seal", "referee"}, kinds)
	ref := recs[2]
	assert.Equal(t, 3, ref.ExitCode)
	assert.Equal(t, "run", ref.Referee.Command)
	assert.Equal(t, "test", ref.Referee.Check)
	assert.Equal(t, recs[0].Sig, ref.Referee.CheckSig)
	assert.Equal(t, r.rev("main"), ref.Referee.BaseCommit)
	assert.Equal(t, r.rev("HEAD"), ref.Referee.HeadCommit)
	assert.Equal(t, sha256Hex(r.policy), ref.Referee.Policy.SHA256)
	assert.Equal(t, attest.PolicyNotRead, recs[5].Referee.Policy.State, "no --base: no policy read")
	assert.Empty(t, recs[5].Referee.BaseCommit)

	code, out, errOut = referee(t, nil, "verify", r.records, "--pubkey", r.pub, "--tree", r.dir, "--expect-exit", "0")
	require.Equal(t, 0, code, out+errOut)
	assert.Contains(t, out, "all 6 records verify")
	code, out, _ = referee(t, nil, "verify", r.records, "--pubkey", r.pub, "--dir", r.dir, "--base", "main")
	assert.Equal(t, exitFailed, code, "the lint run recorded no base")
	assert.Contains(t, out, "no base commit recorded")

	// The referee record without its check record does not verify.
	lines := strings.SplitAfter(strings.TrimSpace(readString(t, r.records)), "\n")
	alone := filepath.Join(t.TempDir(), "alone.jsonl")
	require.NoError(t, os.WriteFile(alone, []byte(lines[1]+lines[2]), 0o600))
	code, out, _ = referee(t, nil, "verify", alone, "--pubkey", r.pub)
	assert.Equal(t, exitFailed, code)
	assert.Contains(t, out, `its check record "test" is not in this file`)
}

func readString(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(raw)
}

// resign rewrites the referee record at index i of path with edit applied,
// signed with the real key: what a buggy or compromised referee could write.
func resign(t *testing.T, r selfRecordRepo, edit func(*attest.Record)) string {
	t.Helper()
	recs, err := attest.ReadFile(r.records)
	require.NoError(t, err)
	signer, err := attest.LoadKey(r.key)
	require.NoError(t, err)
	ref := *recs[0].Referee
	recs[0].Referee = &ref
	edit(&recs[0])
	require.NoError(t, signer.Sign(&recs[0]))
	path := filepath.Join(t.TempDir(), "edited.jsonl")
	require.NoError(t, attest.AppendFile(path, recs[0]))
	return path
}

// TestVerifySelfRecordFailures: verify fails a referee record whose policy
// hash is not the base's, whose exit code does not match its lane, whose
// base, head, build or ruleset is not the expected one, or that has a
// byte flipped; and a file without one when one is required.
func TestVerifySelfRecordFailures(t *testing.T) {
	r := newSelfRecordRepo(t)
	code, out, errOut := referee(t, nil, "lanes", "--dir", r.dir, "--base", "main", "--key", r.key, "--record", r.records)
	require.Equal(t, exitHuman, code, out+errOut)
	verify := func(file string, extra ...string) (int, string) {
		code, out, errOut := referee(t, nil, append([]string{"verify", file, "--pubkey", r.pub, "--dir", r.dir}, extra...)...)
		return code, out + errOut
	}
	code, out = verify(r.records, "--base", "main", "--head", "HEAD")
	require.Equal(t, 0, code, out)

	wrongPolicy := resign(t, r, func(rec *attest.Record) { rec.Referee.Policy.SHA256 = sha256Hex("human: []\n") })
	code, out = verify(wrongPolicy, "--base", "main")
	assert.Equal(t, exitFailed, code)
	assert.Contains(t, out, "policy .referee.yml: recorded sha256 ")
	code, out = verify(wrongPolicy)
	assert.Equal(t, 0, code, "the policy hash is checked against --base: "+out)

	wrongExit := resign(t, r, func(rec *attest.Record) { rec.ExitCode = exitOK })
	code, out = verify(wrongExit)
	assert.Equal(t, exitFailed, code)
	assert.Contains(t, out, `exit code 0 does not match lane "human"`)
	code, out = verify(r.records, "--expect-exit", "0")
	assert.Equal(t, exitFailed, code)
	assert.Contains(t, out, "exit code 20, expected 0")

	unknownLane := resign(t, r, func(rec *attest.Record) { rec.Referee.Lane = "bogus" })
	code, _ = verify(unknownLane)
	assert.Equal(t, exitFailed, code)

	r.git("checkout", "-q", "main")
	r.commit("later", map[string]string{"b.go": "package b\n"})
	code, out = verify(r.records, "--base", "main")
	assert.Equal(t, exitFailed, code)
	assert.Contains(t, out, "recorded for base")
	code, out = verify(r.records, "--head", "main")
	assert.Equal(t, exitFailed, code)
	assert.Contains(t, out, "recorded for head")

	code, out = verify(r.records, "--expect-referee-commit", "0123456")
	assert.Equal(t, exitFailed, code)
	assert.Contains(t, out, "written by referee commit "+testRefereeCommit)
	dirty := resign(t, r, func(rec *attest.Record) { rec.Referee.Commit = testRefereeCommit + "-dirty" })
	code, _ = verify(dirty, "--expect-referee-commit", testRefereeCommit)
	assert.Equal(t, exitFailed, code, "a dirty build is not the commit")
	code, out = verify(r.records, "--expect-ruleset", "1-0000000000000000")
	assert.Equal(t, exitFailed, code)
	assert.Contains(t, out, "judged with ruleset")

	// Flip one hex digit of each recorded fact: the signature fails.
	line := readString(t, r.records)
	rec := refereeRecords(t, r.records)[0]
	for _, fact := range []string{rec.Referee.Policy.SHA256, rec.Referee.BaseCommit, rec.Referee.HeadCommit, rec.Referee.Commit, rec.Referee.Ruleset} {
		i := strings.Index(line, fact) + len(fact) - 1
		flipped := []byte(line)
		flipped[i] ^= 1
		path := filepath.Join(t.TempDir(), "flipped.jsonl")
		require.NoError(t, os.WriteFile(path, flipped, 0o600))
		code, out = verify(path)
		assert.Equal(t, exitFailed, code, fact)
		assert.Contains(t, out, "signature does not verify", fact)
	}
	flippedExit := strings.Replace(line, `"exit_code":20`, `"exit_code":0`, 1)
	path := filepath.Join(t.TempDir(), "exit.jsonl")
	require.NoError(t, os.WriteFile(path, []byte(flippedExit), 0o600))
	code, out = verify(path)
	assert.Equal(t, exitFailed, code)
	assert.Contains(t, out, "signature does not verify")

	// A file with no referee record, when one is required.
	if runtime.GOOS != "windows" {
		checks := filepath.Join(t.TempDir(), "checks.jsonl")
		code, out, errOut = referee(t, nil, "run", "--dir", r.dir, "--name", "t", "--key", r.key, "--out", checks, "--", "true")
		require.Equal(t, 0, code, out+errOut)
		lines := strings.SplitAfter(readString(t, checks), "\n")
		noReferee := filepath.Join(t.TempDir(), "no-referee.jsonl")
		require.NoError(t, os.WriteFile(noReferee, []byte(lines[0]+lines[1]), 0o600))
		code, out = verify(noReferee)
		assert.Equal(t, 0, code, "not required: "+out)
		for _, flags := range [][]string{{"--require-referee"}, {"--expect-exit", "0"}, {"--base", "main"}, {"--expect-ruleset", rulesetVersion}} {
			code, out = verify(noReferee, flags...)
			assert.Equal(t, exitFailed, code, flags)
			assert.Contains(t, out, "no referee record", flags)
		}
	}
}

// TestSelfRecordFailsClosed: a self-record that was asked for and cannot be
// made is exit 2, never the command's own exit code.
func TestSelfRecordFailsClosed(t *testing.T) {
	r := newSelfRecordRepo(t)
	missing := filepath.Join(t.TempDir(), "no-such-dir", "referee.jsonl")
	inside := filepath.Join(r.dir, "k")
	code, _, errOut := referee(t, nil, "keygen", "--out", inside)
	require.Equal(t, 0, code, errOut)
	for name, args := range map[string][]string{
		"key without record":     {"lanes", "--dir", r.dir, "--base", "main", "--key", r.key},
		"record without key":     {"audit", "--dir", r.dir, "--base", "main", "--record", r.records},
		"missing key":            {"lanes", "--dir", r.dir, "--base", "main", "--key", r.key + ".missing", "--record", r.records},
		"key inside the repo":    {"lanes", "--dir", r.dir, "--base", "main", "--key", filepath.Join(inside, attest.KeyFileName), "--record", r.records},
		"unwritable record":      {"audit", "--dir", r.dir, "--base", "change", "--key", r.key, "--record", missing},
		"run with a bad base":    {"run", "--dir", r.dir, "--name", "t", "--key", r.key, "--out", r.records, "--base", "no-such-ref", "--", "true"},
		"verify bad expect-exit": {"verify", r.records, "--pubkey", r.pub, "--expect-exit", "zero"},
		"verify short commit":    {"verify", r.records, "--pubkey", r.pub, "--expect-referee-commit", "abc"},
		"verify bad base":        {"verify", r.records, "--pubkey", r.pub, "--dir", r.dir, "--base", "no-such-ref"},
	} {
		t.Run(name, func(t *testing.T) {
			if name == "verify bad expect-exit" || name == "verify short commit" || name == "verify bad base" {
				code, out, errOut := referee(t, nil, "lanes", "--dir", r.dir, "--base", "change", "--key", r.key, "--record", r.records)
				require.Equal(t, exitOK, code, out+errOut)
			}
			code, out, errOut := referee(t, nil, args...)
			assert.Equal(t, exitError, code, "stdout: %s\nstderr: %s", out, errOut)
		})
	}
	_, err := os.Stat(missing)
	assert.ErrorIs(t, err, os.ErrNotExist)
}

// TestRulesetVersionChangesWithRules: the ruleset version is a hash of the
// rule tables, so editing a rule's pattern, id or message, or the default
// human lane, changes it without anyone bumping a constant.
func TestRulesetVersionChangesWithRules(t *testing.T) {
	assert.Regexp(t, regexp.MustCompile(`^1-[0-9a-f]{16}$`), rulesetVersion)
	human := []string{"/.github/workflows/**"}
	same := computeRuleset(lineRules, human)
	assert.Equal(t, same, computeRuleset(slices.Clone(lineRules), slices.Clone(human)), "deterministic")

	edits := map[string]func([]lineRule) []lineRule{
		"pattern": func(rs []lineRule) []lineRule { rs[0].Pattern = regexp.MustCompile(`\bt\.Skip\(`); return rs },
		"id":      func(rs []lineRule) []lineRule { rs[1].ID = "js_skip2"; return rs },
		"message": func(rs []lineRule) []lineRule { rs[2].Message += "!"; return rs },
		"comment": func(rs []lineRule) []lineRule { rs[3].Comment = []string{"//"}; return rs },
		"removed": func(rs []lineRule) []lineRule { return rs[1:] },
		"added":   func(rs []lineRule) []lineRule { return append(rs, rs[0]) },
	}
	for name, edit := range edits {
		assert.NotEqual(t, same, computeRuleset(edit(slices.Clone(lineRules)), human), name)
	}
	assert.NotEqual(t, same, computeRuleset(lineRules, append(slices.Clone(human), "/Makefile")), "human lane")
}

// TestSelfRecordBuildFallsBack: an unstamped build ("dev", "none") names
// what the Go build info says, else "dev" / "unknown", never "".
func TestSelfRecordBuildFallsBack(t *testing.T) {
	oldV, oldC := buildVersion, buildCommit
	t.Cleanup(func() { buildVersion, buildCommit = oldV, oldC })
	SetBuild("dev", "none")
	v, c := refereeBuild()
	assert.NotEmpty(t, v)
	assert.NotEmpty(t, c)
	assert.NotEqual(t, "none", c)
	SetBuild("1.2.3", "0123456789abcdef0123456789abcdef01234567")
	v, c = refereeBuild()
	assert.Equal(t, "1.2.3", v)
	assert.Equal(t, "0123456789abcdef0123456789abcdef01234567", c)
}

// TestSelfRecordJSONShape pins the referee section's field names.
func TestSelfRecordJSONShape(t *testing.T) {
	r := newSelfRecordRepo(t)
	code, out, errOut := referee(t, nil, "lanes", "--dir", r.dir, "--base", "main", "--key", r.key, "--record", r.records)
	require.Equal(t, exitHuman, code, out+errOut)
	var line map[string]any
	require.NoError(t, json.Unmarshal([]byte(readString(t, r.records)), &line))
	assert.Equal(t, "referee", line["kind"])
	assert.Equal(t, float64(20), line["exit_code"])
	ref, ok := line["referee"].(map[string]any)
	require.True(t, ok, line)
	keys := make([]string, 0, len(ref))
	for k := range ref {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	assert.Equal(t, []string{"base", "base_commit", "command", "commit", "head_commit", "lane", "policy", "ruleset", "version"}, keys)
	assert.Equal(t, map[string]any{"path": ".referee.yml", "state": "present", "sha256": sha256Hex(r.policy)}, ref["policy"])
}
