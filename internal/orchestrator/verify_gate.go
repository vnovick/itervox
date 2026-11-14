package orchestrator

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/attest"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
)

// Signed checks: agent.profiles.<name>.verify. require_evidence (#80) asks
// the agent to record its own checks, so the writer of the fix also writes
// the proof. With verify the daemon runs each check itself, in the
// worktree, after the agent's run ends and before the run counts as
// succeeded, and signs what it saw (internal/attest): the exit code, the
// output hash and tail, and the tree the check ran on. Any failure holds
// the run input-required, exactly like a failed evidence gate.
//
// It all runs on the worker goroutine; the outcome reaches the event loop
// only through the worker's exit event (input-required on failure,
// succeeded otherwise). Records are written to the attestation store, not
// to State.

// attestationConfig is what SetAttestation installs.
type attestationConfig struct {
	signer *attest.Signer
	store  attest.Store
}

// SetAttestation installs the signing key and the record store for signed
// checks. Call before Run. Without it verify still runs and gates, but its
// records are neither signed nor stored.
func (o *Orchestrator) SetAttestation(signer *attest.Signer, store attest.Store) {
	if signer == nil {
		o.attestation.Store(nil)
		return
	}
	o.attestation.Store(&attestationConfig{signer: signer, store: store})
}

// AttestationStore is the record store and the public key records verify
// with; ok is false when no attestation is configured.
func (o *Orchestrator) AttestationStore() (attest.Store, ed25519.PublicKey, bool) {
	a := o.attestation.Load()
	if a == nil {
		return attest.Store{}, nil, false
	}
	return a.store, a.signer.PublicKey(), true
}

// verifyTreeExcludes are left out of every check's tree hash: Itervox's own
// bookkeeping, which the worker may write after the checks (the handoff it
// synthesizes, the agent's evidence file).
var verifyTreeExcludes = []string{HandoffDirRelPath, EvidenceDirRelPath}

// attestRunID names one run's record file: the run timestamp and profile,
// like the run's handoff file, so files sort by time.
func attestRunID(runTimestamp, profileName string) string {
	role := strings.ReplaceAll(strings.TrimSpace(profileName), " ", "-")
	if role == "" {
		role = "agent"
	}
	return runTimestamp + "_" + role
}

// verifyVerdict is the outcome of a run's signed checks.
type verifyVerdict struct {
	Failed  []string // one human-readable reason per failure
	Records []attest.Record
}

func (v verifyVerdict) ok() bool { return len(v.Failed) == 0 }

// verifyRun is one run's identity for its records.
type verifyRun struct {
	issue    domain.Issue
	runID    string
	profile  string
	runLogID string
}

// runVerifyChecks runs checks in wsPath, in order, stopping at the first
// failure, then seals the run with the tree after the last check, signs and
// stores the records. A check that changed the tree (the seal differs from
// a check's tree) fails the run too: the proof must be for the tree that is
// handed on. Runs on the worker goroutine.
func (o *Orchestrator) runVerifyChecks(ctx context.Context, wsPath string, run verifyRun, checks []config.VerifyCheck) verifyVerdict {
	var v verifyVerdict
	att := o.attestation.Load()
	iso := agent.IsolationFrom(ctx)
	stamp := func(r *attest.Record) {
		r.Issue, r.RunID, r.Profile = run.issue.Identifier, run.runID, run.profile
		if att != nil {
			if err := att.signer.Sign(r); err != nil {
				v.Failed = append(v.Failed, fmt.Sprintf("could not sign the record (%v)", err))
			}
		}
	}

	for _, c := range checks {
		spec := attest.Spec{
			Name:         c.Name,
			Shell:        c.Run,
			Timeout:      time.Duration(c.TimeoutMs) * time.Millisecond,
			TreeExcludes: verifyTreeExcludes,
		}
		if iso != nil {
			// The code under test runs confined like the agent that wrote
			// it: it cannot read the signing key.
			spec.Wrap = iso.WrapArgv
		}
		o.logVerify(run, "INFO", fmt.Sprintf("worker: verify `%s`: running %s", c.Name, c.Run))
		rec := attest.Run(ctx, wsPath, spec)
		stamp(&rec)
		v.Records = append(v.Records, rec)
		if ctx.Err() != nil {
			return v
		}
		if !rec.Passed() {
			v.Failed = append(v.Failed, verifyFailureReason(rec))
			break
		}
		o.logVerify(run, "INFO", fmt.Sprintf("worker: verify `%s`: passed in %dms", c.Name, rec.DurationMs))
	}

	seal := attest.NewSeal(ctx, wsPath, verifyTreeExcludes)
	stamp(&seal)
	if v.ok() && seal.TreeHash != "" {
		for _, r := range v.Records {
			if r.TreeHash != seal.TreeHash {
				v.Failed = append(v.Failed, fmt.Sprintf("`%s`: the working tree changed while the checks ran (the check ran on tree %s, the run ends on %s); a check must not modify tracked or untracked, non-ignored files", r.Name, shortHash(r.TreeHash), shortHash(seal.TreeHash)))
				break
			}
		}
	}
	v.Records = append(v.Records, seal)

	if att != nil {
		if err := att.store.Append(run.issue.Identifier, run.runID, v.Records...); err != nil {
			slog.Warn("worker: could not store signed check records",
				"issue_identifier", run.issue.Identifier, "error", err)
			v.Failed = append(v.Failed, fmt.Sprintf("the signed records could not be stored (%v)", err))
		}
	} else {
		slog.Warn("worker: verify ran without a signing key; records are not kept",
			"issue_identifier", run.issue.Identifier)
	}
	return v
}

// verifyFailureReason is one failed check, for the hold context: its name,
// exit code (or timeout / start error), command and the last output lines.
func verifyFailureReason(r attest.Record) string {
	var what string
	switch {
	case r.TimedOut:
		what = fmt.Sprintf("timed out after %dms and was killed", r.DurationMs)
	case r.ExitCode < 0 && r.Error != "":
		what = fmt.Sprintf("did not run to completion (exit code %d: %s)", r.ExitCode, r.Error)
	default:
		what = fmt.Sprintf("exited with code %d", r.ExitCode)
	}
	reason := fmt.Sprintf("`%s` %s (`%s`)", r.Name, what, r.Command)
	if tail := lastLines(r.OutputTail, 8); tail != "" {
		reason += "\n\n```\n" + tail + "\n```"
	}
	return reason
}

// lastLines is at most n trailing non-empty lines of s.
func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func shortHash(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	if h == "" {
		return "(unknown)"
	}
	return h
}

// verifyHoldContext is the input-required text for a run held by failed
// signed checks.
func verifyHoldContext(v verifyVerdict) string {
	return fmt.Sprintf("Signed checks failed (verify): Itervox ran them itself in the worktree after the agent finished.\n- %s\n\n"+
		"Ask the agent to fix the failure, then reply to resume; the checks run again when the run ends.",
		strings.Join(v.Failed, "\n- "))
}

// verifyCovers is the set of check names a profile's verify list runs: the
// agent's evidence file is not read for them.
func verifyCovers(checks []config.VerifyCheck) map[string]bool {
	out := make(map[string]bool, len(checks))
	for _, c := range checks {
		out[c.Name] = true
	}
	return out
}

// withoutVerified drops from required the names verify runs (the daemon's
// own run is their proof, never the agent's file).
func withoutVerified(required []string, checks []config.VerifyCheck) []string {
	if len(checks) == 0 {
		return required
	}
	covered := verifyCovers(checks)
	var out []string
	for _, r := range required {
		if !covered[r] {
			out = append(out, r)
		}
	}
	return out
}

func (o *Orchestrator) logVerify(run verifyRun, level, msg string) {
	if o.logBuf != nil {
		o.logBuf.Add(run.issue.Identifier, makeBufLineWithSession(level, msg, run.runLogID))
	}
}

// buildVerifyBlock tells the agent which checks Itervox runs itself after
// the run, so it does not spend turns recording them. Empty without verify.
func buildVerifyBlock(checks []config.VerifyCheck) string {
	if len(checks) == 0 {
		return ""
	}
	var names []string
	for _, c := range checks {
		names = append(names, fmt.Sprintf("`%s` (`%s`)", c.Name, c.Run))
	}
	return strings.Join([]string{
		"- signed checks: " + strings.Join(names, ", "),
		"",
		"Itervox runs these itself in this worktree when you finish, on the files as you leave them",
		"(committed or not), and the run is done only if each exits 0. Your own record of them does",
		"not count, so you need not write one; run them yourself first if you want to be sure.",
	}, "\n")
}

// buildRunEvidenceBlock is the prompt's "Evidence (required)" block for a
// run: the checks the agent must record (require_evidence minus verify)
// and the checks Itervox runs itself (verify).
func buildRunEvidenceBlock(required []string, checks []config.VerifyCheck, relPath string) string {
	block := buildEvidenceBlock(withoutVerified(required, checks), relPath)
	verifyBlock := buildVerifyBlock(checks)
	switch {
	case verifyBlock == "":
		return block
	case block == "":
		return "## Evidence (required)\n\n" + verifyBlock
	default:
		return block + "\n\n" + verifyBlock
	}
}
