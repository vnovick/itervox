package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/agent/agenttest"
	"github.com/vnovick/itervox/internal/attest"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/gitexec"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/tracker"
)

// attestFixture is a project with a signing key and a git worktree with one
// signed run (a passing check and its seal) in the store.
type attestFixture struct {
	project, ws, runFile string
	store                attest.Store
	signer               *attest.Signer
}

func newAttestFixture(t *testing.T) attestFixture {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("checks are bash command lines")
	}
	project := t.TempDir()
	workflow := filepath.Join(project, "WORKFLOW.md")
	require.NoError(t, os.WriteFile(workflow, []byte("---\n---\n"), 0o644))
	signer, err := attest.LoadOrCreateKey(filepath.Join(attestStateDir(workflow), attest.KeyFileName))
	require.NoError(t, err)
	ws := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"}} {
		out, err := gitexec.Command(context.Background(), ws, args...).CombinedOutput()
		require.NoError(t, err, "%s", out)
	}
	require.NoError(t, os.WriteFile(filepath.Join(ws, "main.go"), []byte("package main\n"), 0o644))

	ctx := context.Background()
	rec := attest.Run(ctx, ws, attest.Spec{Name: "test", Shell: "echo ok"})
	rec.Issue, rec.RunID = "ENG-1", "2026-11-14T10-00-00.000Z_impl"
	require.NoError(t, signer.Sign(&rec))
	seal := attest.NewSeal(ctx, ws, nil)
	seal.Issue, seal.RunID = rec.Issue, rec.RunID
	require.NoError(t, signer.Sign(&seal))
	store := attest.Store{Dir: filepath.Join(attestStateDir(workflow), attest.DirName)}
	require.NoError(t, store.Append(rec.Issue, rec.RunID, rec, seal))
	return attestFixture{project: project, ws: ws, runFile: store.RunPath(rec.Issue, rec.RunID), store: store, signer: signer}
}

// TestAttestVerifyCLI (signed checks): `itervox attest verify` accepts a
// genuine run file (with --tree on the unchanged worktree), and fails a
// tampered record and a worktree that changed since.
func TestAttestVerifyCLI(t *testing.T) {
	f := newAttestFixture(t)
	workflow := filepath.Join(f.project, "WORKFLOW.md")
	run := func(args ...string) (int, string) {
		var out, errOut bytes.Buffer
		code := attestCmd(context.Background(), args, &out, &errOut)
		return code, out.String() + errOut.String()
	}

	code, out := run("verify", f.runFile, "--workflow", workflow, "--tree", f.ws)
	assert.Equal(t, 0, code, out)
	assert.Contains(t, out, "all 2 records verify")

	require.NoError(t, os.WriteFile(filepath.Join(f.ws, "main.go"), []byte("package main // changed\n"), 0o644))
	code, out = run("verify", f.runFile, "--workflow", workflow, "--tree", f.ws)
	assert.Equal(t, 1, code, out)
	assert.Contains(t, out, "different tree")
	code, _ = run("verify", f.runFile, "--workflow", workflow)
	assert.Equal(t, 0, code, "without --tree only the signatures and the run's own seal count")

	raw, err := os.ReadFile(f.runFile)
	require.NoError(t, err)
	tampered := filepath.Join(t.TempDir(), "tampered.jsonl")
	require.NoError(t, os.WriteFile(tampered, []byte(strings.Replace(string(raw), `"name":"test"`, `"name":"lint"`, 1)), 0o644))
	code, out = run("verify", tampered, "--pubkey", filepath.Join(attestStateDir(workflow), attest.PublicKeyFileName))
	assert.Equal(t, 1, code, out)
	assert.Contains(t, out, "signature does not verify")

	code, _ = run()
	assert.Equal(t, 2, code)
	code, _ = run("verify", f.runFile, "--pubkey", filepath.Join(t.TempDir(), "missing.pub"))
	assert.Equal(t, 2, code)
}

// TestAttestationsAdapterVerifiesRecords (signed checks): the endpoint's
// adapter returns the stored run with each record verified against the
// orchestrator's key and the run's final tree.
func TestAttestationsAdapterVerifiesRecords(t *testing.T) {
	f := newAttestFixture(t)
	cfg := &config.Config{}
	orch := orchestrator.New(cfg, tracker.NewMemoryTracker(nil, nil, nil), &agenttest.FakeRunner{}, nil)
	a := attestationsAdapter{orch: orch}
	resp, err := a.Attestations("ENG-1")
	require.NoError(t, err)
	assert.Empty(t, resp.Runs, "no attestation configured: nothing to read")

	orch.SetAttestation(f.signer, f.store)
	resp, err = a.Attestations("ENG-1")
	require.NoError(t, err)
	require.Len(t, resp.Runs, 1)
	assert.Equal(t, attest.KeyID(f.signer.PublicKey()), resp.KeyID)
	require.Len(t, resp.Runs[0].Records, 2)
	for _, r := range resp.Runs[0].Records {
		assert.True(t, r.Verified, "%s: %s", r.Kind, r.Reason)
	}
	assert.Equal(t, resp.Runs[0].Records[0].TreeHash, resp.Runs[0].FinalTree)
}

// TestItervoxGitignoreIgnoresAttestKey (signed checks): .itervox/.gitignore
// keeps the signing key and the record store out of git; the public key
// stays committable, and git agrees.
func TestItervoxGitignoreIgnoresAttestKey(t *testing.T) {
	root := t.TempDir()
	out, err := gitexec.Command(context.Background(), root, "init", "-q").CombinedOutput()
	require.NoError(t, err, "%s", out)
	dir := filepath.Join(root, ".itervox")
	require.NoError(t, ensureItervoxGitignore(dir))
	raw, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	require.NoError(t, err)
	lines := strings.Split(string(raw), "\n")
	assert.Contains(t, lines, "/attest.key")
	assert.Contains(t, lines, "attestations/")
	ignored := func(rel string) bool {
		return gitexec.Command(context.Background(), root, "check-ignore", "-q", rel).Run() == nil
	}
	assert.True(t, ignored(".itervox/attest.key"))
	assert.True(t, ignored(".itervox/attestations/ENG-1/run.jsonl"))
	assert.False(t, ignored(".itervox/attest.pub"))
}
