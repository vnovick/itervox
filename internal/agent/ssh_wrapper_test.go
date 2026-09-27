package agent

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// runWrapperUnderBash runs remoteBashInvocation's argument the way a POSIX
// login shell would (`/bin/sh -c <arg>`), feeding its payload on a stdin
// held open until it exits, as the SSH runners do (CORE-155), with extraPath
// first on PATH. Plain `bash -c` (no -l) keeps PATH intact.
func runWrapperUnderBash(t *testing.T, script string, extraPath string, extraEnv ...string) (string, int) {
	t.Helper()
	arg, payload := remoteBashInvocation("-c", script)
	cmd := exec.Command("/bin/sh", "-c", arg)
	cmd.Env = append(os.Environ(), "PATH="+extraPath+string(os.PathListSeparator)+os.Getenv("PATH"))
	cmd.Env = append(cmd.Env, extraEnv...)
	// Its own session with no controlling terminal, as sshd gives a -T
	// session: the wrapper's `set -m` job control must not touch go test's
	// terminal or process group.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	feed, err := attachRemoteStdin(cmd, payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	feed.start()
	err = cmd.Wait()
	feed.join()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	}
	return out.String(), code
}

// TestRemotePromptRedirectIsByteExact (CORE-156) replaces the Linux
// MAX_ARG_STRLEN refusal test: the refusal (exit 98) is gone because no
// argument carries the prompt any more. On a Linux worker (fake uname) the
// remote script hands a 1 MiB prompt of every byte but NUL — invalid UTF-8
// included — to the agent's stdin byte-exact, under the first bash on PATH
// and /bin/bash (3.2 on macOS), in a UTF-8 and a C locale, with the
// agent's stdin at EOF right after the prompt (cat returns).
func TestRemotePromptRedirectIsByteExact(t *testing.T) {
	fake := t.TempDir()
	if err := os.WriteFile(filepath.Join(fake, "uname"), []byte("#!/bin/sh\necho Linux\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewPCG(156, 1))
	b := make([]byte, 1<<20)
	for i := range b {
		b[i] = byte(1 + rng.IntN(255))
	}
	prompt := string(b)
	bashes := [][2]string{{"path_bash", fake}}
	if _, err := os.Stat("/bin/bash"); err == nil {
		bashes = append(bashes, [2]string{"bin_bash", fake + string(os.PathListSeparator) + "/bin"})
	}
	for _, wb := range bashes {
		for _, locale := range []string{"en_US.UTF-8", "C"} {
			t.Run(wb[0]+"/"+locale, func(t *testing.T) {
				out, code := runWrapperUnderBash(t, remotePromptAssignment(prompt)+"cat"+remotePromptRedirect+"; echo END", wb[1], "LANG="+locale, "LC_ALL="+locale)
				if code != 0 {
					t.Fatalf("exit %d; output %.300q", code, out)
				}
				if got := strings.TrimSuffix(out, "END\n"); got != prompt {
					i := 0
					for i < len(got) && i < len(prompt) && got[i] == prompt[i] {
						i++
					}
					t.Fatalf("prompt differs: want %d bytes, got %d, first difference at offset %d", len(prompt), len(got), i)
				}
			})
		}
	}
}

// TestRemoteBashInvocationRunsScriptVerbatim: the wrapper evaluates exactly
// the bytes it was given, preserves the script's exit status, and rejects a
// script whose byte count does not match (fails closed on truncation).
func TestRemoteBashInvocationRunsScriptVerbatim(t *testing.T) {
	script := `printf '%s|' "a'b" 'c\d' "$((6*7))"; exit 5`
	out, code := runWrapperUnderBash(t, script, "")
	if out != `a'b|c\d|42|` || code != 5 {
		t.Fatalf("got %q exit %d; want %q exit 5", out, code, `a'b|c\d|42|`)
	}

	arg, payload := remoteBashInvocation("-c", script)
	cmd := exec.Command("/bin/sh", "-c", arg)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdin = strings.NewReader(payload[3:]) // the first 3 bytes were eaten
	b, _ := cmd.CombinedOutput()
	if cmd.ProcessState.ExitCode() != 97 || !strings.Contains(string(b), "arrived truncated") {
		t.Fatalf("truncated script: exit %d, output %q; want exit 97", cmd.ProcessState.ExitCode(), b)
	}
}

// TestRemoteWrapperIsShellNeutral pins checkRemoteWrapper's contract
// independently of it (round 3, m4): across script lengths either side of
// every digit-count boundary and of the former Linux per-argument limit, the
// argument is exactly `bash <flags> '<wrapper>'`, starts with the fixed
// prefix (asserted, not TrimPrefix'd), carries the right byte count and no
// arg-limit refusal (CORE-156: nothing prompt-sized is an argument), and the
// wrapper never holds ' \ ! CR or LF — even when the script is full of them.
func TestRemoteWrapperIsShellNeutral(t *testing.T) {
	seed := time.Now().UnixNano()
	rng := rand.New(rand.NewPCG(uint64(seed), 0))
	t.Logf("random seed %d", seed)
	const hostile = "a'b\\c!d\ne\rf"
	script := func(n int) string {
		b := make([]byte, n)
		for i := range b {
			b[i] = hostile[rng.IntN(len(hostile))]
		}
		return string(b)
	}
	const linuxArgStrLen = 131072
	lengths := []int{0, 1, 9, 10, 99, 100, linuxArgStrLen - 1, linuxArgStrLen, 1 << 20, 1 + rng.IntN(1<<18)}
	const forbidden = "'\\!\n\r"
	for _, flags := range []string{"-lc", "-c"} {
		for _, n := range lengths {
			arg, payload := remoteBashInvocation(flags, script(n))
			prefix := "bash " + flags + ` 'set +vx; o=$-; p=; shopt -oq pipefail && p=1; set +aeuET +o pipefail; trap - ERR DEBUG RETURN; LC_ALL=C IFS= read -t 3600 -r -d "" s; `
			if !strings.HasPrefix(arg, prefix) {
				t.Fatalf("flags %s n=%d: argument %q does not start with %q", flags, n, arg, prefix)
			}
			if !strings.HasSuffix(arg, `exec 3<&-; case $o in *E*) set -E;; esac; case $o in *T*) set -T;; esac; [ -z "$p" ] || set -o pipefail; case $o in *u*) set -u;; esac; case $o in *e*) set -e;; esac; eval "$s" ); exit $?'`) {
				t.Fatalf("flags %s n=%d: argument %q does not end with the eval and its status", flags, n, arg)
			}
			if !strings.Contains(arg, `exec 3<&0 </dev/null; d=$; set -m; ( trap : HUP USR1 USR2 ALRM PIPE TERM; g=$(exec sh -c "echo ${d}PPID"); { trap "" HUP INT QUIT USR1 USR2 ALRM PIPE TERM; cat <&3 >/dev/null; command kill -0 "$g" || exit 0; command kill -TERM -- -"$g"; sleep 2; command kill -KILL -- -"$g"; } >/dev/null 2>&1 & `) {
				t.Fatalf("flags %s n=%d: argument %q lacks the kill-on-EOF watcher", flags, n, arg)
			}
			if strings.Contains(arg, "kill -TERM 0") || strings.Contains(arg, "kill -KILL 0") || strings.Contains(arg, "%1") {
				t.Fatalf("flags %s: the wrapper must signal only its own agent group, never group 0 or a job spec: %q", flags, arg)
			}
			inner := arg[len("bash "+flags+" '") : len(arg)-1]
			if strings.ContainsAny(inner, forbidden) {
				t.Fatalf("flags %s n=%d: wrapper holds a shell-dependent character: %q", flags, n, inner)
			}
			if want := fmt.Sprintf(`wc -c) ))" = %d ]`, n+1); !strings.Contains(inner, want) {
				t.Fatalf("flags %s n=%d: wrapper lacks the byte count %q: %q", flags, n, want, inner)
			}
			if strings.Contains(inner, "MAX_ARG_STRLEN") || strings.Contains(inner, "uname") || strings.Contains(inner, "exit 98") {
				t.Fatalf("n=%d: the wrapper must hold no arg-limit refusal (CORE-156): %q", n, inner)
			}
			if len(payload) != n+2 || !strings.HasSuffix(payload, "\n\x00") {
				t.Fatalf("n=%d: stdin payload is %d bytes, want the script, a newline and a NUL", n, len(payload))
			}
		}
	}
}

// TestRemoteWrapperKeepsProfileShellOptionsForScript (M1-B9 round 3 N1,
// round 4 D1): the wrapper clears errexit/nounset/pipefail for its own code
// and the watcher, but the script must still run under the options the
// login environment set — as it always has — minus tracing. SHELLOPTS in
// the environment switches the options on at bash start-up, as a profile
// would. errexit is checked by behaviour (`false` must end the script) as
// well as by `$-`. Round 3 captured the options with `o=$(set +o)`, and bash
// drops -e inside a command substitution, so the script lost errexit; the
// round-3 test ran only under /bin/sh, whose posix mode leaked into the
// inner bash and hid that. Every outer login shell is exercised now.
func TestRemoteWrapperKeepsProfileShellOptionsForScript(t *testing.T) {
	const script = `case $- in *e*) echo errexit;; esac; case $- in *u*) echo nounset;; esac; case $- in *x*) echo xtrace;; esac; set -o | grep -E "^pipefail[[:space:]]+on" >/dev/null && echo pipefail; false; echo reached-after-false`
	for _, outer := range []string{"/bin/bash", "/bin/sh", "/bin/zsh", "/bin/tcsh"} {
		if _, err := os.Stat(outer); err != nil {
			continue
		}
		t.Run(strings.ReplaceAll(strings.TrimPrefix(outer, "/"), "/", "_"), func(t *testing.T) {
			arg, payload := remoteBashInvocation("-c", script)
			cmd := exec.Command(outer, "-c", arg)
			cmd.Env = append(os.Environ(), "SHELLOPTS=errexit:nounset:pipefail:xtrace")
			cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
			var out bytes.Buffer
			cmd.Stdout = &out
			feed, err := attachRemoteStdin(cmd, payload)
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			feed.start()
			err = cmd.Wait()
			feed.join()
			if got, want := out.String(), "errexit\nnounset\npipefail\n"; got != want {
				t.Fatalf("script saw options/output %q, want %q (profile options kept, tracing off, `false` ends the script)", got, want)
			}
			if code := cmd.ProcessState.ExitCode(); code != 1 {
				t.Fatalf("exit %d (err %v), want 1: `false` under the profile's errexit ends the script with its status", code, err)
			}
		})
	}
}
