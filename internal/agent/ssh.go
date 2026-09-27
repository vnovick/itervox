package agent

import (
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"sync"

	"github.com/vnovick/itervox/internal/config"
)

// sshStrictHostMu guards strictHostDefault and strictHostByHost. The orchestrator
// updates these at startup and on config reload (under cfgMu); runners read
// them on every SSH command construction.
var (
	sshStrictHostMu   sync.RWMutex
	strictHostDefault = DefaultSSHStrictHostMode
	strictHostByHost  map[string]string
)

// DefaultSSHStrictHostMode is the StrictHostKeyChecking mode used when
// WORKFLOW.md does not set agent.ssh_strict_host_checking: TOFU — pin on
// first contact, reject on mismatch.
const DefaultSSHStrictHostMode = "accept-new"

// SSHStrictHostDefault returns the current default StrictHostKeyChecking
// mode (the one applied to hosts without a per-host override).
func SSHStrictHostDefault() string {
	sshStrictHostMu.RLock()
	defer sshStrictHostMu.RUnlock()
	return strictHostDefault
}

// SetSSHStrictHostDefault configures the default StrictHostKeyChecking mode
// applied to every SSH-hosted runner command unless overridden per-host.
//
// Valid modes (per `man ssh_config`):
//   - "accept-new" — TOFU; pin on first contact, reject on mismatch (default; recommended)
//   - "yes"        — strict; reject any unknown or changed host key
//   - "no" / "off" — permissive; accept any host key (insecure; matches pre-T-32 behavior)
//   - "ask"        — interactive; prompt on first contact (incompatible with BatchMode)
//
// Unknown mode strings are ignored and logged at WARN (CORE-140); the set of
// valid modes is config.IsValidSSHStrictHostMode, and config.ValidateDispatch
// rejects an invalid WORKFLOW.md value before it reaches this setter.
func SetSSHStrictHostDefault(mode string) {
	if !config.IsValidSSHStrictHostMode(mode) {
		sshStrictHostMu.RLock()
		current := strictHostDefault
		sshStrictHostMu.RUnlock()
		slog.Warn("ssh: ignoring invalid StrictHostKeyChecking default; keeping the current mode",
			"mode", mode, "current", current)
		return
	}
	sshStrictHostMu.Lock()
	strictHostDefault = mode
	sshStrictHostMu.Unlock()
}

// SetSSHStrictHostOverrides replaces the per-host override map. A nil or
// empty map clears all overrides (every host falls back to the default).
// An entry with an invalid mode is dropped and logged at WARN (CORE-140);
// that host falls back to the default.
func SetSSHStrictHostOverrides(byHost map[string]string) {
	cleaned := make(map[string]string, len(byHost))
	for host, mode := range byHost {
		if !config.IsValidSSHStrictHostMode(mode) {
			slog.Warn("ssh: ignoring invalid per-host StrictHostKeyChecking mode; host uses the default",
				"host", host, "mode", mode)
			continue
		}
		cleaned[host] = mode
	}
	sshStrictHostMu.Lock()
	strictHostByHost = cleaned
	sshStrictHostMu.Unlock()
}

// sshStrictHostMode returns the StrictHostKeyChecking mode for the given host,
// preferring per-host override over the package default.
func sshStrictHostMode(host string) string {
	sshStrictHostMu.RLock()
	defer sshStrictHostMu.RUnlock()
	if mode, ok := strictHostByHost[host]; ok {
		return mode
	}
	return strictHostDefault
}

// sshStrictHostOption returns the `-o StrictHostKeyChecking=<mode>` argv pair
// for the given host. Use as `cmd.Args = append(cmd.Args, sshStrictHostOption(host)...)`
// or splice into an exec.Command(...) call.
func sshStrictHostOption(host string) []string {
	return []string{"-o", "StrictHostKeyChecking=" + sshStrictHostMode(host)}
}

// remoteKillGraceSeconds is how long the remote watcher waits, after
// sending SIGTERM to the agent's process group, before it sends SIGKILL
// (CORE-155).
const remoteKillGraceSeconds = 2

// remotePayloadReadSeconds is the explicit timeout on reading the script
// from stdin, so a login profile's (readonly) TMOUT cannot cut it short.
const remotePayloadReadSeconds = 3600

// remoteBashInvocation returns how to run script under `bash <flags>` on an
// SSH worker: the ONE ssh remote-command argument, and the payload that
// must be written to the ssh process's stdin — through attachRemoteStdin,
// which keeps that stdin open until the ssh process exits (M1-B2 fix round
// 2, I1 + I2; M1-B9, CORE-155).
//
// Why not put the script in the argument (fix round 1): OpenSSH joins the
// remote-command arguments into one string and sshd runs it with the remote
// user's login shell (`$SHELL -c`), so anything in that string is parsed by
// a shell we do not control. Single-quoting it is not enough: in csh/tcsh a
// newline ends a single-quoted string (every later prompt line ran as a
// command — prompt-to-RCE from issue text) and `!` triggers history
// expansion. It also cost size: the prompt, already quoted inside the
// script, was quoted again, so each ' or \ grew 16x and ~45 KB of code-like
// prompt exceeded Linux's 128 KiB per-argument limit (MAX_ARG_STRLEN, which
// applies to the local ssh argv AND to sshd's `$SHELL -c` exec). Base64 in
// the argument would fix the parsing but not the size (4/3x of a 200 KiB
// prompt is still over 128 KiB), so the script travels out of band.
//
// Transport: the argument is always exactly
//
//	bash <flags> 'set +vx; o=$-; p=; shopt -oq pipefail && p=1; set +aeuET +o pipefail; trap - ERR DEBUG RETURN; LC_ALL=C IFS= read -t 3600 -r -d "" s; [ "$(( $(printf %s "$s" | wc -c) ))" = <N+1> ] || { echo "..." >&2; exit 97; }; exec 3<&0 </dev/null; d=$; set -m; ( trap : HUP USR1 USR2 ALRM PIPE TERM; g=$(exec sh -c "echo ${d}PPID"); { trap "" HUP INT QUIT USR1 USR2 ALRM PIPE TERM; cat <&3 >/dev/null; command kill -0 "$g" || exit 0; command kill -TERM -- -"$g"; sleep 2; command kill -KILL -- -"$g"; } >/dev/null 2>&1 & exec 3<&-; case $o in *E*) set -E;; esac; case $o in *T*) set -T;; esac; [ -z "$p" ] || set -o pipefail; case $o in *u*) set -u;; esac; case $o in *e*) set -e;; esac; eval "$s" ); exit $?'
//
// and the payload is the script, a newline and a NUL.
//   - Shell-neutral argument: the only characters whose meaning INSIDE single
//     quotes differs across the login shells we verify (sh, bash 3.2 and 5,
//     zsh, dash, ksh, csh, tcsh) and fish are ' \ ! and newline (csh/tcsh:
//     newline ends the string, ! is history; fish: \\ and \' are escapes).
//     The wrapper contains none of them (checkRemoteWrapper enforces it), so
//     every one of those shells passes the same bytes to bash. fish is not
//     installed where this was verified; the argument is still safe for it
//     by construction, since it holds no \ or ' inside the quotes.
//   - No prompt byte is ever parsed by the login shell: bash reads the
//     script from stdin and runs exactly those bytes with `eval`, so the
//     script bash executes is byte-identical to the fix round 1 script
//     (`set -e; cd W; ...` and the pipefail/tee pipeline), plus a trailing
//     newline.
//   - Framing (CORE-155): the local side keeps ssh's stdin open for the whole
//     turn, so the script cannot be read to EOF. `read -d ""` reads up to the
//     NUL that ends the payload, one byte at a time on a pipe, so it never
//     consumes past it; LC_ALL=C makes it byte-exact (bash 5.3 in a UTF-8
//     locale drops bytes of invalid multibyte sequences). The script cannot
//     hold a NUL (rejectNULPrompt, round 3, m2). A `head -c <N>` framing was
//     rejected: a login profile that eats k bytes of stdin would leave head
//     waiting for k bytes that never come — a hang, where this fails closed
//     at once (below).
//   - Kill on disconnect (CORE-155; round 2 V1-V3): `exec 3<&0 </dev/null`
//     keeps the ssh channel's stdin on fd 3 and gives everything else
//     /dev/null. `set -m` makes the parenthesised job run in a process group
//     of its OWN (pgid = the job subshell's pid), in the foreground, so its
//     exit status is simply `$?` — no `$!` (not shell-neutral) and no
//     `jobs -p` (loses finished jobs on bash 3.2). Job control is off again
//     inside the job, so the agent, its pipelines and every descendant stay
//     in that group. The job learns its own pid, which is the group id,
//     without BASHPID (bash 4+): `d=$` holds a literal dollar sign, so
//     `$(exec sh -c "echo ${d}PPID")` runs `sh -c 'echo $PPID'` exec'd
//     straight from the job subshell, whose pid it prints. The job then
//     starts the watcher in the background (it stays in the group) and runs
//     the script with fd 3 closed.
//     The watcher is a background `{ ... }` that first ignores HUP, INT,
//     QUIT, USR1, USR2, ALRM, PIPE and TERM (round 4, D3: it shares the
//     agent's process group, so an agent or tool running `kill 0` or
//     `kill -USR1 0` reaches it; round 3's watcher died of those silently,
//     after which a cancel killed nothing), then runs `cat <&3 >/dev/null`.
//     cat inherits those ignored dispositions and blocks until EOF on fd 3:
//     it is an external program, so a login profile's (readonly) TMOUT
//     never applies (round 2, V1), there is no read timeout to tell apart
//     from EOF (round 3, N4: no $SECONDS or clock), and no SIGALRM can end
//     it early (round 4, D3: bash 3.2 implements `read -t` with SIGALRM, so
//     an ALRM sent to the group ended the round-3 `read -t` like a timeout,
//     which it could not tell from EOF, and the live turn was killed). Only
//     SIGKILL/SIGSTOP, which nothing can ignore, can end it otherwise — and
//     a SIGKILL sent to the whole group kills the agent too.
//     EOF means the local ssh client went away (turn cancel, daemon
//     shutdown, the CORE-001 group kill: the server closes the channel), or
//     the channel closed after a normal completion. On EOF the watcher
//     first checks the job (`kill -0 "$g"`): if it has already exited (the
//     wrapper reaps it before exiting, and only then does the channel
//     close) it exits without signalling anything, so a child the agent
//     deliberately left running survives a normal turn (V3: the watcher is
//     found by the group it lives in and the job pid it holds, not by a job
//     spec a profile's background job could take). Otherwise it (still
//     ignoring TERM)
//     sends SIGTERM to the agent's group (`kill -TERM -- -"$g"`), waits 2 s
//     and sends SIGKILL to it (itself included). It never signals anything
//     outside that group (V2):
//     not the wrapper, the login shell, the SSH server or another session —
//     which matters on Dropbear, whose per-connection server process and
//     every session channel on the connection share ONE process group, so
//     the round-1 `kill 0` killed the server and every sibling agent. The
//     group id cannot be reused while the watcher, a member, is alive.
//   - Signals to the job shell (round 4, D3): the job subshell — the shell
//     that runs the script — catches HUP, USR1, USR2, ALRM, PIPE and TERM
//     with a no-op trap, so a signal the agent sends to its own group does
//     not end the turn when the agent itself ignores it. A caught (not
//     ignored) signal is reset to its default in every program the script
//     starts, so the agent and its tools keep their own dispositions. The
//     cancel path is unchanged: the agent gets SIGTERM, and the job shell,
//     which survives it, goes with everything else at the SIGKILL 2 s
//     later.
//   - Profile options (round 3, N1; round 4, D1): the login profile runs
//     first and can switch on options that change control flow — errexit,
//     nounset, pipefail, errtrace/functrace and ERR/DEBUG/RETURN traps. The
//     wrapper records errexit, nounset, errtrace and functrace from `$-`
//     (`o=$-`) and pipefail with `shopt -oq pipefail` — no command
//     substitution: round 3's `o=$(set +o)` ran in one, where bash turns
//     errexit off, so the script silently lost the profile's `set -e`. It
//     then clears those options and the traps for its own code and the
//     watcher, and turns the recorded options back on inside the job right
//     before the script (errexit last), so the script runs under the
//     profile's shell options as it always has, minus tracing (round 3,
//     m1). Options the wrapper never changes (noclobber, noglob, ...) are
//     simply inherited, except allexport (M2-close): `set +a` is cleared
//     with the others and deliberately NOT restored for the script. Under a
//     profile's `set -a` every assignment becomes an environment variable —
//     the wrapper's `s` (the whole script) and the script's prompt variable —
//     so the next exec failed with E2BIG ("Argument list too long", from
//     128 KiB on Linux) and the wrapper exited 97 blaming a stdin-reading
//     profile, and a smaller prompt leaked into the environment of the agent
//     and every tool it ran. The script itself is generated by itervox and
//     relies on no implicit export; variables the profile assigned while
//     allexport was on are already exported and stay so. `command` in front of kill (and the former read)
//     bypasses shell functions a profile might define with those names;
//     the pid is quoted against an unusual IFS. `sleep` and `cat` are NOT
//     prefixed: under bash 3.2 the watcher disappeared during
//     `command sleep 2` and never sent the SIGKILL, so a TERM-ignoring agent
//     survived (the bin_bash ignores_term rows; its xtrace ends at
//     `sleep 2`; most likely bash 3.2 exec'd the sleep in place of the
//     watcher). A plain external command does not do this.
//     On a normal exit the wrapper exits with the job's status: 0, the
//     agent's code, or codex's pipefail/tee status, unchanged. The watcher's
//     stdout/stderr are /dev/null, so it never holds the channel open.
//     Rejected alternatives: a PTY (ssh -t) merges stdout/stderr and
//     translates CRLF (see -T below); setsid(1) is not on macOS; `kill 0`
//     (round 1) relies on the server putting each session in its own group,
//     which Dropbear does not; walking `ps` ppid links to kill descendants
//     misses any that re-parented to init and races with forks, where a
//     process group does neither. Limit: a descendant that leaves the
//     agent's process group (setsid, its own job control) escapes the group
//     kill — the same bound as CORE-001's local group kill.
//   - No trace of the script (round 3, m1): a login profile (or BASH_ENV)
//     that runs `set -x` / `set -v` would make bash trace the `s=` read,
//     the printf and the eval — copies of a script that can hold secrets
//     from agent.command — into stderr, and stderr flows through
//     resolveFailureText into FailureText, slog, the log buffer and the
//     dashboard. `set +vx` runs first, before any script byte is read. The
//     login shell's own trace (or verbose echo) of this -c line is harmless:
//     the wrapper is constants and integers only.
//   - Integrity: `bash -l` runs the login profile BEFORE the -c command; a
//     profile that reads stdin would eat the start of the payload, and
//     evaluating the remainder could start mid-prompt. The wrapper compares
//     the received byte count with <N+1> (the script and its newline) and
//     refuses to run on a mismatch (exit 97). The newline before the NUL
//     makes a profile that reads one line stop at or before it, so the check
//     still fires; only a profile that reads stdin to EOF now hangs until
//     the turn is cancelled, where it used to fail with exit 97.
//   - Size: the argument is well under 1 KiB whatever the prompt, and no
//     process on the worker gets the prompt in its argv (CORE-156): the
//     script hands it to the agent CLI on stdin (remotePromptRedirect), so
//     Linux's per-argument MAX_ARG_STRLEN (131072 bytes) and ARG_MAX no
//     longer cap the prompt. The former Linux-only refusal (exit 98) is
//     gone with the argument it guarded. What remains in argv — flags, the
//     session id, the workspace and log paths — is operator-sized.
//   - Requirements on the worker: bash (3.2 or later: `read -d`/`-t`,
//     `set -m`, `kill -- -PGID`, process substitution with /dev/fd or a
//     writable TMPDIR for its FIFO), sh, cat, printf, wc and sleep (POSIX
//     userland); no base64, setsid, head, ps, mktemp or uname. Any SSH server (OpenSSH,
//     Dropbear): the wrapper creates its own process group rather than
//     relying on the server's. ssh must not allocate a PTY for
//     stdin: every call site passes -T (round 3, m3), so even ssh_config
//     `RequestTTY force` cannot put the payload through a terminal line
//     discipline (echo, CRLF) or corrupt the sublog tar stream. Without a
//     PTY sshd sends no SIGHUP when the client dies; the watcher above is
//     what stops the remote agent.
//   - Detection bound: EOF reaches the watcher as soon as sshd notices the
//     client is gone. A killed client closes its TCP connection, so that is
//     immediate; a network partition is noticed only by sshd's
//     ClientAliveInterval/TCP keepalive.
func remoteBashInvocation(flags, script string) (string, string) {
	want := len(script) + 1 // the script and the newline before the NUL
	wrapper := fmt.Sprintf(`set +vx; o=$-; p=; shopt -oq pipefail && p=1; set +aeuET +o pipefail; trap - ERR DEBUG RETURN; `+
		`LC_ALL=C IFS= read -t %d -r -d "" s; [ "$(( $(printf %%s "$s" | wc -c) ))" = %d ] || { echo "itervox: remote command arrived truncated (expected %d bytes); does a login profile read stdin?" >&2; exit 97; }; `,
		remotePayloadReadSeconds, want, want)
	wrapper += fmt.Sprintf(`exec 3<&0 </dev/null; d=$; set -m; ( trap : HUP USR1 USR2 ALRM PIPE TERM; g=$(exec sh -c "echo ${d}PPID"); `+
		`{ trap "" HUP INT QUIT USR1 USR2 ALRM PIPE TERM; cat <&3 >/dev/null; `+
		`command kill -0 "$g" || exit 0; command kill -TERM -- -"$g"; sleep %d; command kill -KILL -- -"$g"; } >/dev/null 2>&1 & `+
		`exec 3<&-; case $o in *E*) set -E;; esac; case $o in *T*) set -T;; esac; [ -z "$p" ] || set -o pipefail; `+
		`case $o in *u*) set -u;; esac; case $o in *e*) set -e;; esac; eval "$s" ); exit $?`,
		remoteKillGraceSeconds)
	checkRemoteWrapper(wrapper)
	return "bash " + flags + " '" + wrapper + "'", script + "\n\x00"
}

// remotePromptVar is the remote script's variable that holds the prompt.
// Its name cannot collide with the wrapper's own variables (s o p d g).
const remotePromptVar = "itervox_prompt"

// remotePromptAssignment is the statement, placed in the remote script
// before the agent command, that sets remotePromptVar to the prompt (single-
// quoted by ShellQuote, parsed by the remote bash only).
func remotePromptAssignment(prompt string) string {
	return remotePromptVar + "=" + ShellQuote(prompt) + "; "
}

// remotePromptRedirect is appended to the agent CLI's command in the remote
// script: it puts the prompt on the CLI's stdin (CORE-156).
//
// The prompt already travels byte-exact inside the script (the NUL-framed
// ssh stdin payload, counted against exit 97), so nothing new crosses the
// wire. bash's builtin printf writes it into a pipe from a process
// substitution: no exec, so no MAX_ARG_STRLEN; `builtin` bypasses a shell
// function a login profile might define as printf; LC_ALL=C is defence in
// depth for invalid multibyte sequences (bash 3.2 and 5.3 passed them
// byte-exact without it too, in UTF-8 and C locales). The prompt is expanded
// from remotePromptVar rather than written literally inside `<( )`: bash
// 3.2 re-parses the text of a process substitution and doubles its
// internal quoting bytes, so a literal 0x01 or 0x7f reached the agent as
// 0x01 0x01 / 0x01 0x7f (measured); a variable expansion is not re-parsed.
// The CLI reads the prompt and then EOF — the writer exits when done — so
// it never sees the ssh channel (fd 3, the watcher's) or waits on it;
// everything else in the script keeps the wrapper's /dev/null stdin. The
// writer is an ordinary member of the agent's process group, so a cancel
// kills it with the agent even when it is blocked on a pipe the agent
// never read, and its status is not part of any pipeline (codex's
// pipefail/tee status is unchanged).
// Rejected: a remote temp file needs mktemp and a cleanup that the SIGKILL
// escalation cannot run; a here-document or here-string appends a newline
// and would need a delimiter the prompt cannot contain; `printf | agent`
// would put the writer's SIGPIPE status into codex's pipefail pipeline.
const remotePromptRedirect = ` < <(LC_ALL=C builtin printf %s "$` + remotePromptVar + `")`

// remoteStdin feeds an ssh process its stdin payload and then holds the
// pipe open until the process exits (CORE-155): the remote wrapper treats
// EOF on that stdin as "the client is gone" and kills the agent's process
// group. cmd.Wait closes the pipe (exec.Cmd.StdinPipe), whether the process
// exited by itself or was killed by the CORE-001 group kill, so the write
// end is never leaked and a write still blocked at that point returns.
type remoteStdin struct {
	w       io.WriteCloser
	payload string
	done    chan struct{}
}

// attachRemoteStdin wires payload to cmd's stdin; call it before cmd.Start,
// then start after a successful Start and join after cmd.Wait.
func attachRemoteStdin(cmd *exec.Cmd, payload string) (*remoteStdin, error) {
	w, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("agent: ssh stdin pipe: %w", err)
	}
	return &remoteStdin{w: w, payload: payload}, nil
}

// start writes the payload in the background (it can exceed the pipe
// buffer, and ssh drains it only as the remote side reads). It never closes
// the pipe: that is cmd.Wait's job. A nil receiver is a no-op (local turns).
func (r *remoteStdin) start() {
	if r == nil {
		return
	}
	r.done = make(chan struct{})
	go func() {
		defer close(r.done)
		// A failed write means ssh or the remote side is already gone; the
		// turn's exit status (97 for a short payload, 255 for ssh) reports it.
		_, _ = io.WriteString(r.w, r.payload)
	}()
}

// join waits for the payload writer; call it after cmd.Wait, which closed
// the pipe, so a writer still blocked on it has returned or is returning.
func (r *remoteStdin) join() {
	if r == nil || r.done == nil {
		return
	}
	<-r.done
}

// checkRemoteWrapper panics if the wrapper holds a character whose meaning
// inside single quotes differs between login shells. The wrapper is built
// only from constants and integers, so this is a programming-error guard.
func checkRemoteWrapper(w string) {
	if strings.ContainsAny(w, "'\\!\n\r") {
		panic("agent: remote wrapper contains a shell-dependent character: " + w)
	}
}
