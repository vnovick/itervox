// Package depsanalysis implements the layered dependency analysis feature:
// tracker-declared edges are merged with an LLM-inferred edge layer produced
// by a configured analyzer profile, persisted to a local sidecar file, and
// surfaced on the snapshot for the dashboard.
//
// Read-only with respect to the tracker — the inferred layer never writes
// back to Linear or GitHub. The orchestrator never mutates State from this
// package; sidecar reads happen during snapshot building.
package depsanalysis

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/vnovick/itervox/internal/atomicfs"
)

// SidecarSchemaVersion is the current `.itervox/dependencies.json` schema.
// LoadSidecar also accepts schema version 1 (edges load with Confidence 0,
// since v1 predates the confidence field). Any other version is discarded
// (forward compatibility beyond v2 is not promised). The dashboard then
// shows tracker-only edges until a fresh analysis pass runs.
const SidecarSchemaVersion = 2

// SidecarRelativePath is the location of the sidecar relative to the project
// root (the directory containing WORKFLOW.md).
const SidecarRelativePath = ".itervox/dependencies.json"

// Sidecar is the on-disk schema for `.itervox/dependencies.json`.
type Sidecar struct {
	Version     int            `json:"version"`
	GeneratedAt time.Time      `json:"generatedAt"`
	Profile     string         `json:"profile"`
	Edges       []InferredEdge `json:"edges"`
	// Analyzed records, per issue identifier, the content fingerprint and
	// timestamp of the last analysis pass that considered that issue. It is
	// additive/optional: no schema version bump accompanies it, and sidecars
	// written before this field existed load with Analyzed == nil. Consumed
	// by PlanIncremental/MergeIncremental to skip re-analyzing unchanged
	// issues.
	Analyzed map[string]AnalyzedIssue `json:"analyzed,omitempty"`
}

// AnalyzedIssue is the per-issue bookkeeping entry in Sidecar.Analyzed.
type AnalyzedIssue struct {
	// Fingerprint is the sha256 hex digest of the issue's content
	// (title + description) at the time it was last analyzed. See
	// IssueFingerprint.
	Fingerprint string    `json:"fingerprint"`
	AnalyzedAt  time.Time `json:"analyzedAt"`
	// State is the tracker state name (AnalyzerIssue.State) the issue carried
	// at the time it was last analyzed. Additive/optional: no schema version
	// bump accompanies it, and sidecars written before this field existed
	// load with State == "". A blank State is treated as "active" by the
	// auto-analyze scheduler's rule 2 (cmd/itervox/deps_auto_analyze.go) —
	// conservative, since it costs at most one extra migration pass rather
	// than silently mis-scoping a pre-fix entry as terminal.
	State string `json:"state,omitempty"`
}

// IssueFingerprint returns a content-only fingerprint for an issue: the
// sha256 hex digest of title + "\x00" + description. State transitions,
// labels, and other metadata never affect the fingerprint — an issue whose
// title/description are untouched is considered unchanged for incremental
// analysis purposes even if its tracker state moved.
func IssueFingerprint(title, description string) string {
	sum := sha256.Sum256([]byte(title + "\x00" + description))
	return hex.EncodeToString(sum[:])
}

// InferredEdge is one edge produced by the agent analyzer pass.
type InferredEdge struct {
	Source     string    `json:"source"`
	Target     string    `json:"target"`
	Evidence   string    `json:"evidence"`
	InferredAt time.Time `json:"inferredAt"`
	// Confidence is the analyzer's confidence in this edge, clamped to
	// [0, 1] by LoadSidecar. Sidecars written under schema v1 carry no
	// confidence field and load with Confidence 0.
	Confidence float64 `json:"confidence"`
}

// SidecarPath returns the absolute sidecar path for a project rooted at
// projectDir. When projectDir is empty, the path is treated as relative.
func SidecarPath(projectDir string) string {
	if projectDir == "" {
		return SidecarRelativePath
	}
	return filepath.Join(projectDir, SidecarRelativePath)
}

// sidecarMinSupportedVersion is the oldest schema version LoadSidecar still
// accepts. v1 sidecars predate the Confidence field and load with
// Confidence 0 on every edge.
const sidecarMinSupportedVersion = 1

// LoadSidecar reads the sidecar at path. Returns (nil, nil) when the file is
// absent or carries an unsupported schema version (forward / backward
// compatibility beyond [sidecarMinSupportedVersion, SidecarSchemaVersion] is
// not promised — operators re-run analysis on upgrade). Edge confidence is
// clamped into [0, 1] regardless of what was persisted on disk.
func LoadSidecar(path string) (*Sidecar, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("depsanalysis: read sidecar %s: %w", path, err)
	}
	var sc Sidecar
	if err := json.Unmarshal(data, &sc); err != nil {
		return nil, fmt.Errorf("depsanalysis: parse sidecar %s: %w", path, err)
	}
	if sc.Version < sidecarMinSupportedVersion || sc.Version > SidecarSchemaVersion {
		return nil, nil
	}
	for i, edge := range sc.Edges {
		sc.Edges[i].Confidence = clampConfidence(edge.Confidence)
	}
	return &sc, nil
}

// clampConfidence restricts a confidence value to [0, 1].
func clampConfidence(c float64) float64 {
	return min(max(c, 0), 1)
}

// SaveSidecar atomically writes the sidecar to path. The parent directory is
// created when missing.
func SaveSidecar(path string, sc *Sidecar) error {
	if sc == nil {
		return errors.New("depsanalysis: cannot save nil sidecar")
	}
	if sc.Version == 0 {
		sc.Version = SidecarSchemaVersion
	}
	if sc.Version != SidecarSchemaVersion {
		return fmt.Errorf("depsanalysis: sidecar version %d != current %d", sc.Version, SidecarSchemaVersion)
	}
	data, err := json.MarshalIndent(sc, "", "  ")
	if err != nil {
		return fmt.Errorf("depsanalysis: marshal sidecar: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("depsanalysis: create sidecar dir: %w", err)
	}
	if err := atomicfs.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("depsanalysis: write sidecar %s: %w", path, err)
	}
	return nil
}
