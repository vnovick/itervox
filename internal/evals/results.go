package evals

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"time"

	"github.com/vnovick/itervox/internal/atomicfs"
)

// Persisted eval results (#201). `make evals` (recorded mode), `make
// evals-live`, `make evals-record` and cmd/itervox-evals write each judged
// Report as JSON to <project>/.itervox/evals/<ISO8601>.json — gitignored
// runtime state, the newest MaxResultFiles kept — and the daemon's Repo
// Analysis page reads the newest files back through LoadResults. Reading is
// all the daemon does: no eval run is ever started from the dashboard.

// ResultsDir is where eval reports are persisted, relative to the project
// root (the directory holding WORKFLOW.md).
const ResultsDir = ".itervox/evals"

// MaxResultFiles is how many reports WriteResultFile keeps and LoadResults
// reads.
const MaxResultFiles = 20

// ResultFileVersion is the persisted report's schema version.
const ResultFileVersion = 1

// maxResultFileBytes bounds one report LoadResults will read; a larger file
// is skipped rather than loaded into the daemon.
const maxResultFileBytes = 4 << 20

// Digest sources (ScenarioVerdict.DigestSource).
const (
	DigestSourceLive      = "live"
	DigestSourceRecording = "recording"
)

// Result modes (ResultFile.Mode): which command produced the report.
const (
	ModeRecorded = "recorded" // replayed the committed recordings (`make evals`)
	ModeLive     = "live"     // ran a real agent (`make evals-live`)
	ModeRecord   = "record"   // ran a real agent and wrote candidates (`make evals-record`)
)

// resultTimeLayout is the file name stamp: ISO 8601 basic format in UTC with
// milliseconds, so names sort chronologically and carry no ':' (not allowed
// in Windows file names).
const resultTimeLayout = "20060102T150405.000Z"

// resultNamePattern matches the file names WriteResultFile produces.
var resultNamePattern = regexp.MustCompile(`^\d{8}T\d{6}\.\d{3}Z\.json$`)

// ResultFile is one persisted eval report.
type ResultFile struct {
	Version    int          `json:"version"`
	Mode       string       `json:"mode"`
	StartedAt  time.Time    `json:"startedAt"`
	FinishedAt time.Time    `json:"finishedAt"`
	Workflow   string       `json:"workflow,omitempty"`
	Pass       int          `json:"pass"`
	Total      int          `json:"total"`
	Results    []CaseResult `json:"results"`
}

// CaseResult is one scenario's outcome in a persisted report.
type CaseResult struct {
	Profile  string `json:"profile"`
	Scenario string `json:"scenario"`
	Pass     bool   `json:"pass"`
	// Failures lists every assertion that failed — missing required
	// actions, forbidden actions, forbidden_phrases present, marker phrases
	// missing — deterministic judge first.
	Failures []string `json:"failures,omitempty"`
	// Error is a live run that could not be judged as complete (backend
	// failure, budget).
	Error string `json:"error,omitempty"`
	// Stale marks a recorded-mode recording older than its sources.
	Stale bool `json:"stale,omitempty"`
	// PromptDigest / DigestSource: see ScenarioVerdict.
	PromptDigest string  `json:"promptDigest,omitempty"`
	DigestSource string  `json:"digestSource,omitempty"`
	Tokens       int     `json:"tokens,omitempty"`
	CostUSD      float64 `json:"costUsd,omitempty"`
}

// NewResultFile turns a judged report (and, for live modes, its per-run
// usage) into the persisted shape.
func NewResultFile(mode string, report Report, runs []LiveRun, workflow string, startedAt, finishedAt time.Time) ResultFile {
	usage := map[string]LiveRun{}
	for _, r := range runs {
		usage[r.Profile+"/"+r.Scenario] = r
	}
	pass, total := report.Aggregate()
	f := ResultFile{
		Version: ResultFileVersion, Mode: mode, Workflow: workflow,
		StartedAt: startedAt.UTC(), FinishedAt: finishedAt.UTC(),
		Pass: pass, Total: total, Results: make([]CaseResult, 0, len(report.Verdicts)),
	}
	for _, v := range report.Verdicts {
		r := CaseResult{
			Profile: v.Profile, Scenario: v.Scenario, Pass: v.Pass(), Error: v.Error, Stale: v.Stale,
			PromptDigest: v.PromptDigest, DigestSource: v.DigestSource,
		}
		for _, j := range []JudgeVerdict{v.Deterministic, v.Structural} {
			switch {
			case j.Pass:
			case len(j.Failures) > 0:
				r.Failures = append(r.Failures, j.Failures...)
			default: // a verdict built without the failure list (missing recording)
				r.Failures = append(r.Failures, j.Reason)
			}
		}
		r.Failures = slices.Compact(r.Failures) // both judges report "recording missing"
		if u, ok := usage[v.Profile+"/"+v.Scenario]; ok {
			r.Tokens, r.CostUSD = u.Tokens, u.CostUSD
		}
		f.Results = append(f.Results, r)
	}
	return f
}

// WriteResultFile persists f as <dir>/<ISO8601>.json (named after its
// FinishedAt), makes sure dir is gitignored, and prunes all but the newest
// MaxResultFiles reports. It returns the written path.
func WriteResultFile(dir string, f ResultFile) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("evals: results dir: %w", err)
	}
	if err := ensureResultsGitignore(dir); err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return "", fmt.Errorf("evals: encode results: %w", err)
	}
	at := f.FinishedAt.UTC()
	if at.IsZero() {
		at = time.Now().UTC()
	}
	path := filepath.Join(dir, at.Format(resultTimeLayout)+".json")
	// Two reports finishing in the same millisecond get distinct names.
	for range 1000 {
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			break
		}
		at = at.Add(time.Millisecond)
		path = filepath.Join(dir, at.Format(resultTimeLayout)+".json")
	}
	if err := atomicfs.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return "", fmt.Errorf("evals: write results: %w", err)
	}
	names, err := resultFileNames(dir)
	if err != nil {
		return path, nil
	}
	for _, old := range names[min(len(names), MaxResultFiles):] {
		_ = os.Remove(filepath.Join(dir, old))
	}
	return path, nil
}

// ensureResultsGitignore keeps the results dir out of git whatever the
// project's own ignore rules are: reports are local runtime state.
func ensureResultsGitignore(dir string) error {
	path := filepath.Join(dir, ".gitignore")
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if err := atomicfs.WriteFile(path, []byte("# itervox eval reports (#201): local runtime state\n*\n"), 0o644); err != nil {
		return fmt.Errorf("evals: results .gitignore: %w", err)
	}
	return nil
}

// resultFileNames lists the report files in dir, newest first.
func resultFileNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && resultNamePattern.MatchString(e.Name()) {
			names = append(names, e.Name())
		}
	}
	slices.Sort(names)
	slices.Reverse(names)
	return names, nil
}

// ReportRef identifies one report LoadResults read.
type ReportRef struct {
	File       string    `json:"file"`
	Mode       string    `json:"mode"`
	FinishedAt time.Time `json:"finishedAt"`
	Pass       int       `json:"pass"`
	Total      int       `json:"total"`
}

// CaseSummary is the latest result of one profile/scenario across the
// reports read, with its regression marker.
type CaseSummary struct {
	Profile  string     `json:"profile"`
	Scenario string     `json:"scenario"`
	Latest   CaseResult `json:"latest"`
	// LatestAt / Mode / File name the report the latest result is from.
	LatestAt time.Time `json:"latestAt"`
	Mode     string    `json:"mode"`
	File     string    `json:"file"`
	// Regression is set when the latest result fails on its prompt digest
	// and the newest result on the previous digest (PreviousDigest, at
	// PreviousAt) passed. Results without a digest never mark one.
	Regression     bool       `json:"regression,omitempty"`
	PreviousDigest string     `json:"previousDigest,omitempty"`
	PreviousAt     *time.Time `json:"previousAt,omitempty"`
	// Results counts the reports read that include the case.
	Results int `json:"results"`
}

// ResultsSummary is LoadResults' view of the newest reports.
type ResultsSummary struct {
	// Reports are the reports read, newest first.
	Reports []ReportRef `json:"reports"`
	// Cases are sorted by profile, then scenario.
	Cases []CaseSummary `json:"cases"`
	// Skipped counts report files that could not be read or parsed.
	Skipped int `json:"skipped,omitempty"`
}

// LoadResults reads the newest MaxResultFiles reports in dir and returns the
// latest result of every case with its regression marker. A missing dir is
// an empty summary, not an error.
func LoadResults(dir string) (ResultsSummary, error) {
	out := ResultsSummary{Reports: []ReportRef{}, Cases: []CaseSummary{}}
	names, err := resultFileNames(dir)
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return out, fmt.Errorf("evals: read results: %w", err)
	}
	type seen struct {
		result CaseResult
		at     time.Time
		mode   string
		file   string
	}
	history := map[string][]seen{} // newest first, per case
	var order []string
	for _, name := range names[:min(len(names), MaxResultFiles)] {
		f, err := readResultFile(filepath.Join(dir, name))
		if err != nil {
			out.Skipped++
			continue
		}
		out.Reports = append(out.Reports, ReportRef{File: name, Mode: f.Mode, FinishedAt: f.FinishedAt, Pass: f.Pass, Total: f.Total})
		for _, r := range f.Results {
			key := r.Profile + "/" + r.Scenario
			if _, ok := history[key]; !ok {
				order = append(order, key)
			}
			history[key] = append(history[key], seen{result: r, at: f.FinishedAt, mode: f.Mode, file: name})
		}
	}
	for _, key := range order {
		h := history[key]
		latest := h[0]
		c := CaseSummary{
			Profile: latest.result.Profile, Scenario: latest.result.Scenario, Latest: latest.result,
			LatestAt: latest.at, Mode: latest.mode, File: latest.file, Results: len(h),
		}
		if d := latest.result.PromptDigest; !latest.result.Pass && d != "" {
			for _, prev := range h[1:] {
				if prev.result.PromptDigest == "" || prev.result.PromptDigest == d {
					continue
				}
				// The newest result on the previous digest decides.
				if prev.result.Pass {
					at := prev.at
					c.Regression, c.PreviousDigest, c.PreviousAt = true, prev.result.PromptDigest, &at
				}
				break
			}
		}
		out.Cases = append(out.Cases, c)
	}
	slices.SortFunc(out.Cases, func(a, b CaseSummary) int {
		return cmp.Or(cmp.Compare(a.Profile, b.Profile), cmp.Compare(a.Scenario, b.Scenario))
	})
	return out, nil
}

func readResultFile(path string) (ResultFile, error) {
	fh, err := os.Open(path)
	if err != nil {
		return ResultFile{}, err
	}
	defer func() { _ = fh.Close() }()
	data, err := io.ReadAll(io.LimitReader(fh, maxResultFileBytes+1))
	if err != nil {
		return ResultFile{}, err
	}
	if len(data) > maxResultFileBytes {
		return ResultFile{}, errors.New("evals: results file too large")
	}
	var f ResultFile
	if err := json.Unmarshal(data, &f); err != nil {
		return ResultFile{}, err
	}
	if f.Version != ResultFileVersion {
		return ResultFile{}, fmt.Errorf("evals: results file version %d", f.Version)
	}
	return f, nil
}
