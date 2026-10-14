package main

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/charmbracelet/x/term"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/procgroup"
)

// Login-shell PATH resolution (#119).
//
// A daemon started from Finder, launchd or the macOS menu-bar app inherits
// launchd's minimal PATH (/usr/bin:/bin:/usr/sbin:/sbin), so `claude` and
// `codex` installed through nvm, asdf, volta or Homebrew are not found. When
// the process was not started from an interactive shell — or when an agent
// CLI the workflow names is missing from the inherited PATH — the daemon asks
// the user's login shell for its PATH, once at startup and bounded by
// loginShellPATHTimeout, and installs the merged result as its own PATH, so
// every later lookup (backend validation, hooks, agent subprocesses) sees it.
// `itervox doctor` does the same and reports the source.

// loginShellPATHEnv overrides the automatic decision: 1/true/always forces
// the login-shell lookup, 0/false/never disables it, auto (or unset) decides.
const loginShellPATHEnv = "ITERVOX_LOGIN_SHELL_PATH"

// loginShellPATHTimeout bounds the whole login-shell lookup, rc files
// included. A var so tests can shorten it.
var loginShellPATHTimeout = 5 * time.Second

// loginShellPATHMarker brackets the PATH in the shell's output, so whatever
// an interactive rc file prints (banners, prompts, warnings) is ignored.
const loginShellPATHMarker = "__ITERVOX_LOGIN_SHELL_PATH__"

// maxLoginShellOutput bounds how much of the shell's stdout is kept.
const maxLoginShellOutput = 256 << 10

// PATH sources reported by pathResolution.Source.
const (
	pathSourceInherited  = "inherited"
	pathSourceLoginShell = "login_shell"
)

// pathResolution is the outcome of resolveLoginShellPATH.
type pathResolution struct {
	Source  string        // pathSourceInherited or pathSourceLoginShell
	Reason  string        // why the login shell was (not) consulted
	Shell   string        // the login shell consulted, when one was
	Elapsed time.Duration // time spent in the login shell
	Added   int           // directories the login shell contributed
	Err     string        // why a consulted login shell yielded nothing
	Missing []string      // agent CLIs still not on PATH afterwards
	PATH    string        // the resulting PATH
}

// String renders the resolution as the one line doctor prints.
func (r pathResolution) String() string {
	switch {
	case r.Source == pathSourceLoginShell:
		return fmt.Sprintf("login shell (%s, %s, +%d dirs) — %s", r.Shell, r.Elapsed.Round(time.Millisecond), r.Added, r.Reason)
	case r.Err != "":
		return fmt.Sprintf("inherited — %s; login shell %s failed: %s", r.Reason, r.Shell, r.Err)
	default:
		return "inherited — " + r.Reason
	}
}

// loginShellPATHMode parses ITERVOX_LOGIN_SHELL_PATH: "auto", "always" or
// "never". Anything unrecognised is auto.
func loginShellPATHMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "always", "on":
		return "always"
	case "0", "false", "no", "never", "off":
		return "never"
	default:
		return "auto"
	}
}

// shouldUseLoginShellPATH is the pure decision: consult the login shell when
// forced, never when disabled, and otherwise only when the process was not
// started from an interactive shell or a needed agent CLI is missing.
func shouldUseLoginShellPATH(mode string, interactive bool, missing []string) (bool, string) {
	switch {
	case mode == "never":
		return false, loginShellPATHEnv + "=never"
	case mode == "always":
		return true, loginShellPATHEnv + "=always"
	case !interactive:
		return true, "not started from an interactive shell"
	case len(missing) > 0:
		return true, "not on the inherited PATH: " + strings.Join(missing, ", ")
	default:
		return false, "started from an interactive shell and every agent CLI is on PATH"
	}
}

// startedFromInteractiveShell reports whether this process looks like it was
// started from a shell session: a terminal on stdin or stderr, or SHLVL,
// which every common shell exports and launchd, Finder and systemd do not.
// A daemon backgrounded with nohup from a shell keeps SHLVL.
func startedFromInteractiveShell(getenv func(string) string, stdinTTY, stderrTTY bool) bool {
	return stdinTTY || stderrTTY || getenv("SHLVL") != ""
}

// agentCLINames returns the bare executable names of the default agent
// command and every enabled profile's command. Absolute and relative paths
// are skipped: they do not depend on PATH.
func agentCLINames(cfg *config.Config) []string {
	if cfg == nil {
		return []string{"claude"}
	}
	commands := []string{cfg.Agent.Command}
	for _, p := range cfg.Agent.Profiles {
		if config.ProfileEnabled(p) {
			commands = append(commands, p.Command)
		}
	}
	var names []string
	for _, c := range commands {
		c = strings.TrimSpace(c)
		start, end, ok := resolveCommandTokenSpan(c)
		if !ok {
			continue
		}
		name := strings.Trim(c[start:end], `'"`)
		if name == "" || strings.ContainsRune(name, '/') || slices.Contains(names, name) {
			continue
		}
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// missingOnPATH returns the names exec.LookPath cannot find on pathList.
func missingOnPATH(names []string, pathList string) []string {
	var missing []string
	for _, n := range names {
		if lookPathIn(n, pathList) == "" {
			missing = append(missing, n)
		}
	}
	return missing
}

// lookPathIn is exec.LookPath against an explicit PATH value.
func lookPathIn(name, pathList string) string {
	for _, dir := range filepath.SplitList(pathList) {
		if dir == "" {
			continue
		}
		p := filepath.Join(dir, name)
		if info, err := os.Stat(p); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return p
		}
	}
	return ""
}

// userLoginShell returns the user's login shell: $SHELL, else the account
// database (dscl on macOS, /etc/passwd elsewhere), else /bin/sh.
func userLoginShell(getenv func(string) string) string {
	if sh := strings.TrimSpace(getenv("SHELL")); sh != "" {
		return sh
	}
	user := cmp.Or(getenv("USER"), getenv("LOGNAME"))
	if runtime.GOOS == "darwin" && user != "" {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if out, err := exec.CommandContext(ctx, "dscl", ".", "-read", "/Users/"+user, "UserShell").Output(); err == nil {
			if _, sh, ok := strings.Cut(strings.TrimSpace(string(out)), ":"); ok && filepath.IsAbs(strings.TrimSpace(sh)) {
				return strings.TrimSpace(sh)
			}
		}
	}
	if f, err := os.Open("/etc/passwd"); err == nil {
		defer func() { _ = f.Close() }()
		uid := strconv.Itoa(os.Getuid())
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			fields := strings.Split(sc.Text(), ":")
			if len(fields) == 7 && fields[2] == uid && filepath.IsAbs(fields[6]) {
				return fields[6]
			}
		}
	}
	return "/bin/sh"
}

// loginShellPATH runs `<shell> -l -i -c` once, bounded by timeout, and
// returns the PATH it prints between two markers. Interactive (-i) because
// nvm and most version managers initialise in ~/.zshrc / ~/.bashrc, which a
// login-only shell does not read. The shell gets its own session (no
// controlling terminal, stdin /dev/null) and its whole process group is
// killed on the deadline, so an rc file that waits for input or starts a
// background job cannot hold startup.
func loginShellPATH(shell string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	script := "echo " + loginShellPATHMarker + "; printenv PATH; echo " + loginShellPATHMarker
	cmd := exec.CommandContext(ctx, shell, "-l", "-i", "-c", script)
	procgroup.Configure(cmd, time.Second)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // also a new group: Configure's Cancel still reaches it
	out := &cappedBuffer{max: maxLoginShellOutput}
	cmd.Stdout = out
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("timed out after %s", timeout)
	}
	path, ok := pathBetweenMarkers(out.Bytes())
	if !ok {
		if err != nil {
			return "", err
		}
		return "", errors.New("no PATH in the shell's output")
	}
	return path, nil
}

// pathBetweenMarkers extracts the line between the first two marker lines.
func pathBetweenMarkers(out []byte) (string, bool) {
	var lines []string
	inside := false
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == loginShellPATHMarker {
			if inside {
				path := strings.TrimSpace(strings.Join(lines, ""))
				return path, path != ""
			}
			inside = true
			continue
		}
		if inside {
			lines = append(lines, line)
		}
	}
	return "", false
}

// mergePATH joins two PATH values, first's directories first, dropping
// duplicates, empty and relative entries.
func mergePATH(first, second string) (merged string, fromSecond int) {
	var dirs []string
	add := func(list string, counting bool) {
		for _, d := range filepath.SplitList(list) {
			if d == "" || !filepath.IsAbs(d) || slices.Contains(dirs, d) {
				continue
			}
			dirs = append(dirs, d)
			if counting {
				fromSecond++
			}
		}
	}
	add(first, false)
	add(second, true)
	return strings.Join(dirs, string(os.PathListSeparator)), fromSecond
}

// resolvePATH decides and, when needed, consults the login shell. It does
// not touch the process environment.
//
// Precedence of the merged PATH: when the process was not started from a
// shell, the login shell's PATH comes first (it is what the user's terminal
// would use) and inherited-only directories follow; when an interactive
// start merely lacks a CLI, the inherited PATH keeps its order and the login
// shell only appends what it adds.
func resolvePATH(getenv func(string) string, interactive bool, needed []string, timeout time.Duration) pathResolution {
	inherited := getenv("PATH")
	missing := missingOnPATH(needed, inherited)
	use, reason := shouldUseLoginShellPATH(loginShellPATHMode(getenv(loginShellPATHEnv)), interactive, missing)
	res := pathResolution{Source: pathSourceInherited, Reason: reason, PATH: inherited, Missing: missing}
	if !use || runtime.GOOS == "windows" {
		return res
	}
	res.Shell = userLoginShell(getenv)
	started := time.Now()
	login, err := loginShellPATH(res.Shell, timeout)
	res.Elapsed = time.Since(started)
	if err != nil {
		res.Err = err.Error()
		return res
	}
	if interactive {
		res.PATH, res.Added = mergePATH(inherited, login)
	} else {
		merged, _ := mergePATH(login, inherited)
		_, res.Added = mergePATH(inherited, login)
		res.PATH = merged
	}
	res.Source = pathSourceLoginShell
	res.Missing = missingOnPATH(needed, res.PATH)
	return res
}

// activePATHResolution is what applyLoginShellPATH decided for this
// process; nil until it runs.
var (
	activePATHResolutionMu sync.Mutex
	activePATHResolution   *pathResolution
)

func currentPATHResolution() *pathResolution {
	activePATHResolutionMu.Lock()
	defer activePATHResolutionMu.Unlock()
	return activePATHResolution
}

// applyLoginShellPATH resolves PATH for the workflow's agent CLIs, installs
// it in the process environment and records it for doctor and the daemon
// registry. Run once per process, before anything looks up an agent CLI.
func applyLoginShellPATH(workflowPath string) pathResolution {
	cfg, err := config.Load(workflowPath)
	if err != nil {
		cfg = nil
	}
	res := resolvePATH(os.Getenv,
		startedFromInteractiveShell(os.Getenv, term.IsTerminal(os.Stdin.Fd()), term.IsTerminal(os.Stderr.Fd())),
		agentCLINames(cfg), loginShellPATHTimeout)
	if res.Source == pathSourceLoginShell {
		if err := os.Setenv("PATH", res.PATH); err != nil {
			res.Source, res.Err = pathSourceInherited, "set PATH: "+err.Error()
		}
	}
	activePATHResolutionMu.Lock()
	activePATHResolution = &res
	activePATHResolutionMu.Unlock()
	return res
}

// logPATHResolution logs the daemon's PATH decision: the source, why, and
// directory counts — never the PATH itself.
func logPATHResolution(res pathResolution) {
	attrs := []any{"source", res.Source, "reason", res.Reason}
	if res.Shell != "" {
		attrs = append(attrs, "shell", res.Shell, "elapsed", res.Elapsed.Round(time.Millisecond).String(), "added_dirs", res.Added)
	}
	if len(res.Missing) > 0 {
		attrs = append(attrs, "missing", res.Missing)
	}
	if res.Err != "" {
		slog.Warn("PATH: login shell lookup failed, keeping the inherited PATH", append(attrs, "error", res.Err)...)
		return
	}
	slog.Info("PATH resolved", attrs...)
}

// agentCLIReport resolves each agent CLI the workflow names on the current
// PATH, as "name: /abs/path" or "name: not found", for doctor.
func agentCLIReport(cfg *config.Config) (found, missing []string) {
	if cfg == nil {
		return nil, nil
	}
	for _, name := range agentCLINames(cfg) {
		if p, err := exec.LookPath(name); err == nil {
			found = append(found, name+": "+p)
		} else {
			missing = append(missing, name)
		}
	}
	return found, missing
}

// cappedBuffer keeps the first max bytes written and discards the rest
// while reporting success, so a chatty rc file cannot stall the shell.
type cappedBuffer struct {
	buf bytes.Buffer
	max int
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if room := c.max - c.buf.Len(); room > 0 {
		c.buf.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

func (c *cappedBuffer) Bytes() []byte { return c.buf.Bytes() }
