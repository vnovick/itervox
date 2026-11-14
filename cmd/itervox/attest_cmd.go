package main

import (
	"context"
	"crypto/ed25519"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/vnovick/itervox/internal/attest"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/server"
)

// Signed checks (agent.profiles.<name>.verify): the daemon's signing key,
// the attestations endpoint's adapter and `itervox attest verify`.

// attestStateDir is the project state directory holding the signing key
// pair and the record store: .itervox/ next to WORKFLOW.md.
func attestStateDir(workflowPath string) string {
	return filepath.Join(filepath.Dir(workflowPath), ".itervox")
}

// setupAttestation loads (or creates, on first use) the signing key and
// installs it with the record store on orch. It runs before the event loop
// starts, so the key file exists before any run's isolation policy is built
// and is denied to sandboxed agents (agent.IsolationProjectDeny). A key
// that cannot be loaded is logged; verify then still gates runs, unsigned.
// Callers run it only when a profile has verify (profilesUseVerify), so a
// project that does not use signed checks gains no files; a WORKFLOW.md
// reload builds a new orchestrator and runs it again.
func setupAttestation(orch *orchestrator.Orchestrator, workflowPath string) {
	dir := attestStateDir(workflowPath)
	// The key must never be committed: make sure .itervox/.gitignore lists
	// it before it is created (as the token files do).
	if err := ensureItervoxGitignore(dir); err != nil {
		slog.Warn("attest: could not make sure the signing key is gitignored", "error", err)
	}
	signer, err := attest.LoadOrCreateKey(filepath.Join(dir, attest.KeyFileName))
	if err != nil {
		slog.Warn("attest: signing key unavailable; signed checks run unsigned and are not stored", "error", err)
		return
	}
	orch.SetAttestation(signer, attest.Store{Dir: filepath.Join(dir, attest.DirName)})
}

// profilesUseVerify reports whether any profile in cfg has a verify list.
func profilesUseVerify(cfg *config.Config) bool {
	for _, p := range cfg.Agent.Profiles {
		if len(p.Verify) > 0 {
			return true
		}
	}
	return false
}

// attestationsAdapter backs GET /api/v1/issues/{identifier}/attestations:
// it reads the issue's record files and verifies them on the HTTP
// goroutine.
type attestationsAdapter struct {
	orch *orchestrator.Orchestrator
}

func (a attestationsAdapter) Attestations(identifier string) (server.AttestationsResponse, error) {
	store, pub, ok := a.orch.AttestationStore()
	if !ok {
		return server.AttestationsResponse{}, nil
	}
	runs, err := store.Runs(identifier)
	if err != nil {
		return server.AttestationsResponse{}, err
	}
	resp := server.AttestationsResponse{KeyID: attest.KeyID(pub)}
	for _, run := range runs {
		view := server.AttestationRunView{RunID: run.ID, Records: []server.AttestationRecordView{}}
		if seal, ok := run.Seal(); ok {
			view.FinalTree = seal.TreeHash
		}
		for _, res := range attest.VerifyRun(run, pub) {
			r := res.Record
			if view.Profile == "" {
				view.Profile = r.Profile
			}
			view.Records = append(view.Records, server.AttestationRecordView{
				Kind: r.Kind, Name: r.Name, Command: r.Command, Pipefail: r.Pipefail,
				ExitCode: r.ExitCode, TimedOut: r.TimedOut, Error: r.Error,
				StartedAt: r.StartedAt, DurationMs: r.DurationMs,
				OutputSHA256: r.OutputSHA256, OutputTail: r.OutputTail,
				TreeHash: r.TreeHash, Head: r.Head, KeyID: r.KeyID,
				Verified: res.Verified, Reason: res.Reason,
			})
		}
		resp.Runs = append(resp.Runs, view)
	}
	return resp, nil
}

const attestUsage = `usage: itervox attest verify <record.jsonl> [--pubkey PATH | --workflow WORKFLOW.md] [--tree DIR]
Checks every record's ed25519 signature and that each check ran on the run's
final tree (its seal). --tree DIR also requires each check's tree to be the
working tree in DIR now (tracked plus untracked, non-ignored files).
The public key defaults to .itervox/attest.pub next to WORKFLOW.md.`

func runAttest(args []string) {
	fatalExit(attestCmd(context.Background(), args, os.Stdout, os.Stderr))
}

// attestCmd runs `itervox attest` and returns the exit code: 0 when every
// record verifies, 1 when one does not, 2 on a usage or read error.
func attestCmd(ctx context.Context, args []string, out, errOut io.Writer) int {
	if len(args) == 0 || args[0] != "verify" {
		_, _ = fmt.Fprintln(errOut, attestUsage)
		return 2
	}
	fs := flag.NewFlagSet("attest verify", flag.ContinueOnError)
	fs.SetOutput(errOut)
	pubPath := fs.String("pubkey", "", "public key file (default: .itervox/attest.pub next to --workflow)")
	workflow := fs.String("workflow", "WORKFLOW.md", "path to WORKFLOW.md (locates the default public key)")
	treeDir := fs.String("tree", "", "also require each check's tree to be the working tree in this directory now")
	var positional []string
	rest := args[1:]
	for len(rest) > 0 {
		if err := fs.Parse(rest); err != nil {
			return 2
		}
		if fs.NArg() == 0 {
			break
		}
		positional = append(positional, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	if len(positional) != 1 {
		_, _ = fmt.Fprintln(errOut, attestUsage)
		return 2
	}
	if *pubPath == "" {
		*pubPath = filepath.Join(attestStateDir(*workflow), attest.PublicKeyFileName)
	}
	pub, err := attest.ReadPublicKey(*pubPath)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "itervox attest verify: %v\n", err)
		return 2
	}
	recs, err := attest.ReadFile(positional[0])
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "itervox attest verify: %v\n", err)
		return 2
	}
	if len(recs) == 0 {
		_, _ = fmt.Fprintf(errOut, "itervox attest verify: %s has no records\n", positional[0])
		return 1
	}
	return printAttestVerification(ctx, out, errOut, attest.RunFile{Path: positional[0], Records: recs}, pub, *treeDir)
}

// printAttestVerification prints one line per record and returns the exit
// code.
func printAttestVerification(ctx context.Context, out, errOut io.Writer, run attest.RunFile, pub ed25519.PublicKey, treeDir string) int {
	trees := map[string]string{} // excludes key -> current tree in treeDir
	failed := 0
	for _, res := range attest.VerifyRun(run, pub) {
		r := res.Record
		reason := res.Reason
		if res.Verified && treeDir != "" && r.Kind == attest.KindCheck {
			key := fmt.Sprint(r.TreeExcludes)
			if _, seen := trees[key]; !seen {
				treeCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
				tree, err := attest.TreeHash(treeCtx, treeDir, r.TreeExcludes)
				cancel()
				if err != nil {
					_, _ = fmt.Fprintf(errOut, "itervox attest verify: --tree %s: %v\n", treeDir, err)
					return 2
				}
				trees[key] = tree
			}
			if err := attest.Verify(r, pub, trees[key]); err != nil {
				reason = err.Error()
			}
		}
		status := "ok  "
		if reason != "" {
			status = "FAIL"
			failed++
		}
		label := r.Kind
		if r.Name != "" {
			label += " " + r.Name
		}
		line := fmt.Sprintf("%s %-14s exit=%d tree=%s head=%s", status, label, r.ExitCode, shortHex(r.TreeHash), shortHex(r.Head))
		if r.Kind == attest.KindSeal {
			line = fmt.Sprintf("%s %-14s tree=%s head=%s", status, label, shortHex(r.TreeHash), shortHex(r.Head))
		}
		if reason != "" {
			line += "  (" + reason + ")"
		}
		_, _ = fmt.Fprintln(out, line)
	}
	if failed > 0 {
		_, _ = fmt.Fprintf(out, "%d of %d records did not verify\n", failed, len(run.Records))
		return 1
	}
	_, _ = fmt.Fprintf(out, "all %d records verify (key %s)\n", len(run.Records), attest.KeyID(pub))
	return 0
}

func shortHex(h string) string {
	if h == "" {
		return "-"
	}
	if len(h) > 12 {
		return h[:12]
	}
	return h
}
