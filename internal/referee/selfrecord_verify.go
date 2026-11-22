package referee

import (
	"context"
	"crypto/ed25519"
	"flag"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"github.com/vnovick/itervox/internal/attest"
	"github.com/vnovick/itervox/internal/guardrails"
)

// selfVerifyFlags are verify's checks of referee records.
type selfVerifyFlags struct {
	require                     *bool
	base, head, dir             *string
	commit, ruleset, expectExit *string
}

func addSelfVerifyFlags(fs *flag.FlagSet) selfVerifyFlags {
	return selfVerifyFlags{
		require:    fs.Bool("require-referee", false, "fail when the file has no referee record"),
		base:       fs.String("base", "", "require each referee record to be for this ref's commit, with the policy sha256 at it"),
		head:       fs.String("head", "", "require each referee record to be for this head ref's commit"),
		dir:        fs.String("dir", ".", "repository --base and --head are resolved in"),
		commit:     fs.String("expect-referee-commit", "", "require referee records written by the referee built from this commit (7+ hex digits)"),
		ruleset:    fs.String("expect-ruleset", "", "require referee records judged with this ruleset version"),
		expectExit: fs.String("expect-exit", "", "require the last referee record's exit code to be this"),
	}
}

// selfExpect is what verify requires of referee records.
type selfExpect struct {
	required               bool
	baseCommit, headCommit string
	policies               map[string]attest.RefereePolicy // path -> at baseCommit
	commit, ruleset        string
	exit                   int // -1: not checked
}

// resolve turns the flags into expectations: refs resolved, numbers parsed.
// Any error is exit 2.
func (f selfVerifyFlags) resolve(ctx context.Context) (selfExpect, error) {
	want := selfExpect{commit: strings.ToLower(strings.TrimSpace(*f.commit)), ruleset: strings.TrimSpace(*f.ruleset), exit: -1}
	if want.commit != "" && len(want.commit) < 7 {
		return want, fmt.Errorf("--expect-referee-commit needs at least 7 hex digits, got %q", want.commit)
	}
	if s := strings.TrimSpace(*f.expectExit); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 || n > 255 {
			return want, fmt.Errorf("--expect-exit must be an exit code 0-255, got %q", s)
		}
		want.exit = n
	}
	var err error
	if *f.base != "" {
		if want.baseCommit, err = resolveCommit(ctx, *f.dir, *f.base); err != nil {
			return want, fmt.Errorf("--base: %w", err)
		}
	}
	if *f.head != "" {
		if want.headCommit, err = resolveCommit(ctx, *f.dir, *f.head); err != nil {
			return want, fmt.Errorf("--head: %w", err)
		}
	}
	want.required = *f.require || want.baseCommit != "" || want.headCommit != "" || want.commit != "" || want.ruleset != "" || want.exit >= 0
	return want, nil
}

// verifyRecords is verify's report: every record's signature (and tree),
// and every referee record's content against want.
func verifyRecords(ctx context.Context, stdout, stderr io.Writer, run attest.RunFile, pub ed25519.PublicKey, treeDir string, f selfVerifyFlags) int {
	want, err := f.resolve(ctx)
	if err != nil {
		return failf(stderr, "verify", "%v", err)
	}
	last := -1
	want.policies = map[string]attest.RefereePolicy{}
	for i, r := range run.Records {
		if r.Kind != attest.KindReferee {
			continue
		}
		last = i
		if r.Referee == nil || want.baseCommit == "" {
			continue
		}
		p := r.Referee.Policy.Path
		if _, seen := want.policies[p]; seen || p == "" {
			continue
		}
		if want.policies[p], err = policyDigest(ctx, *f.dir, want.baseCommit, p); err != nil {
			return failf(stderr, "verify", "%v", err)
		}
	}
	if want.required && last < 0 {
		_, _ = fmt.Fprintf(stdout, "FAIL no referee record in %s (one is required)\n", run.Path)
		return exitFailed
	}
	return attest.PrintVerificationWith(ctx, stdout, stderr, "referee verify", run, pub, attest.VerifyOptions{
		TreeDir: treeDir,
		Check: func(i int, r attest.Record) string {
			if r.Kind != attest.KindReferee {
				return ""
			}
			return selfRecordReason(r, run.Records, want, i == last)
		},
	})
}

// selfRecordReason is why a signed referee record fails want ("" when it
// does not).
func selfRecordReason(r attest.Record, all []attest.Record, want selfExpect, last bool) string {
	ref := r.Referee
	if ref == nil {
		return "a referee record without its referee section"
	}
	if reason := exitConsistency(r, all); reason != "" {
		return reason
	}
	if want.commit != "" && !commitMatches(ref.Commit, want.commit) {
		return fmt.Sprintf("written by referee commit %s, expected %s", ref.Commit, want.commit)
	}
	if want.ruleset != "" && ref.Ruleset != want.ruleset {
		return fmt.Sprintf("judged with ruleset %s, expected %s", ref.Ruleset, want.ruleset)
	}
	if last && want.exit >= 0 && r.ExitCode != want.exit {
		return fmt.Sprintf("exit code %d, expected %d", r.ExitCode, want.exit)
	}
	if want.baseCommit != "" {
		switch {
		case ref.BaseCommit == "":
			return "no base commit recorded (--base)"
		case ref.BaseCommit != want.baseCommit:
			return fmt.Sprintf("recorded for base %s, not %s (--base)", attest.ShortHex(ref.BaseCommit), attest.ShortHex(want.baseCommit))
		}
		if got := want.policies[ref.Policy.Path]; ref.Policy.Path == "" || got != ref.Policy {
			return fmt.Sprintf("policy %s: recorded %s, the base ref has %s", ref.Policy.Path, policyLabel(ref.Policy), policyLabel(got))
		}
	}
	if want.headCommit != "" && ref.HeadCommit != want.headCommit {
		return fmt.Sprintf("recorded for head %s, not %s (--head)", attest.ShortHex(ref.HeadCommit), attest.ShortHex(want.headCommit))
	}
	return ""
}

// exitConsistency checks a referee record's exit code against what it
// decided: the lane, the finding count, or its check record's exit code.
// Exit 2 (an error) is consistent with having decided nothing.
func exitConsistency(r attest.Record, all []attest.Record) string {
	ref, code := r.Referee, r.ExitCode
	switch ref.Command {
	case "lanes":
		if code == exitError && ref.Lane == "" {
			return ""
		}
		if !slices.Contains([]string{guardrails.LaneAuto, guardrails.LaneSecondOpinion, guardrails.LaneHuman}, ref.Lane) || laneExitCode(ref.Lane) != code {
			return fmt.Sprintf("exit code %d does not match lane %q", code, ref.Lane)
		}
	case "audit":
		if code == exitError && ref.Findings == 0 {
			return ""
		}
		if want := map[bool]int{true: exitHuman, false: exitOK}[ref.Findings > 0]; code != want {
			return fmt.Sprintf("exit code %d does not match %d finding(s)", code, ref.Findings)
		}
	case "run":
		if ref.CheckSig == "" {
			return "names no check record"
		}
		for _, c := range all {
			if c.Kind == attest.KindCheck && c.Sig == ref.CheckSig && c.Name == ref.Check {
				if got := runExitCode(c); got != code {
					return fmt.Sprintf("exit code %d does not match its check's (%d)", code, got)
				}
				return ""
			}
		}
		return fmt.Sprintf("its check record %q is not in this file", ref.Check)
	default:
		return fmt.Sprintf("unknown referee command %q", ref.Command)
	}
	return ""
}

// commitMatches reports whether a recorded build commit is want: equal, or
// want a 7+ digit prefix of a clean (not -dirty) commit.
func commitMatches(recorded, want string) bool {
	recorded = strings.ToLower(recorded)
	if recorded == want {
		return true
	}
	return !strings.HasSuffix(recorded, "-dirty") && len(want) >= 7 && strings.HasPrefix(recorded, want)
}

func policyLabel(p attest.RefereePolicy) string {
	if p.State == attest.PolicyPresent {
		return "sha256 " + attest.ShortHex(p.SHA256)
	}
	if p.State == "" {
		return "nothing"
	}
	return p.State
}
