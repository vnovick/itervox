package agent

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"

	"github.com/vnovick/itervox/internal/config"
)

// Process-level agent isolation (#258).
//
// A profile with isolation "process" runs its agent CLI inside an operating
// system sandbox: bubblewrap (or Landlock when bwrap is missing or cannot
// create namespaces) on Linux, sandbox-exec on macOS. The sandbox lets the
// run write only to its workspace, the git directories that worktree needs,
// the temp directory and the agent CLI's own state, and hides a deny list of
// credentials and every other workspace. Network stays open: agents need
// their APIs, and the guardrail shims call the daemon over localhost.
//
// The runners never decide whether to sandbox. The worker resolves an
// *Isolation once per run (PrepareIsolation) and passes it in the turn's
// context (WithIsolation), the same way RunEnv travels; every local launch
// path builds its argv and hands it to agentCommand, which wraps it. With no
// Isolation in the context agentCommand is exactly exec.CommandContext, so
// isolation "none" launches byte-identical commands.

// ErrIsolationUnavailable reports that a run asked for process isolation and
// no sandbox mechanism works on this machine. The run must not start: there
// is no silent fallback to running unconfined.
var ErrIsolationUnavailable = errors.New("agent: process isolation unavailable")

// IsolationMechanism is one way of confining a subprocess. Each platform
// lists its mechanisms in preference order (platformIsolationMechanisms);
// tests inject their own to control availability.
type IsolationMechanism interface {
	// Name is the mechanism's display name ("bubblewrap", "landlock",
	// "sandbox-exec"), shown in the run log, the issue detail and doctor.
	Name() string
	// Available returns nil when the mechanism works on this machine, or
	// why it does not.
	Available() error
	// Wrap returns argv rewritten to run inside the sandbox policy p.
	Wrap(argv []string, p IsolationPolicy) ([]string, error)
}

// Isolation is a resolved sandbox for one run: the mechanism and the policy
// built for the run's workspace. A nil *Isolation means no sandbox.
type Isolation struct {
	Mechanism IsolationMechanism
	Policy    IsolationPolicy
}

// Name is the mechanism's name, or "" for a nil Isolation.
func (iso *Isolation) Name() string {
	if iso == nil || iso.Mechanism == nil {
		return ""
	}
	return iso.Mechanism.Name()
}

// Describe is the one-line summary the run log records: the mechanism and
// the policy it enforces.
func (iso *Isolation) Describe() string {
	if iso == nil {
		return "isolation: none"
	}
	return fmt.Sprintf("isolation: process via %s; write: %s; read-only re-exposed: %s; denied: %s; network: allowed",
		iso.Name(), listOrNone(iso.Policy.Write), listOrNone(iso.Policy.Read), listOrNone(iso.Policy.Deny))
}

func listOrNone(paths []string) string {
	if len(paths) == 0 {
		return "(none)"
	}
	return strings.Join(paths, ", ")
}

// wrap returns argv run inside the sandbox, with extraWrite (per-turn
// paths such as the CLI log directory) added to the writable list.
func (iso *Isolation) wrap(argv []string, extraWrite ...string) ([]string, error) {
	if iso.Mechanism == nil {
		return nil, fmt.Errorf("%w: no sandbox mechanism resolved for this run", ErrIsolationUnavailable)
	}
	p := iso.Policy
	if extra := resolveExisting(extraWrite); len(extra) > 0 {
		p.Write = dedupe(append(append([]string{}, p.Write...), extra...))
	}
	return iso.Mechanism.Wrap(argv, p)
}

type isolationKey struct{}

// WithIsolation returns ctx carrying iso for the runner. A nil iso leaves
// ctx unchanged.
func WithIsolation(ctx context.Context, iso *Isolation) context.Context {
	if iso == nil {
		return ctx
	}
	return context.WithValue(ctx, isolationKey{}, iso)
}

// IsolationFrom returns the Isolation ctx carries, nil when none.
func IsolationFrom(ctx context.Context) *Isolation {
	iso, _ := ctx.Value(isolationKey{}).(*Isolation)
	return iso
}

// agentCommand builds the exec.Cmd for a local agent launch from argv
// (argv[0] is the program). It is the single place every local launch path
// — direct exec and login shell, for claude and codex — goes through, so a
// sandbox covers all of them. Without an Isolation in ctx it is exactly
// exec.CommandContext(turnCtx, argv[0], argv[1:]...).
func agentCommand(ctx, turnCtx context.Context, argv []string, extraWrite ...string) (*exec.Cmd, error) {
	iso := IsolationFrom(ctx)
	if iso == nil {
		return exec.CommandContext(turnCtx, argv[0], argv[1:]...), nil
	}
	wrapped, err := iso.wrap(argv, extraWrite...)
	if err != nil {
		return nil, err
	}
	return exec.CommandContext(turnCtx, wrapped[0], wrapped[1:]...), nil
}

// refuseRemoteIsolation fails a turn that carries an Isolation but runs on
// an SSH worker: the only local process there is ssh, so the remote agent
// would run unconfined. Config load refuses the combination; this catches
// SSH hosts added at runtime.
func refuseRemoteIsolation(ctx context.Context, workerHost string) error {
	if workerHost == "" || IsolationFrom(ctx) == nil {
		return nil
	}
	return fmt.Errorf("%w: process isolation is not supported on SSH workers (host %s)", ErrIsolationUnavailable, workerHost)
}

// IsolateShellCommand wraps a POSIX shell command line for a launcher that
// runs it with `/bin/sh -c` (the tmux sessions, #178): the result runs
// `/bin/sh -c command` inside the sandbox. A nil iso returns command
// unchanged.
func IsolateShellCommand(iso *Isolation, command string) (string, error) {
	if iso == nil {
		return command, nil
	}
	wrapped, err := iso.wrap([]string{"/bin/sh", "-c", command})
	if err != nil {
		return "", err
	}
	quoted := make([]string, len(wrapped))
	for i, a := range wrapped {
		quoted[i] = ShellQuote(a)
	}
	return strings.Join(quoted, " "), nil
}

// PrepareIsolation resolves the sandbox for one run. mode "none" (or "")
// returns nil: no sandbox. mode "process" picks the first available
// mechanism through detect and builds the policy from in; when none is
// available it returns an error wrapping ErrIsolationUnavailable with the
// reason, and the caller must not start the run.
func PrepareIsolation(mode string, in IsolationInputs, detect func() (IsolationMechanism, error)) (*Isolation, error) {
	switch mode {
	case "", config.IsolationNone:
		return nil, nil
	case config.IsolationProcess:
	default:
		return nil, fmt.Errorf("%w: unknown isolation %q", ErrIsolationUnavailable, mode)
	}
	if detect == nil {
		detect = DetectIsolation
	}
	m, err := detect()
	if err != nil {
		return nil, err
	}
	return &Isolation{Mechanism: m, Policy: BuildIsolationPolicy(in)}, nil
}

var (
	detectOnce sync.Once
	detected   IsolationMechanism
	detectErr  error
)

// DetectIsolation returns the first available mechanism on this machine,
// probing once per process. The error wraps ErrIsolationUnavailable and
// names why each mechanism was rejected.
func DetectIsolation() (IsolationMechanism, error) {
	detectOnce.Do(func() {
		detected, detectErr = SelectIsolationMechanism(platformIsolationMechanisms())
	})
	return detected, detectErr
}

// SelectIsolationMechanism returns the first of ms whose Available is nil;
// otherwise an error wrapping ErrIsolationUnavailable with every reason.
func SelectIsolationMechanism(ms []IsolationMechanism) (IsolationMechanism, error) {
	var reasons []string
	for _, m := range ms {
		err := m.Available()
		if err == nil {
			return m, nil
		}
		reasons = append(reasons, m.Name()+": "+err.Error())
	}
	if len(reasons) == 0 {
		return nil, fmt.Errorf("%w: no sandbox mechanism is supported on this operating system", ErrIsolationUnavailable)
	}
	return nil, fmt.Errorf("%w: %s", ErrIsolationUnavailable, strings.Join(reasons, "; "))
}

// IsolationMechanismReport lists every mechanism this platform knows with
// its availability (nil error = available), probed afresh. For doctor.
func IsolationMechanismReport() []IsolationMechanismStatus {
	var out []IsolationMechanismStatus
	for _, m := range platformIsolationMechanisms() {
		out = append(out, IsolationMechanismStatus{Name: m.Name(), Err: m.Available()})
	}
	return out
}

// IsolationMechanismStatus is one row of IsolationMechanismReport.
type IsolationMechanismStatus struct {
	Name string
	Err  error
}
