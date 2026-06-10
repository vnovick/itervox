package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/depsanalysis"
)

// TestDecideInitDepsAnalysis_AutoRespectsPlaceholderHeuristic — `auto` mode
// (the default) only runs the analyzer when the .env stub looks populated.
// Placeholder env → skip; real env → run. This is the v0.2.0 behaviour the
// flag preserves when no operator override is passed.
func TestDecideInitDepsAnalysis_AutoRespectsPlaceholderHeuristic(t *testing.T) {
	tmp := t.TempDir()
	placeholderEnv := filepath.Join(tmp, "placeholder.env")
	if err := os.WriteFile(placeholderEnv, []byte("LINEAR_API_KEY=lin_api_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if decideInitDepsAnalysis("auto", placeholderEnv) {
		t.Error("auto mode must skip when env file is placeholder")
	}
	realEnv := filepath.Join(tmp, "real.env")
	if err := os.WriteFile(realEnv, []byte("LINEAR_API_KEY=lin_api_b3a1c4f78e2d6951b06c8e3a1c4f78e2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !decideInitDepsAnalysis("auto", realEnv) {
		t.Error("auto mode must run when env file is populated")
	}
}

// TestDecideInitDepsAnalysis_AlwaysOverridesPlaceholder — `--analyze always`
// runs the pass even with a placeholder env. The pass itself will fail (no
// credentials) and the error message guides the operator; that's the
// intended behaviour for CI scripts that explicitly want a hard signal.
func TestDecideInitDepsAnalysis_AlwaysOverridesPlaceholder(t *testing.T) {
	tmp := t.TempDir()
	envPath := filepath.Join(tmp, "placeholder.env")
	if err := os.WriteFile(envPath, []byte("LINEAR_API_KEY=lin_api_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !decideInitDepsAnalysis("always", envPath) {
		t.Error("--analyze always must run even with placeholder env")
	}
}

// TestDecideInitDepsAnalysis_NeverSkipsRegardless — `--analyze never` skips
// the pass even when the env file is populated. Useful when the operator
// wants to do init purely as scaffolding (no API calls during init).
func TestDecideInitDepsAnalysis_NeverSkipsRegardless(t *testing.T) {
	tmp := t.TempDir()
	envPath := filepath.Join(tmp, "real.env")
	if err := os.WriteFile(envPath, []byte("LINEAR_API_KEY=lin_api_b3a1c4f78e2d6951b06c8e3a1c4f78e2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if decideInitDepsAnalysis("never", envPath) {
		t.Error("--analyze never must skip even with populated env")
	}
}

// TestAdvertiseMissingDepsSidecar_FiresWhenSidecarAbsent — the daemon
// startup advisory must surface a slog line when deps_analyzer_profile is
// set and the sidecar file is missing. Captured by replacing the default
// slog handler with one that writes to a buffer.
func TestAdvertiseMissingDepsSidecar_FiresWhenSidecarAbsent(t *testing.T) {
	tmp := t.TempDir()
	wf := filepath.Join(tmp, "WORKFLOW.md")
	if err := os.WriteFile(wf, []byte("---\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{}
	cfg.Agent.DepsAnalyzerProfile = "deps-analyzer"

	buf := &bytes.Buffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	defer slog.SetDefault(prev)

	advertiseMissingDepsSidecar(cfg, wf)

	if !strings.Contains(buf.String(), "dependency-analyzer sidecar missing") {
		t.Errorf("advisory not logged; got %q", buf.String())
	}
	if !strings.Contains(buf.String(), "itervox deps analyze") {
		t.Errorf("advisory missing CLI hint; got %q", buf.String())
	}
}

// TestAdvertiseMissingDepsSidecar_SilentWhenProfileUnset — the analyzer is
// opt-in via deps_analyzer_profile. Operators who haven't configured a
// profile should NOT see an advisory at every daemon start.
func TestAdvertiseMissingDepsSidecar_SilentWhenProfileUnset(t *testing.T) {
	tmp := t.TempDir()
	wf := filepath.Join(tmp, "WORKFLOW.md")
	if err := os.WriteFile(wf, []byte("---\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{} // DepsAnalyzerProfile == ""

	buf := &bytes.Buffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	defer slog.SetDefault(prev)

	advertiseMissingDepsSidecar(cfg, wf)

	if strings.Contains(buf.String(), "sidecar missing") {
		t.Errorf("advisory must NOT fire when profile is unset; got %q", buf.String())
	}
}

// TestAdvertiseMissingDepsSidecar_SilentWhenSidecarPresent — once the
// sidecar exists (init's one-shot pass succeeded OR dashboard ran the
// pass), the advisory must NOT fire. Otherwise operators would see a
// confusing "missing" line every restart.
func TestAdvertiseMissingDepsSidecar_SilentWhenSidecarPresent(t *testing.T) {
	tmp := t.TempDir()
	wf := filepath.Join(tmp, "WORKFLOW.md")
	if err := os.WriteFile(wf, []byte("---\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	itxDir := filepath.Join(tmp, ".itervox")
	if err := os.MkdirAll(itxDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sc := depsanalysis.Sidecar{
		Version:     depsanalysis.SidecarSchemaVersion,
		GeneratedAt: time.Now().UTC(),
		Profile:     "deps-analyzer",
		Edges:       []depsanalysis.InferredEdge{},
	}
	data, _ := json.Marshal(sc)
	if err := os.WriteFile(filepath.Join(itxDir, "dependencies.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{}
	cfg.Agent.DepsAnalyzerProfile = "deps-analyzer"

	buf := &bytes.Buffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	defer slog.SetDefault(prev)

	advertiseMissingDepsSidecar(cfg, wf)

	if strings.Contains(buf.String(), "sidecar missing") {
		t.Errorf("advisory must NOT fire when sidecar already exists; got %q", buf.String())
	}
}

// TestRunDepsList_EmptySidecarPrintsEmptyEdges — `itervox deps list` against
// a project with no sidecar prints `{"edges":[]}` to stdout (so JSON-piping
// tools always get a valid envelope) and a hint to stderr. Exit code 0.
func TestRunDepsList_EmptySidecarPrintsEmptyEdges(t *testing.T) {
	tmp := t.TempDir()
	wf := filepath.Join(tmp, "WORKFLOW.md")
	if err := os.WriteFile(wf, []byte("---\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, _ := captureStdout(t, func() {
		runDepsList([]string{"--workflow", wf})
	})

	if !strings.Contains(stdout, `"edges":[]`) {
		t.Errorf(`expected '{"edges":[]}' on stdout; got %q`, stdout)
	}
}

// TestRunDepsList_PrintsSidecarAsJSON — when the sidecar exists, the CLI
// prints it as indented JSON suitable for piping into `jq` or visual
// inspection. Asserts the schema-version field round-trips.
func TestRunDepsList_PrintsSidecarAsJSON(t *testing.T) {
	tmp := t.TempDir()
	wf := filepath.Join(tmp, "WORKFLOW.md")
	if err := os.WriteFile(wf, []byte("---\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	itxDir := filepath.Join(tmp, ".itervox")
	if err := os.MkdirAll(itxDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sc := depsanalysis.Sidecar{
		Version:     depsanalysis.SidecarSchemaVersion,
		GeneratedAt: time.Now().UTC(),
		Profile:     "deps-analyzer",
		Edges: []depsanalysis.InferredEdge{
			{Source: "ENG-1", Target: "ENG-2", Evidence: "test", InferredAt: time.Now().UTC()},
		},
	}
	data, _ := json.Marshal(sc)
	if err := os.WriteFile(filepath.Join(itxDir, "dependencies.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, _ := captureStdout(t, func() {
		runDepsList([]string{"--workflow", wf})
	})

	if !strings.Contains(stdout, `"version": `+itoa(depsanalysis.SidecarSchemaVersion)) {
		t.Errorf("expected sidecar version in output; got %q", stdout)
	}
	if !strings.Contains(stdout, "ENG-1") || !strings.Contains(stdout, "ENG-2") {
		t.Errorf("expected edge IDs in output; got %q", stdout)
	}
}

// captureStdout swaps os.Stdout with a pipe for the duration of fn and
// returns whatever was written. Restores stdout before returning.
func captureStdout(t *testing.T, fn func()) (string, string) {
	t.Helper()
	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	done := make(chan struct{})
	var captured []byte
	go func() {
		captured, _ = io.ReadAll(r)
		close(done)
	}()
	fn()
	_ = w.Close()
	<-done
	os.Stdout = oldStdout
	return string(captured), ""
}

// itoa is a tiny helper that avoids pulling strconv into the test file just
// for one Sprintf-style format.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
