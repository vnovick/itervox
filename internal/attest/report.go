package attest

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"io"
	"time"
)

// PrintVerification verifies every record of run with pub, prints one line
// per record to out and returns the exit code: 0 when every record
// verifies, 1 when one does not, 2 when treeDir's tree cannot be computed.
// A non-empty treeDir also requires each check's tree to be the working tree
// in treeDir now. prog prefixes error messages ("itervox attest verify").
// It is shared by `itervox attest verify` and `referee verify`.
func PrintVerification(ctx context.Context, out, errOut io.Writer, prog string, run RunFile, pub ed25519.PublicKey, treeDir string) int {
	trees := map[string]string{} // excludes key -> current tree in treeDir
	failed := 0
	for _, res := range VerifyRun(run, pub) {
		r := res.Record
		reason := res.Reason
		if res.Verified && treeDir != "" && r.Kind == KindCheck {
			key := fmt.Sprint(r.TreeExcludes)
			if _, seen := trees[key]; !seen {
				treeCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
				tree, err := TreeHash(treeCtx, treeDir, r.TreeExcludes)
				cancel()
				if err != nil {
					_, _ = fmt.Fprintf(errOut, "%s: --tree %s: %v\n", prog, treeDir, err)
					return 2
				}
				trees[key] = tree
			}
			if err := Verify(r, pub, trees[key]); err != nil {
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
		line := fmt.Sprintf("%s %-14s exit=%d tree=%s head=%s", status, label, r.ExitCode, ShortHex(r.TreeHash), ShortHex(r.Head))
		if r.Kind == KindSeal {
			line = fmt.Sprintf("%s %-14s tree=%s head=%s", status, label, ShortHex(r.TreeHash), ShortHex(r.Head))
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
	_, _ = fmt.Fprintf(out, "all %d records verify (key %s)\n", len(run.Records), KeyID(pub))
	return 0
}

// ShortHex is the first 12 digits of a hex id, or "-" for none.
func ShortHex(h string) string {
	if h == "" {
		return "-"
	}
	if len(h) > 12 {
		return h[:12]
	}
	return h
}
