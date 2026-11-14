package guardrails

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/vnovick/itervox/internal/gitexec"
)

// BenchmarkShimGitRevParse: one `git rev-parse HEAD` called directly and
// through a run's git shim (a real Prepare runtime; this test binary
// stands in for itervox, so its start-up is lighter than the real
// binary's — scripts/guardrails-shim-latency.sh times the real one). Each
// sub-benchmark reports p50-ms and p95-ms besides ns/op.
func BenchmarkShimGitRevParse(b *testing.B) {
	r := newRig(b, nil)
	realGit, err := exec.LookPath("git")
	if err != nil {
		b.Skip("no git")
	}
	env := append(gitexec.Environ(), "GIT_CONFIG_GLOBAL=/dev/null",
		"PATH="+r.rt.BinDir()+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, c := range []struct{ name, prog string }{
		{"direct", realGit},
		{"shim", filepath.Join(r.rt.BinDir(), "git")},
	} {
		b.Run(c.name, func(b *testing.B) {
			var samples []time.Duration
			for b.Loop() {
				cmd := exec.Command(c.prog, "rev-parse", "HEAD")
				cmd.Dir, cmd.Env = r.work, env
				start := time.Now()
				if out, err := cmd.CombinedOutput(); err != nil {
					b.Fatalf("%s rev-parse: %v: %s", c.name, err, out)
				}
				samples = append(samples, time.Since(start))
			}
			slices.Sort(samples)
			at := func(p float64) float64 {
				return float64(samples[min(len(samples)-1, int(p*float64(len(samples))))]) / float64(time.Millisecond)
			}
			b.ReportMetric(at(0.50), "p50-ms")
			b.ReportMetric(at(0.95), "p95-ms")
		})
	}
}
