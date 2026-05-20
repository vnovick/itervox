package workspace

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

const defaultHookTimeoutMs = 60000
const maxHookOutputBytes = 256 * 1024

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
