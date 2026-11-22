package referee

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/attest"
	"github.com/vnovick/itervox/internal/config"
)

// The referee's record of itself: every lanes, audit and run can also
// append a signed attest record (attest.KindReferee) saying which referee
// build ran, with which ruleset, which policy file (its SHA-256 at the base
// commit), on which base and head, and the exit code it returned. verify
// checks it. A command opts in with addSelfRecordFlags (or its own --key),
// openSelfRecord, the setters as it learns facts, and finish on every
// return: adding a command is that, plus a case in selfRecordReason.

// Build info set by the front end (main.version / main.commit ldflags).
var buildVersion, buildCommit string

// SetBuild records the referee build's version and commit, as the release
// build stamps them; "", "dev", "none" and "unknown" fall back to the Go
// build info (go install @version, vcs.revision). Call it before Run.
func SetBuild(version, commit string) { buildVersion, buildCommit = version, commit }

func unset(s string) bool {
	switch strings.TrimSpace(s) {
	case "", "dev", "none", "unknown", "(devel)":
		return true
	}
	return false
}

// refereeBuild is the version and commit the record names.
func refereeBuild() (version, commit string) {
	version, commit = buildVersion, buildCommit
	if bi, ok := debug.ReadBuildInfo(); ok {
		if unset(version) {
			version = bi.Main.Version
		}
		if unset(commit) {
			var rev string
			dirty := false
			for _, s := range bi.Settings {
				switch s.Key {
				case "vcs.revision":
					rev = s.Value
				case "vcs.modified":
					dirty = s.Value == "true"
				}
			}
			if commit = rev; rev != "" && dirty {
				commit += "-dirty"
			}
		}
	}
	if unset(version) {
		version = "dev"
	}
	if unset(commit) {
		commit = "unknown"
	}
	return version, commit
}

// rulesetEpoch is bumped by hand when a rule changes in code the hash below
// cannot see: a fileKind function, the threshold comparison, test_deleted,
// or how lanes match. Everything in the rule tables is hashed, so a changed
// pattern, message, comment prefix or default human-lane glob changes the
// ruleset version by itself. A new rule table must be added to the hash.
const rulesetEpoch = 1

// rulesetVersion names this build's rules: "<epoch>-<16 hex digits>".
var rulesetVersion = computeRuleset(lineRules, config.DefaultGuardrailHumanLane)

// RulesetVersion is this build's ruleset version, as referee records name
// it (`referee --version` prints it, for verify --expect-ruleset).
func RulesetVersion() string { return rulesetVersion }

func computeRuleset(rules []lineRule, humanLane []string) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "referee ruleset epoch %d\n", rulesetEpoch)
	for _, r := range rules {
		_, _ = fmt.Fprintf(h, "line\x00%s\x00%s\x00%s\x00%q\x00%t\n", r.ID, r.Message, r.Pattern.String(), r.Comment, r.Prepare != nil)
	}
	for _, re := range []*regexp.Regexp{thresholdKey, commitMessageArg, jsTestFile} {
		_, _ = fmt.Fprintf(h, "regexp\x00%s\n", re.String())
	}
	for _, g := range humanLane {
		_, _ = fmt.Fprintf(h, "human\x00%s\n", g)
	}
	return fmt.Sprintf("%d-%s", rulesetEpoch, hex.EncodeToString(h.Sum(nil))[:16])
}

// normalizePolicyPath is a policy path as a repository path ("" for none).
func normalizePolicyPath(p string) string {
	return strings.TrimPrefix(path.Clean("/"+strings.ReplaceAll(p, `\`, "/")), "/")
}

// policyDigest is the policy file's state and SHA-256 at commit.
func policyDigest(ctx context.Context, dir, commit, policyPath string) (attest.RefereePolicy, error) {
	pol := attest.RefereePolicy{Path: normalizePolicyPath(policyPath), State: attest.PolicyNotRead}
	if commit == "" || pol.Path == "" {
		return pol, nil
	}
	raw, found, err := showFile(ctx, dir, commit, pol.Path)
	if err != nil {
		return pol, fmt.Errorf("hash %s at the base ref: %w", pol.Path, err)
	}
	if !found {
		pol.State = attest.PolicyAbsent
		return pol, nil
	}
	sum := sha256.Sum256(raw)
	pol.State, pol.SHA256 = attest.PolicyPresent, hex.EncodeToString(sum[:])
	return pol, nil
}

// selfRecordFlags are lanes' and audit's --key and --record.
type selfRecordFlags struct{ key, record *string }

func addSelfRecordFlags(fs *flag.FlagSet) selfRecordFlags {
	return selfRecordFlags{
		key:    fs.String("key", "", "ed25519 private key (referee keygen): also append a signed referee record (with --record)"),
		record: fs.String("record", "", "JSONL file the signed referee record is appended to (with --key)"),
	}
}

// selfRecorder collects one command's facts and writes its record. A nil
// recorder (no --key) records nothing; every method is nil-safe.
type selfRecorder struct {
	signer           *attest.Signer
	out, dir         string
	started          time.Time
	info             attest.Referee
	baseRef, headRef string
	policyPath       string
}

// openSelfRecord loads the key for command's record, or returns nil when
// neither key nor out is set. Only one of them is an error, and so is a key
// inside the repository in dir.
func openSelfRecord(ctx context.Context, command, dir, key, out string) (*selfRecorder, error) {
	if key == "" && out == "" {
		return nil, nil
	}
	if key == "" || out == "" {
		return nil, errors.New("--key and --record go together")
	}
	root, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if top, err := git(ctx, root, "rev-parse", "--show-toplevel"); err == nil {
		root = strings.TrimSpace(string(top))
	}
	if rel, inside := within(root, key); inside {
		return nil, fmt.Errorf("the key %s is inside the repository (%s); keep it outside", key, rel)
	}
	signer, err := attest.LoadKey(key)
	if err != nil {
		return nil, err
	}
	return &selfRecorder{signer: signer, out: out, dir: dir, started: time.Now().UTC(), info: attest.Referee{Command: command}}, nil
}

// setRefs names the base and head refs as given, and the policy path.
func (s *selfRecorder) setRefs(base, head, policyPath string) {
	if s != nil {
		s.baseRef, s.headRef, s.policyPath = base, head, policyPath
		s.info.Base = base
	}
}

// setCommits records the commits the command resolved base and head to.
func (s *selfRecorder) setCommits(base, head string) {
	if s != nil {
		s.info.BaseCommit, s.info.HeadCommit = base, head
	}
}

func (s *selfRecorder) setLane(lane string) {
	if s != nil {
		s.info.Lane = lane
	}
}

func (s *selfRecorder) setFindings(n int) {
	if s != nil {
		s.info.Findings = n
	}
}

// build completes and signs the record for exit code code. Commits the
// command did not report are resolved here (best effort: a command that
// failed may not have a base).
func (s *selfRecorder) build(ctx context.Context, code int) (attest.Record, error) {
	if s.info.BaseCommit == "" && s.baseRef != "" {
		s.info.BaseCommit, _ = resolveCommit(ctx, s.dir, s.baseRef)
	}
	if s.info.HeadCommit == "" && s.headRef != "" {
		s.info.HeadCommit, _ = resolveCommit(ctx, s.dir, s.headRef)
	}
	pol, err := policyDigest(ctx, s.dir, s.info.BaseCommit, s.policyPath)
	if err != nil && code != exitError {
		return attest.Record{}, err
	}
	s.info.Policy = pol
	s.info.Version, s.info.Commit = refereeBuild()
	s.info.Ruleset = rulesetVersion
	info := s.info
	rec := attest.Record{
		Version: attest.RecordVersion, Kind: attest.KindReferee, ExitCode: code,
		StartedAt: s.started, DurationMs: time.Since(s.started).Milliseconds(), Referee: &info,
	}
	if err := s.signer.Sign(&rec); err != nil {
		return attest.Record{}, err
	}
	return rec, nil
}

// finish appends the record for exit code code and returns code, or
// exitError when the record cannot be written: a run the referee was asked
// to record and could not is never a pass.
func (s *selfRecorder) finish(ctx context.Context, code int, stderr io.Writer) int {
	if s == nil {
		return code
	}
	rec, err := s.build(ctx, code)
	if err == nil {
		err = attest.AppendFile(s.out, rec)
	}
	if err != nil {
		return failf(stderr, s.info.Command, "referee record: %v", err)
	}
	return code
}
