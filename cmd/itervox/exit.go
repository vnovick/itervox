package main

import (
	"os"
	"os/exec"

	"github.com/charmbracelet/x/term"
)

// fatalExit terminates the process with the given exit code, restoring the
// terminal to a sane mode if stdin is a TTY. This is the SAFE alternative to
// raw os.Exit for any code path that may run AFTER `go statusui.Run` puts the
// terminal into alt-screen / raw mode — without the cooked-mode restore, the
// shell prompt comes back garbled (no echo, broken line discipline).
//
// The main()-level `defer recover()` in main.go (T-12) handles the panic case;
// fatalExit covers the explicit-exit case. Callers that exit BEFORE statusui
// has been launched are equally safe to use this — the stty call is a no-op
// when the TTY is already in cooked mode.
//
// Pair with the CLAUDE.md invariant: any new os.Exit() outside this file is
// a regression candidate (see scripts/check-no-os-exit.sh).
func fatalExit(code int) {
	if term.IsTerminal(os.Stdin.Fd()) {
		_ = exec.Command("stty", "sane").Run()
	}
	os.Exit(code)
}
