// Package attest runs a check command as the daemon and records what it
// saw, signed, so "done" rests on a run the agent did not write (#attest:
// the writer is never the judge).
//
// A [Record] holds the command, its real exit code, when it ran and for how
// long, the SHA-256 of its combined output, a bounded and redacted output
// tail, the hash of the working tree it ran on (tracked plus untracked,
// non-ignored files, computed with a throwaway index so the user's index is
// never touched) and the HEAD commit. [Signer] signs records with ed25519;
// [Verify] checks a signature and that the tree is the one expected;
// [Store] keeps one JSONL file per issue run.
//
// Shell commands run as `bash -lc` with `set -o pipefail` first, so
// `make test | tail -n 50` records make's failure rather than tail's
// success. An argv command runs with no shell at all.
//
// The package imports only the standard library and the leaf packages
// gitexec (the only way to run git), logging (RedactString) and procgroup
// (the shared process-group kill), so orchestrator and cmd/itervox can both
// use it.
package attest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"hash"
	"os/exec"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/vnovick/itervox/internal/logging"
	"github.com/vnovick/itervox/internal/procgroup"
)

// RecordVersion is the record format this build writes and verifies.
const RecordVersion = 1

// DefaultTimeout bounds a check that names no timeout.
const DefaultTimeout = 10 * time.Minute

// TailBytes bounds the output tail kept in a record.
const TailBytes = 4096

// Record kinds.
const (
	// KindCheck is one check command's run.
	KindCheck = "check"
	// KindSeal closes a run: the tree after its last check. A check record
	// is verified against its run's seal.
	KindSeal = "seal"
)

// pipefailPrefix is prepended to every shell command: bash's pipefail makes
// a pipeline's status the last non-zero status of any stage, so a failing
// command piped into tail or tee is not masked.
const pipefailPrefix = "set -o pipefail\n"

// Record is one signed observation. Every field but Sig is covered by the
// signature (see [Signer.Sign]).
type Record struct {
	Version int    `json:"v"`
	Kind    string `json:"kind"`
	// Issue and RunID say which issue run the record belongs to; Profile
	// is the agent profile whose run it judged.
	Issue   string `json:"issue,omitempty"`
	RunID   string `json:"run_id,omitempty"`
	Profile string `json:"profile,omitempty"`
	// Name is the check's name (a check record).
	Name string `json:"name,omitempty"`
	// Command is the configured command line (redacted); Argv what was
	// executed, before any sandbox wrapper. Shell and Pipefail say how.
	Command  string   `json:"command,omitempty"`
	Argv     []string `json:"argv,omitempty"`
	Shell    bool     `json:"shell,omitempty"`
	Pipefail bool     `json:"pipefail,omitempty"`
	// ExitCode is the process's exit status; -1 when it did not exit
	// normally (killed, timed out, could not start).
	ExitCode   int       `json:"exit_code"`
	TimedOut   bool      `json:"timed_out,omitempty"`
	Error      string    `json:"error,omitempty"`
	StartedAt  time.Time `json:"started_at"`
	DurationMs int64     `json:"duration_ms"`
	// OutputSHA256 is the hex SHA-256 of the complete combined
	// stdout+stderr; OutputTail its last TailBytes, redacted.
	OutputSHA256 string `json:"output_sha256,omitempty"`
	OutputTail   string `json:"output_tail,omitempty"`
	// TreeHash is the git tree of the working tree when the check started
	// (see [TreeHash]), TreeExcludes the paths left out of it; Head the
	// HEAD commit. Empty outside a git work tree.
	TreeHash     string   `json:"tree_hash,omitempty"`
	TreeExcludes []string `json:"tree_excludes,omitempty"`
	Head         string   `json:"head,omitempty"`
	TreeError    string   `json:"tree_error,omitempty"`
	// KeyID names the signing key (the first 16 hex digits of the SHA-256
	// of the public key); Sig is the base64 ed25519 signature.
	KeyID string `json:"key_id,omitempty"`
	Sig   string `json:"sig,omitempty"`
}

// Passed reports whether a check record is a clean exit 0.
func (r Record) Passed() bool {
	return r.Kind == KindCheck && r.ExitCode == 0 && !r.TimedOut && r.Error == ""
}

// Spec is one check to run.
type Spec struct {
	// Name is the check's name.
	Name string
	// Shell is a command line run by bash with pipefail. Argv, when set
	// instead, is executed directly.
	Shell string
	Argv  []string
	// Timeout bounds the run (<= 0: DefaultTimeout); the whole process
	// group is killed when it passes.
	Timeout time.Duration
	// TreeExcludes are repository paths left out of the tree hash (the
	// daemon's own bookkeeping, which may change after the check).
	TreeExcludes []string
	// Wrap, when set, rewrites argv to run inside a sandbox (the run's
	// process isolation). The record keeps the unwrapped argv.
	Wrap func(argv []string) ([]string, error)
	// Env is appended to the daemon's scrubbed environment.
	Env []string
}

// Run runs spec in dir and returns its unsigned record. The tree hash and
// HEAD are taken just before the command starts. Run never fails: a
// command that cannot start records ExitCode -1 and the Error.
func Run(ctx context.Context, dir string, spec Spec) Record {
	rec := Record{
		Version:      RecordVersion,
		Kind:         KindCheck,
		Name:         spec.Name,
		TreeExcludes: append([]string(nil), spec.TreeExcludes...),
		ExitCode:     -1,
	}
	tree, err := TreeHash(ctx, dir, spec.TreeExcludes)
	if err != nil {
		rec.TreeError = logging.RedactString(err.Error())
	}
	rec.TreeHash = tree
	rec.Head = Head(ctx, dir)

	argv, err := commandArgv(spec)
	if spec.Shell != "" {
		rec.Command = logging.RedactString(spec.Shell)
		rec.Shell, rec.Pipefail = true, true
	} else {
		rec.Command = logging.RedactString(strings.Join(spec.Argv, " "))
	}
	for _, a := range argv {
		rec.Argv = append(rec.Argv, logging.RedactString(a))
	}
	rec.StartedAt = time.Now().UTC()
	if err != nil {
		rec.Error = err.Error()
		return rec
	}
	if spec.Wrap != nil {
		if argv, err = spec.Wrap(argv); err != nil {
			rec.Error = logging.RedactString("attest: sandbox: " + err.Error())
			return rec
		}
	}

	timeout := spec.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	out := newOutputSink()
	cmd := exec.CommandContext(runCtx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Stdout = out
	cmd.Stderr = out
	cmd.Env = append(gitEnviron(), spec.Env...)
	procgroup.Configure(cmd, 5*time.Second)

	start := time.Now()
	runErr := procgroup.Start(cmd)
	if runErr == nil {
		untrack := procgroup.Track(ctx, cmd)
		runErr = cmd.Wait()
		untrack()
	}
	rec.DurationMs = time.Since(start).Milliseconds()
	rec.OutputSHA256, rec.OutputTail = out.result()

	switch {
	case runErr == nil:
		rec.ExitCode = 0
	case errors.Is(runCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil:
		rec.TimedOut = true
		rec.Error = "attest: timed out after " + timeout.String()
	default:
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			rec.ExitCode = exitErr.ExitCode() // -1 when killed by a signal
			if rec.ExitCode < 0 {
				rec.Error = logging.RedactString(runErr.Error())
			}
		} else {
			rec.Error = logging.RedactString(runErr.Error())
		}
		if ctx.Err() != nil {
			rec.Error = "attest: cancelled"
		}
	}
	return rec
}

// commandArgv is the argv spec runs, before any sandbox wrapper.
func commandArgv(spec Spec) ([]string, error) {
	if strings.TrimSpace(spec.Shell) != "" {
		shell := procgroup.Shell("bash")
		if shell == "" {
			return nil, errors.New("attest: no bash found to run the check")
		}
		return []string{shell, "-lc", pipefailPrefix + spec.Shell}, nil
	}
	if len(spec.Argv) == 0 || spec.Argv[0] == "" {
		return nil, errors.New("attest: no command to run")
	}
	return append([]string(nil), spec.Argv...), nil
}

// outputSink hashes everything written to it and keeps the last TailBytes.
type outputSink struct {
	mu   sync.Mutex
	h    hash.Hash
	tail []byte
}

func newOutputSink() *outputSink { return &outputSink{h: sha256.New()} }

func (s *outputSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.h.Write(p)
	s.tail = append(s.tail, p...)
	if over := len(s.tail) - TailBytes; over > 0 {
		s.tail = append(s.tail[:0], s.tail[over:]...)
	}
	return len(p), nil
}

// result is the output's hex SHA-256 and its redacted, valid-UTF-8 tail.
func (s *outputSink) result() (string, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tail := s.tail
	// Drop a rune the TailBytes cut split in two.
	for len(tail) > 0 && !utf8.RuneStart(tail[0]) {
		tail = tail[1:]
	}
	text := strings.ToValidUTF8(string(bytes.TrimRight(tail, "\x00")), "�")
	return hex.EncodeToString(s.h.Sum(nil)), logging.RedactString(text)
}

// NewSeal returns the unsigned seal for a run: the tree in dir now, after
// its last check, with the same excludes the checks used.
func NewSeal(ctx context.Context, dir string, excludes []string) Record {
	rec := Record{
		Version:      RecordVersion,
		Kind:         KindSeal,
		TreeExcludes: append([]string(nil), excludes...),
		StartedAt:    time.Now().UTC(),
	}
	tree, err := TreeHash(ctx, dir, excludes)
	if err != nil {
		rec.TreeError = logging.RedactString(err.Error())
	}
	rec.TreeHash = tree
	rec.Head = Head(ctx, dir)
	return rec
}
