package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/attest"
)

const runUsage = `usage: referee run --name <check> --key <private key> --out <records.jsonl> [--dir .] [--timeout 10m] -- <cmd...>

Runs one check and appends two signed records to --out: the check (its real
exit code, duration, output hash and tail, and the git tree it ran on) and a
seal (the tree after it). One argument after -- is a shell command line, run
by bash with pipefail ("make test | tail -n 50" fails when make fails); more
arguments are run directly with no shell. The key must not be inside --dir:
whoever can read it can forge records.

Exit codes: the check's own exit code (1 if it was killed, timed out or did
not start), 2 when referee cannot record the run.`

const keygenUsage = `usage: referee keygen --out <dir>

Writes an ed25519 key pair: <dir>/attest.key (private, mode 0600) and
<dir>/attest.pub. Keep the private key where the agent cannot read it (a CI
secret); share the public key. Never overwrites an existing key.`

const verifyUsage = `usage: referee verify <records.jsonl> --pubkey <file> [--tree DIR]

Checks every record's ed25519 signature and that each check ran on the final
tree (the last seal). --tree DIR also requires each check's tree to be the
working tree in DIR now (tracked plus untracked, non-ignored files).

Exit codes: 0 every record verifies, 1 one does not, 2 usage or error.`

func runCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("referee run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { _, _ = fmt.Fprintln(stderr, runUsage) }
	name := fs.String("name", "", "check name (test, lint, ...)")
	keyPath := fs.String("key", "", "ed25519 private key (referee keygen)")
	outPath := fs.String("out", "", "JSONL file the records are appended to")
	dir := fs.String("dir", ".", "working tree the check runs in")
	timeout := fs.Duration("timeout", attest.DefaultTimeout, "kill the check's process group after this long")
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	cmdArgs := fs.Args()
	switch {
	case strings.TrimSpace(*name) == "":
		return usageError(stderr, runUsage, "--name is required")
	case *keyPath == "":
		return usageError(stderr, runUsage, "--key is required")
	case *outPath == "":
		return usageError(stderr, runUsage, "--out is required")
	case len(cmdArgs) == 0:
		return usageError(stderr, runUsage, "no command after --")
	}
	absDir, err := filepath.Abs(*dir)
	if err != nil {
		return failf(stderr, "run", "%v", err)
	}
	// Paths below are compared with the repository's top level: the tree
	// hash covers the whole repository, whatever --dir is.
	root := absDir
	if top, err := git(ctx, absDir, "rev-parse", "--show-toplevel"); err == nil {
		root = strings.TrimSpace(string(top))
	}
	if rel, inside := within(root, *keyPath); inside {
		return failf(stderr, "run", "the key %s is inside the working tree (%s): anything the check runs could read it; keep it outside", *keyPath, rel)
	}
	signer, err := attest.LoadKey(*keyPath)
	if err != nil {
		return failf(stderr, "run", "%v", err)
	}
	spec := attest.Spec{Name: *name, Timeout: *timeout}
	if len(cmdArgs) == 1 {
		spec.Shell = cmdArgs[0]
	} else {
		spec.Argv = cmdArgs
	}
	// The record file may live in the tree it describes; leave it out of
	// the tree hash, or every record would change the next one's tree.
	if rel, inside := within(root, *outPath); inside {
		spec.TreeExcludes = []string{filepath.ToSlash(rel)}
	}

	rec := attest.Run(ctx, absDir, spec)
	seal := attest.NewSeal(ctx, absDir, spec.TreeExcludes)
	for _, r := range []*attest.Record{&rec, &seal} {
		if err := signer.Sign(r); err != nil {
			return failf(stderr, "run", "%v", err)
		}
	}
	if err := attest.AppendFile(*outPath, rec, seal); err != nil {
		return failf(stderr, "run", "%v", err)
	}
	if rec.TreeError != "" {
		_, _ = fmt.Fprintf(stderr, "referee run: no tree hash (%s); verify will reject this record\n", rec.TreeError)
	}
	_, _ = fmt.Fprintf(stdout, "%s exit=%d tree=%s duration=%s -> %s\n", rec.Name, rec.ExitCode, attest.ShortHex(rec.TreeHash),
		(time.Duration(rec.DurationMs) * time.Millisecond).String(), *outPath)
	if rec.Error != "" {
		_, _ = fmt.Fprintf(stderr, "referee run: %s\n", rec.Error)
	}
	switch {
	case rec.Passed():
		return exitOK
	case rec.ExitCode > 0 && rec.ExitCode < 256:
		return rec.ExitCode
	}
	return exitFailed
}

// within reports whether p (relative to the current directory) is inside
// dir, and its path relative to dir.
func within(dir, p string) (string, bool) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", false
	}
	if real, err := filepath.EvalSymlinks(filepath.Dir(abs)); err == nil {
		abs = filepath.Join(real, filepath.Base(abs))
	}
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	rel, err := filepath.Rel(dir, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return rel, true
}

func keygenCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("referee keygen", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { _, _ = fmt.Fprintln(stderr, keygenUsage) }
	out := fs.String("out", "", "directory for attest.key and attest.pub")
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	if *out == "" || fs.NArg() > 0 {
		return usageError(stderr, keygenUsage, "--out <dir> is required")
	}
	keyPath := filepath.Join(*out, attest.KeyFileName)
	if _, err := os.Stat(keyPath); err == nil {
		return failf(stderr, "keygen", "%s already exists; not overwriting it", keyPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return failf(stderr, "keygen", "%v", err)
	}
	signer, err := attest.LoadOrCreateKey(keyPath)
	if err != nil {
		return failf(stderr, "keygen", "%v", err)
	}
	_, _ = fmt.Fprintf(stdout, "wrote %s (private, 0600) and %s (key id %s)\n",
		keyPath, filepath.Join(*out, attest.PublicKeyFileName), attest.KeyID(signer.PublicKey()))
	return exitOK
}

func verifyCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("referee verify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { _, _ = fmt.Fprintln(stderr, verifyUsage) }
	pubPath := fs.String("pubkey", "", "public key file (referee keygen's attest.pub)")
	treeDir := fs.String("tree", "", "also require each check's tree to be the working tree in this directory now")
	// Flags may come before or after the record file.
	var positional []string
	for rest := args; ; {
		if err := fs.Parse(rest); err != nil {
			return exitError
		}
		if fs.NArg() == 0 {
			break
		}
		positional = append(positional, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	if len(positional) != 1 || *pubPath == "" {
		return usageError(stderr, verifyUsage, "need one record file and --pubkey")
	}
	pub, err := attest.ReadPublicKey(*pubPath)
	if err != nil {
		return failf(stderr, "verify", "%v", err)
	}
	recs, err := attest.ReadFile(positional[0])
	if err != nil {
		return failf(stderr, "verify", "%v", err)
	}
	if len(recs) == 0 {
		return failf(stderr, "verify", "%s has no records", positional[0])
	}
	return attest.PrintVerification(ctx, stdout, stderr, "referee verify", attest.RunFile{Path: positional[0], Records: recs}, pub, *treeDir)
}
