package referee

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/vnovick/itervox/internal/procgroup"
)

// Limits for one probe run.
const (
	defaultProbeTimeout = 30 * time.Second
	maxProbeOutput      = 64 << 10 // bytes of output kept per run
	// probeWaitDelay bounds how long a run waits for its output pipe after
	// the tool exited (a child it left running holds the pipe) or after the
	// timeout killed its process group.
	probeWaitDelay = 2 * time.Second
)

// probeIdleWindow is how long a running probe may print nothing before it
// is taken to be waiting for input: its stdin is an open pipe nobody ever
// writes to, so a tool that asks a question blocks reading it. The probe's
// process group is then killed and the run recorded as waiting_for_input,
// well before the per-probe timeout. A variable so tests can shorten it.
var probeIdleWindow = 3 * time.Second

// noOutputPrompt stands for the prompt of a probe that waited for input
// without printing anything.
const noOutputPrompt = "(waiting for input; nothing was printed)"

// probeRun is what one probe did on one install.
type probeRun struct {
	Executable string `json:"executable"` // checkout-relative
	ExitCode   int    `json:"exit_code"`  // -1 when killed (a timeout, or waiting for input)
	TimedOut   bool   `json:"timed_out"`
	// WaitingForInput: the probe was still running and had printed nothing
	// for the idle window, blocked on its stdin (a pipe never written to),
	// so its process group was killed. Prompt is its last non-empty output
	// line (normalised and redacted), the question it was waiting on.
	WaitingForInput bool   `json:"waiting_for_input"`
	Prompt          string `json:"prompt"`
	LeftRunning     bool   `json:"left_running"` // it exited but left a process holding its output
	DurationMs      int64  `json:"duration_ms"`
	OutputBytes     int64  `json:"output_bytes"`
	Truncated       bool   `json:"output_truncated"` // only the first 64 KiB were compared

	lines []string // the normalised output
}

// installCandidates are the checkout-relative paths a tool's command is
// looked up at, in order. A tool installed under node_modules runs through
// its package manager's link (node_modules/.bin/<command>); any other
// install root holds the command at bin/<command> or at its top. The
// command is never looked up on PATH: both installs would resolve to the
// same program, and the diff would compare a tool with itself.
func installCandidates(install, command string) []string {
	if strings.HasPrefix(install+"/", "node_modules/") {
		return []string{"node_modules/.bin/" + command}
	}
	return []string{path.Join(install, "bin", command), path.Join(install, command)}
}

// resolveExecutable finds a tool's command in one checkout: an executable
// regular file (a symlink to one is fine) whose target stays inside the
// checkout. It returns the absolute path and the checkout-relative one.
func resolveExecutable(checkout, install, command string) (string, string, error) {
	realCheckout, err := filepath.EvalSymlinks(checkout)
	if err != nil {
		return "", "", err
	}
	if fi, err := os.Stat(filepath.Join(checkout, filepath.FromSlash(install))); err != nil || !fi.IsDir() {
		return "", "", fmt.Errorf("the install root %s is not a directory in %s (install the dependencies there first)", install, checkout)
	}
	candidates := installCandidates(install, command)
	for _, rel := range candidates {
		full := filepath.Join(checkout, filepath.FromSlash(rel))
		fi, err := os.Stat(full)
		if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm()&0o111 == 0 {
			continue
		}
		real, err := filepath.EvalSymlinks(full)
		if err != nil {
			continue
		}
		if r, err := filepath.Rel(realCheckout, real); err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
			return "", "", fmt.Errorf("%s in %s resolves outside the checkout (%s)", rel, checkout, real)
		}
		return full, rel, nil
	}
	return "", "", fmt.Errorf("no executable %s in %s", strings.Join(candidates, " or "), checkout)
}

// probeEnv is the whole environment a probe runs with. Nothing is
// inherited but PATH: HOME and the XDG and temp directories point into a
// fresh empty directory (no saved login, config or cache of the runner's
// user is visible), the locale and terminal are fixed, colour is off, and
// every variable the adapter treats as deploy-target is dropped even from
// this list. Credentials the runner holds (tokens, cloud keys, deploy
// keys) are therefore never passed.
func probeEnv(home, exeDir string, deployVar func(string) bool) []string {
	pathVar := os.Getenv("PATH")
	if pathVar == "" {
		pathVar = "/usr/bin:/bin"
	}
	vars := [][2]string{
		{"PATH", exeDir + string(os.PathListSeparator) + pathVar},
		{"HOME", home},
		{"XDG_CONFIG_HOME", filepath.Join(home, ".config")},
		{"XDG_CACHE_HOME", filepath.Join(home, ".cache")},
		{"XDG_DATA_HOME", filepath.Join(home, ".local", "share")},
		{"XDG_STATE_HOME", filepath.Join(home, ".local", "state")},
		{"TMPDIR", filepath.Join(home, "tmp")},
		{"LANG", "C"},
		{"LC_ALL", "C"},
		{"TERM", "dumb"},
		{"NO_COLOR", "1"},
	}
	out := make([]string, 0, len(vars))
	for _, kv := range vars {
		if !deployVar(kv[0]) {
			out = append(out, kv[0]+"="+kv[1])
		}
	}
	return out
}

// envNames is the variable names of an environment list.
func envNames(env []string) []string {
	names := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		names = append(names, name)
	}
	return names
}

// runProbe runs argv (argv[0] is the tool's command, exe its resolved
// path) in checkout. Its stdin is an open pipe that itervox never writes
// to and closes only after the probe exited or was killed: a prompt is
// neither answered nor given end of input, so a tool that asks a question
// is seen waiting for the answer. A probe that is still running and has
// printed nothing for probeIdleWindow is taken to be waiting for input: its
// process group is killed and the run marked WaitingForInput. The timeout
// still bounds a probe that keeps printing. An error is a run that could
// not happen at all (it never started, or ctx ended).
//
// The output is normalised with paths (the checkout's) plus the run's own
// HOME as placeholders.
func runProbe(ctx context.Context, checkout, exe string, argv []string, timeout time.Duration, deployVar func(string) bool, paths []pathPlaceholder) (probeRun, error) {
	home, err := os.MkdirTemp("", "referee-diff-home-")
	if err != nil {
		return probeRun{}, err
	}
	defer func() { _ = os.RemoveAll(home) }()
	paths = append(withRealPath(home, "<home>"), paths...)
	if err := os.Mkdir(filepath.Join(home, "tmp"), 0o700); err != nil {
		return probeRun{}, err
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, exe, argv[1:]...)
	cmd.Args[0] = argv[0]
	cmd.Dir = checkout
	// The write end is held, never written to, and closed only once the
	// probe is gone (os/exec also closes it after Wait).
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return probeRun{}, fmt.Errorf("stdin pipe for %s: %w", strings.Join(argv, " "), err)
	}
	defer func() { _ = stdin.Close() }()
	idle := &idleKill{}
	out := &cappedOutput{max: maxProbeOutput, onWrite: idle.reset}
	// One writer for both streams: os/exec then gives the child a single
	// pipe, so their interleaving is the order the tool wrote them in.
	cmd.Stdout = out
	cmd.Stderr = out
	cmd.Env = probeEnv(home, filepath.Dir(exe), deployVar)
	procgroup.Configure(cmd, probeWaitDelay)

	start := time.Now()
	if err := procgroup.Start(cmd); err != nil {
		return probeRun{}, fmt.Errorf("start %s: %w", strings.Join(argv, " "), err)
	}
	idle.start(cmd.Process.Pid, probeIdleWindow)
	waitErr := cmd.Wait()
	waited := idle.stop()
	_ = stdin.Close()
	// A process the tool started and left behind is still in its group:
	// kill it too (a no-op when the group is already empty).
	_ = procgroup.Kill(cmd.Process.Pid)
	run := probeRun{
		DurationMs:  time.Since(start).Milliseconds(),
		ExitCode:    cmd.ProcessState.ExitCode(),
		LeftRunning: errors.Is(waitErr, exec.ErrWaitDelay),
	}
	if ctx.Err() != nil {
		return probeRun{}, fmt.Errorf("%s: %w", strings.Join(argv, " "), ctx.Err())
	}
	switch {
	case errors.Is(runCtx.Err(), context.DeadlineExceeded):
		run.TimedOut, run.LeftRunning = true, false
	case waited && run.ExitCode == -1:
		// Killed by the idle window (not a tool that happened to exit
		// on its own just as the window ran out).
		run.WaitingForInput, run.LeftRunning = true, false
	}
	var raw []byte
	raw, run.OutputBytes, run.Truncated = out.result()
	run.lines = normalizeOutput(raw, paths)
	if run.WaitingForInput {
		run.Prompt = lastPromptLine(run.lines)
	}
	return run, nil
}

// lastPromptLine is the last non-empty line a probe printed before it was
// seen waiting for input, bounded and redacted, or noOutputPrompt.
func lastPromptLine(lines []string) string {
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			return redactAll(bounded(lines[i:i+1], 1))[0]
		}
	}
	return noOutputPrompt
}

// idleKill kills a probe's process group once it has printed nothing for
// the idle window. The timer is reset by every write of output; it fires
// on the runtime's timer goroutine and does nothing but the kill.
type idleKill struct {
	mu      sync.Mutex
	timer   *time.Timer
	window  time.Duration
	fired   bool // the window ran out while the probe was running
	stopped bool
}

func (k *idleKill) start(pgid int, window time.Duration) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.window = window
	k.timer = time.AfterFunc(window, func() {
		k.mu.Lock()
		defer k.mu.Unlock()
		if k.stopped || k.fired {
			return
		}
		k.fired = true
		_ = procgroup.Kill(pgid)
	})
}

// reset restarts the window: the probe printed something.
func (k *idleKill) reset() {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.timer != nil && !k.stopped && !k.fired {
		k.timer.Reset(k.window)
	}
}

// stop ends the watch once the probe is gone and reports whether the idle
// window killed it. A callback already running finishes first (it holds
// the lock), so no kill can follow stop.
func (k *idleKill) stop() bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.stopped = true
	if k.timer != nil {
		k.timer.Stop()
	}
	return k.fired
}

// withRealPath is p and, when it differs, p with its symlinks resolved
// (macOS temp directories live under /private), both standing for
// placeholder, the longer first so it is replaced first.
func withRealPath(p, placeholder string) []pathPlaceholder {
	out := []pathPlaceholder{{p, placeholder}}
	if real, err := filepath.EvalSymlinks(p); err == nil && real != p {
		out = append(out, pathPlaceholder{real, placeholder})
		if len(real) > len(p) {
			out[0], out[1] = out[1], out[0]
		}
	}
	return out
}

// cappedOutput keeps the first max bytes written to it and counts the rest.
// It never fails a write, so a tool that prints a lot is not blocked.
// Every write, kept or not, calls onWrite (output past the cap still
// shows the probe is not waiting).
type cappedOutput struct {
	mu      sync.Mutex
	max     int
	buf     []byte
	total   int64
	onWrite func()
}

func (c *cappedOutput) Write(p []byte) (int, error) {
	c.mu.Lock()
	c.total += int64(len(p))
	if room := c.max - len(c.buf); room > 0 {
		c.buf = append(c.buf, p[:min(room, len(p))]...)
	}
	c.mu.Unlock()
	if c.onWrite != nil && len(p) > 0 {
		c.onWrite()
	}
	return len(p), nil
}

func (c *cappedOutput) result() ([]byte, int64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf, c.total, c.total > int64(len(c.buf))
}
