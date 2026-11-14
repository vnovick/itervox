package main

import "os"

// exit is the one place referee ends the process. Every subcommand returns
// its exit code to main, which calls exit once.
func exit(code int) { os.Exit(code) }
