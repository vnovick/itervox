package guardrails

import (
	"fmt"
	"io"
	"os"
)

// IsEntrypoint reports whether arg (os.Args[1]) is one of the entrypoints
// shims and hooks call. The itervox binary checks it before anything else,
// so a shim adds no daemon start-up work (dotenv, environment scrubbing)
// to the agent's command and forwards the environment as the agent had it.
func IsEntrypoint(arg string) bool {
	return arg == shimEntrypoint || arg == hookEntrypoint
}

// Main runs a shim or hook entrypoint and returns the exit status. args
// start with the entrypoint: `guardrail-shim <policy> <tool> args…` or
// `guardrail-hook <policy> <hook> args…`. A forwarded command replaces the
// process and never returns.
//
// The policy is required: if it cannot be read (the run ended and its
// directory was removed) the command is refused, never run unguarded.
func Main(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) < 3 || !IsEntrypoint(args[0]) {
		_, _ = fmt.Fprintln(stderr, "guardrails: usage: itervox guardrail-shim|guardrail-hook <policy> <name> [args…]")
		return 2
	}
	p, err := LoadPolicy(args[1])
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "%v (the guarded session has ended; refusing)\n", err)
		return 1
	}
	name, rest := args[2], args[3:]
	// The agent's environment is forwarded as it came; this process's own
	// lookups (the hook's git reads) must not find the shims again.
	env := os.Environ()
	realPATH := stripDir(os.Getenv("PATH"), p.BinDir)
	_ = os.Setenv("PATH", realPATH)
	if args[0] == hookEntrypoint {
		if name != "pre-push" {
			input, _ := io.ReadAll(stdin)
			return chainHook(p.OrigHooksDir, name, rest, env, input, stdout, stderr)
		}
		return runPrePush(p, rest, env, stdin, stdout, stderr)
	}
	return runShim(p, name, rest, env, realPATH, stderr)
}
