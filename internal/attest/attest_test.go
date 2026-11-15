package attest

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/gitexec"
)

// TestMain drops inherited repository-location variables (a git hook
// running the suite) so no test git call can reach the enclosing repo.
func TestMain(m *testing.M) {
	gitexec.UnsetInProcess()
	m.Run()
}

func newRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "t@t"},
		{"config", "user.name", "t"},
	} {
		gitT(t, dir, args...)
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("ignored.txt\n"), 0o644))
	gitT(t, dir, "add", "-A")
	gitT(t, dir, "commit", "-q", "-m", "base")
	return dir
}

func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitexec.Command(context.Background(), dir, args...).CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
	return strings.TrimSpace(string(out))
}

func testSigner(t *testing.T) *Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	return NewSigner(priv)
}

func skipWithoutBash(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("bash pipelines are exercised on Unix")
	}
}

// TestAttestRecordsRealExitCode: a failing command piped into tail records
// the command's failure, not tail's success, because shell checks run with
// `set -o pipefail`. Without pipefail bash would report tail's 0; the
// control case below shows that is what plain `bash -c` does.
func TestAttestRecordsRealExitCode(t *testing.T) {
	skipWithoutBash(t)
	dir := t.TempDir()
	rec := Run(context.Background(), dir, Spec{Name: "test", Shell: "(echo boom; exit 7) | tail -n 1"})
	assert.Equal(t, 7, rec.ExitCode, "pipefail: the pipeline's failing stage decides the status")
	assert.True(t, rec.Pipefail)
	assert.True(t, rec.Shell)
	assert.False(t, rec.Passed())
	assert.Contains(t, rec.OutputTail, "boom")
	assert.Equal(t, "set -o pipefail\n", rec.Argv[2][:len(pipefailPrefix)])

	// Control: the same pipeline with no pipefail exits 0 (tail's status).
	ctl := Run(context.Background(), dir, Spec{Argv: []string{"bash", "-c", "(echo boom; exit 7) | tail -n 1"}})
	assert.Equal(t, 0, ctl.ExitCode, "without pipefail the pipeline reports tail's success")

	ok := Run(context.Background(), dir, Spec{Name: "ok", Shell: "echo fine"})
	assert.Equal(t, 0, ok.ExitCode)
	assert.True(t, ok.Passed())
	sum := sha256.Sum256([]byte("fine\n"))
	assert.Equal(t, hex.EncodeToString(sum[:]), ok.OutputSHA256, "the hash covers the complete combined output")

	argv := Run(context.Background(), dir, Spec{Argv: []string{"sh", "-c", "exit 4"}})
	assert.Equal(t, 4, argv.ExitCode, "an argv command's exit code is recorded as is")
}

// TestAttestTimeoutKillsTheCheck: a check past its timeout is killed and
// recorded as timed out with exit code -1.
func TestAttestTimeoutKillsTheCheck(t *testing.T) {
	skipWithoutBash(t)
	start := time.Now()
	rec := Run(context.Background(), t.TempDir(), Spec{Name: "slow", Shell: "sleep 30", Timeout: 200 * time.Millisecond})
	assert.True(t, rec.TimedOut)
	assert.Equal(t, -1, rec.ExitCode)
	assert.False(t, rec.Passed())
	assert.Less(t, time.Since(start), 10*time.Second)
}

// TestAttestOutputTailIsBoundedAndRedacted: only the last TailBytes are
// kept, and secrets in them are scrubbed.
func TestAttestOutputTailIsBoundedAndRedacted(t *testing.T) {
	skipWithoutBash(t)
	rec := Run(context.Background(), t.TempDir(), Spec{Shell: "head -c 20000 /dev/zero | tr '\\0' x; echo; echo 'token ghp_abcdefghijklmnopqrstuvwxyz0123456789'"})
	assert.LessOrEqual(t, len(rec.OutputTail), TailBytes+64)
	assert.NotContains(t, rec.OutputTail, "ghp_abcdefghijklmnopqrstuvwxyz0123456789")
}

// TestAttestTreeHashCoversUntrackedFiles: an untracked, non-ignored file
// changes the tree hash; an ignored file and an excluded path do not.
func TestAttestTreeHashCoversUntrackedFiles(t *testing.T) {
	dir := newRepo(t)
	ctx := context.Background()
	excl := []string{".itervox/handoff"}
	base, err := TreeHash(ctx, dir, excl)
	require.NoError(t, err)
	assert.Equal(t, gitT(t, dir, "rev-parse", "HEAD^{tree}"), base, "a clean tree hashes to HEAD's tree")

	require.NoError(t, os.WriteFile(filepath.Join(dir, "ignored.txt"), []byte("x"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".itervox", "handoff"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".itervox", "handoff", "a.md"), []byte("x"), 0o644))
	same, err := TreeHash(ctx, dir, excl)
	require.NoError(t, err)
	assert.Equal(t, base, same, "ignored files and excluded paths are not part of the tree")

	require.NoError(t, os.WriteFile(filepath.Join(dir, "new.go"), []byte("package main\n"), 0o644))
	withNew, err := TreeHash(ctx, dir, excl)
	require.NoError(t, err)
	assert.NotEqual(t, base, withNew, "an untracked file is part of the tree")

	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main // edited\n"), 0o644))
	withEdit, err := TreeHash(ctx, dir, excl)
	require.NoError(t, err)
	assert.NotEqual(t, withNew, withEdit, "an unstaged edit to a tracked file is part of the tree")
}

// TestAttestDoesNotTouchUserIndex: computing the tree hash (and running a
// check, which computes it) leaves the user's index file byte-for-byte and
// mtime unchanged, and stages nothing.
func TestAttestDoesNotTouchUserIndex(t *testing.T) {
	dir := newRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "untracked.go"), []byte("package main\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main // unstaged\n"), 0o644))
	// git status refreshes the index itself, so it runs before the
	// snapshot and after the final comparison, never in between.
	statusBefore := gitT(t, dir, "status", "--porcelain")
	indexPath := filepath.Join(dir, ".git", "index")
	before, err := os.ReadFile(indexPath)
	require.NoError(t, err)
	statBefore, err := os.Stat(indexPath)
	require.NoError(t, err)

	time.Sleep(20 * time.Millisecond) // an mtime change would be visible
	_, err = TreeHash(context.Background(), dir, nil)
	require.NoError(t, err)
	_ = Run(context.Background(), dir, Spec{Argv: []string{"true"}})

	after, err := os.ReadFile(indexPath)
	require.NoError(t, err)
	statAfter, err := os.Stat(indexPath)
	require.NoError(t, err)
	assert.True(t, bytes.Equal(before, after), "index bytes unchanged")
	assert.Equal(t, statBefore.ModTime(), statAfter.ModTime(), "index mtime unchanged")
	assert.Equal(t, statusBefore, gitT(t, dir, "status", "--porcelain"), "nothing staged or unstaged by the hash")
	_, err = os.Stat(indexPath + ".lock")
	assert.True(t, os.IsNotExist(err), "no index.lock left behind")
}

// TestAttestSignatureVerifies: a signed record verifies with the signer's
// public key, survives a JSONL round trip, and fails with another key.
func TestAttestSignatureVerifies(t *testing.T) {
	skipWithoutBash(t)
	dir := newRepo(t)
	s := testSigner(t)
	rec := Run(context.Background(), dir, Spec{Name: "test", Shell: "echo ok"})
	rec.Issue, rec.RunID, rec.Profile = "ENG-1", "2026-11-14T10-00-00.000Z_impl", "impl"
	require.NoError(t, s.Sign(&rec))
	require.NoError(t, VerifySignature(rec, s.PublicKey()))
	require.NoError(t, Verify(rec, s.PublicKey(), rec.TreeHash))

	store := Store{Dir: t.TempDir()}
	require.NoError(t, store.Append("ENG-1", rec.RunID, rec))
	runs, err := store.Runs("ENG-1")
	require.NoError(t, err)
	require.Len(t, runs, 1)
	require.Len(t, runs[0].Records, 1)
	assert.NoError(t, VerifySignature(runs[0].Records[0], s.PublicKey()), "a stored record still verifies")

	other := testSigner(t)
	assert.ErrorIs(t, VerifySignature(rec, other.PublicKey()), ErrBadSignature)
}

// TestAttestTamperedRecordFails: changing any signed field (the exit code,
// the output hash, the tree) breaks the signature.
func TestAttestTamperedRecordFails(t *testing.T) {
	skipWithoutBash(t)
	dir := newRepo(t)
	s := testSigner(t)
	rec := Run(context.Background(), dir, Spec{Name: "test", Shell: "exit 1"})
	require.NoError(t, s.Sign(&rec))
	require.NoError(t, VerifySignature(rec, s.PublicKey()))

	forged := rec
	forged.ExitCode = 0
	assert.ErrorIs(t, VerifySignature(forged, s.PublicKey()), ErrBadSignature, "a failing check turned passing")
	forged = rec
	forged.OutputSHA256 = strings.Repeat("0", 64)
	assert.ErrorIs(t, VerifySignature(forged, s.PublicKey()), ErrBadSignature)
	forged = rec
	forged.TreeHash = strings.Repeat("a", 40)
	assert.ErrorIs(t, VerifySignature(forged, s.PublicKey()), ErrBadSignature)
	forged = rec
	forged.Sig = ""
	assert.ErrorIs(t, VerifySignature(forged, s.PublicKey()), ErrBadSignature)

	// Verify (signature + tree) refuses the forgery even when the tree it
	// is checked against is the record's own.
	forged = rec
	forged.ExitCode = 0
	assert.ErrorIs(t, Verify(forged, s.PublicKey(), forged.TreeHash), ErrBadSignature, "Verify checks the signature, not only the tree")
}

// TestVerifyRefusesStaleTree: a validly signed record planted from an
// earlier tree fails Verify against the current tree, and VerifyRun marks
// it unverified against its run's seal.
func TestVerifyRefusesStaleTree(t *testing.T) {
	skipWithoutBash(t)
	dir := newRepo(t)
	ctx := context.Background()
	s := testSigner(t)
	stale := Run(ctx, dir, Spec{Name: "test", Shell: "true"})
	require.NoError(t, s.Sign(&stale))

	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main // untested\n"), 0o644))
	current, err := TreeHash(ctx, dir, nil)
	require.NoError(t, err)
	require.NotEqual(t, stale.TreeHash, current)
	require.NoError(t, VerifySignature(stale, s.PublicKey()), "the planted record is genuinely signed")
	assert.ErrorIs(t, Verify(stale, s.PublicKey(), current), ErrStaleTree)

	seal := NewSeal(ctx, dir, nil)
	require.NoError(t, s.Sign(&seal))
	res := VerifyRun(RunFile{ID: "r", Records: []Record{stale, seal}}, s.PublicKey())
	require.Len(t, res, 2)
	assert.False(t, res[0].Verified, "a check from another tree does not verify against the run's seal")
	assert.Contains(t, res[0].Reason, "different tree")
	assert.True(t, res[1].Verified, "the seal itself verifies")
}

// TestAttestLoadOrCreateKey: the key is created once with mode 0600, the
// public key written next to it, and a second load returns the same key.
func TestAttestLoadOrCreateKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".itervox", KeyFileName)
	s1, err := LoadOrCreateKey(path)
	require.NoError(t, err)
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(path)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
	}
	s2, err := LoadOrCreateKey(path)
	require.NoError(t, err)
	assert.Equal(t, s1.PublicKey(), s2.PublicKey())
	pub, err := ReadPublicKey(filepath.Join(filepath.Dir(path), PublicKeyFileName))
	require.NoError(t, err)
	assert.Equal(t, s1.PublicKey(), pub)
}

// CommitTreeHash (push: daemon): for a clean work tree at HEAD it equals
// TreeHash with the same excludes (excluded paths ignored on both sides),
// and it differs once the work tree has a change the commit does not.
func TestCommitTreeHashMatchesCleanTreeHash(t *testing.T) {
	dir := newRepo(t)
	ctx := context.Background()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".itervox", "handoff"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".itervox", "handoff", "h.md"), []byte("h\n"), 0o644))
	gitT(t, dir, "add", "-A")
	gitT(t, dir, "commit", "-q", "-m", "a")
	excludes := []string{".itervox/handoff"}
	work, err := TreeHash(ctx, dir, excludes)
	require.NoError(t, err)
	commit, err := CommitTreeHash(ctx, dir, "HEAD", excludes)
	require.NoError(t, err)
	assert.Equal(t, work, commit)

	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a // changed\n"), 0o644))
	work, err = TreeHash(ctx, dir, excludes)
	require.NoError(t, err)
	assert.NotEqual(t, work, commit, "an uncommitted change is not in the commit's tree")
}
