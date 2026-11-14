package main

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

const pinUsage = `usage: referee pin --policy-sha256 <hex> [--file .referee.yml]
       referee pin --print [--file .referee.yml]

Fails (exit 20) when the file's sha256 is not <hex>, and prints the actual
hash. Pin the policy (or WORKFLOW.md) hash in the workflow file, which is
itself human lane: changing the policy then needs a reviewed workflow change.
--print prints the file's sha256.

Exit codes: 0 match (or --print), 20 mismatch, 2 usage or error.`

func pinCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("referee pin", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { _, _ = fmt.Fprintln(stderr, pinUsage) }
	want := fs.String("policy-sha256", "", "expected sha256 of --file, 64 hex digits")
	file := fs.String("file", defaultPolicyPath, "file to hash (.referee.yml, WORKFLOW.md, ...)")
	printOnly := fs.Bool("print", false, "print the file's sha256 and exit 0")
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	if fs.NArg() > 0 {
		return usageError(stderr, pinUsage, "unexpected argument %q", fs.Arg(0))
	}
	if !*printOnly && !isSHA256Hex(*want) {
		return usageError(stderr, pinUsage, "--policy-sha256 must be 64 hex digits (or use --print)")
	}
	raw, err := os.ReadFile(*file)
	if err != nil {
		return failf(stderr, "pin", "%v", err)
	}
	sum := sha256.Sum256(raw)
	got := hex.EncodeToString(sum[:])
	if *printOnly {
		_, _ = fmt.Fprintf(stdout, "%s  %s\n", got, *file)
		return exitOK
	}
	if !strings.EqualFold(got, strings.TrimSpace(*want)) {
		_, _ = fmt.Fprintf(stdout, "pin: %s does not match the pinned sha256\n  pinned: %s\n  actual: %s\n", *file, strings.ToLower(strings.TrimSpace(*want)), got)
		return exitHuman
	}
	_, _ = fmt.Fprintf(stdout, "pin: %s matches %s\n", *file, got)
	return exitOK
}

func isSHA256Hex(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}
