package workspace

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

const defaultHookTimeoutMs = 60000
const maxHookOutputBytes = 256 * 1024

// itervoxEnvAllowlist lists env vars the daemon explicitly threads into hook
// subprocesses on top of the inherited environment. ITERVOX_BIN is critical
// (P0-F) — without it, hooks that call `itervox action ...` resolve to whatever
// `which itervox` finds on the hook PATH, which is typically the system-
// installed binary, not the running daemon's binary. The other ITERVOX_* vars
// are surfaced for completeness so hooks can address the running daemon
// without scraping arguments.
var itervoxEnvAllowlist = []string{
	"ITERVOX_BIN",
	"ITERVOX_API_TOKEN",
	"ITERVOX_DAEMON_URL",
	"ITERVOX_ACTION_TOKEN",
}

// RunHook runs a shell script in the given workspacePath directory using
// `bash -lc <script>`. An empty script is a no-op. timeoutMs <= 0 falls
// back to the default of 60000ms.
//
// The optional logFn, if provided, is called once per output line after the
// hook completes (or fails), so callers can forward hook stdout/stderr to a
// per-issue log buffer.
//
// Returns an error tagged "hook_timeout" on deadline exceeded, or
// "hook_failed" on non-zero exit. The parent ctx is also respected.
func RunHook(ctx context.Context, script, workspacePath string, timeoutMs int, logFn ...func(string)) error {
	if strings.TrimSpace(script) == "" {
		return nil
	}
	if timeoutMs <= 0 {
		timeoutMs = defaultHookTimeoutMs
	}

	deadline := time.Duration(timeoutMs) * time.Millisecond
	hookCtx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()

	out := &cappedOutput{max: maxHookOutputBytes}
	cmd := exec.CommandContext(hookCtx, "bash", "-lc", script)
	cmd.Dir = workspacePath
	cmd.Stdout = out
	cmd.Stderr = out
	cmd.Env = hookEnv(os.Environ())
	setHookProcessGroup(cmd)

	runErr := cmd.Run()

	// Forward hook output to caller's log function, if provided.
	if len(logFn) > 0 && logFn[0] != nil {
		for _, line := range strings.Split(out.String(), "\n") {
			if strings.TrimSpace(line) != "" {
				logFn[0](line)
			}
		}
	}

	if runErr != nil {
		if hookCtx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("hook_timeout: script exceeded %dms", timeoutMs)
		}
		output := strings.TrimSpace(out.String())
		if output != "" {
			return fmt.Errorf("hook_failed: %w\n%s", runErr, output)
		}
		return fmt.Errorf("hook_failed: %w", runErr)
	}
	return nil
}

type cappedOutput struct {
	max       int
	buf       []byte
	truncated bool
}

func (b *cappedOutput) Write(p []byte) (int, error) {
	if b.max <= 0 {
		return len(p), nil
	}
	if len(p) >= b.max {
		b.buf = append(b.buf[:0], p[len(p)-b.max:]...)
		b.truncated = true
		return len(p), nil
	}
	if len(b.buf)+len(p) > b.max {
		drop := len(b.buf) + len(p) - b.max
		copy(b.buf, b.buf[drop:])
		b.buf = b.buf[:len(b.buf)-drop]
		b.truncated = true
	}
	b.buf = append(b.buf, p...)
	return len(p), nil
}

func (b *cappedOutput) String() string {
	if !b.truncated {
		return string(b.buf)
	}
	return "[hook output truncated]\n" + string(b.buf)
}

// hookEnv assembles the env slice for a hook subprocess. It starts from the
// inherited base (typically os.Environ() — the daemon's env), guarantees that
// every variable in itervoxEnvAllowlist whose process-level value is non-empty
// is present, and overwrites any inherited duplicates so the daemon's view
// wins over any pre-existing stale value (e.g. an operator who exported
// ITERVOX_BIN in their shell rc).
func hookEnv(base []string) []string {
	out := make([]string, 0, len(base)+len(itervoxEnvAllowlist))
	for _, v := range base {
		if isItervoxAllowlistKey(v) {
			continue
		}
		out = append(out, v)
	}
	for _, key := range itervoxEnvAllowlist {
		if val, ok := os.LookupEnv(key); ok && val != "" {
			out = append(out, key+"="+val)
		}
	}
	return out
}

func isItervoxAllowlistKey(kv string) bool {
	for _, key := range itervoxEnvAllowlist {
		prefix := key + "="
		if strings.HasPrefix(kv, prefix) {
			return true
		}
	}
	return false
}

func setHookProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 5 * time.Second
}
