package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/vnovick/itervox/internal/logging"
)

// supervisorTokenEnv names the API token a supervisor (the macOS menu-bar
// app, a process manager) hands the daemon in its environment (#119).
//
// It behaves like a pinned ITERVOX_API_TOKEN — the API requires it as a
// bearer token on every /api/v1/* route except /api/v1/health, even with
// server.allow_unauthenticated: true — with one difference: the daemon NEVER
// prints it. The tokenised dashboard URL is not written to stderr even on a
// terminal or with ITERVOX_PRINT_TOKEN=1, the terminal UI's dashboard URL
// carries no token, and the value is never written to <logs-dir>/api-token,
// HEARTBEAT.md, dashboard_url or the daemon registry. The supervisor already
// holds the value; anything the daemon printed would only be a second copy
// in a place (a log file, a terminal scrollback) the supervisor does not
// control.
const supervisorTokenEnv = "ITERVOX_SUPERVISOR_TOKEN"

// Bounds on a supervisor token. The lower bound keeps the value above
// logging.RegisterSecret's minimum, so it is always scrubbed from log lines;
// the character set keeps it a valid HTTP header value that survives the
// bearer middleware's exact comparison.
const (
	minSupervisorTokenLen = 16
	maxSupervisorTokenLen = 512
)

// supervisorTokenActive is true when this process took its API token from
// ITERVOX_SUPERVISOR_TOKEN. Set once by installSupervisorToken at the top of
// main(), before any goroutine starts, and read-only afterwards (the same
// lifecycle as noPrintToken).
var supervisorTokenActive bool

// validateSupervisorToken checks a supervisor token's shape. The error never
// quotes the value.
func validateSupervisorToken(tok string) error {
	if n := len(tok); n < minSupervisorTokenLen || n > maxSupervisorTokenLen {
		return fmt.Errorf("itervox: %s must be %d-%d characters (got %d)",
			supervisorTokenEnv, minSupervisorTokenLen, maxSupervisorTokenLen, n)
	}
	for i := 0; i < len(tok); i++ {
		if c := tok[i]; c < 0x21 || c > 0x7e {
			return fmt.Errorf("itervox: %s must be printable ASCII without spaces (invalid byte at offset %d)",
				supervisorTokenEnv, i)
		}
	}
	return nil
}

// supervisorEnv is the slice of the process environment
// adoptSupervisorToken touches, injectable for tests.
type supervisorEnv struct {
	lookup func(string) (string, bool)
	set    func(string, string) error
	unset  func(string) error
}

// adoptSupervisorToken moves a supervisor token into ITERVOX_API_TOKEN, which
// is what the HTTP server enforces and what agents and hooks inherit to call
// `itervox action`, and removes ITERVOX_SUPERVISOR_TOKEN so child processes
// do not carry a second copy. A different ITERVOX_API_TOKEN (for example one
// pinned in .itervox/.env) is replaced: the supervisor must be able to reach
// the daemon it started. It reports whether a supervisor token was adopted
// and whether it replaced a different pinned token.
func adoptSupervisorToken(env supervisorEnv) (adopted, replaced bool, err error) {
	raw, ok := env.lookup(supervisorTokenEnv)
	if !ok || raw == "" {
		return false, false, nil
	}
	tok := strings.TrimSpace(raw)
	if err := validateSupervisorToken(tok); err != nil {
		return false, false, err
	}
	// Scrub the value from every log line before anything can mention it.
	logging.RegisterSecret(tok)
	if prev, ok := env.lookup("ITERVOX_API_TOKEN"); ok && prev != "" && prev != tok {
		replaced = true
	}
	if err := env.set("ITERVOX_API_TOKEN", tok); err != nil {
		return false, false, errors.New("itervox: could not install the supervisor token as ITERVOX_API_TOKEN")
	}
	if err := env.unset(supervisorTokenEnv); err != nil {
		return false, false, fmt.Errorf("itervox: could not clear %s", supervisorTokenEnv)
	}
	return true, replaced, nil
}

// installSupervisorToken runs adoptSupervisorToken against the real process
// environment. An invalid token is fatal: a supervisor that passed one would
// otherwise talk to a daemon it cannot authenticate against.
func installSupervisorToken() {
	adopted, replaced, err := adoptSupervisorToken(supervisorEnv{
		lookup: os.LookupEnv,
		set:    os.Setenv,
		unset:  os.Unsetenv,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		fatalExit(2)
	}
	supervisorTokenActive = adopted
	if replaced {
		slog.Warn("server: " + supervisorTokenEnv + " replaces the ITERVOX_API_TOKEN from the environment or .itervox/.env")
	}
}

// dashboardURLToken is the token the terminal UI may embed in its dashboard
// URL: the active API token, or "" when a supervisor owns it.
func dashboardURLToken() string {
	if supervisorTokenActive {
		return ""
	}
	return os.Getenv("ITERVOX_API_TOKEN")
}
