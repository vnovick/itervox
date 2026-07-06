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
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/vnovick/itervox/internal/atomicfs"
)

// SidecarSchemaVersion is the current `.itervox/dependencies.json` schema.
// Older sidecars are discarded; newer sidecars are also discarded (forward
// compatibility is not promised). The dashboard then shows tracker-only
// edges until a fresh analysis pass runs.
const SidecarSchemaVersion = 1

// SidecarRelativePath is the location of the sidecar relative to the project
// root (the directory containing WORKFLOW.md).
const SidecarRelativePath = ".itervox/dependencies.json"

// Sidecar is the on-disk schema for `.itervox/dependencies.json`.
type Sidecar struct {
	Version     int            `json:"version"`
	GeneratedAt time.Time      `json:"generatedAt"`
	Profile     string         `json:"profile"`
	Edges       []InferredEdge `json:"edges"`
}

// InferredEdge is one edge produced by the agent analyzer pass.
type InferredEdge struct {
	Source     string    `json:"source"`
	Target     string    `json:"target"`
	Evidence   string    `json:"evidence"`
	InferredAt time.Time `json:"inferredAt"`
}

// SidecarPath returns the absolute sidecar path for a project rooted at
// projectDir. When projectDir is empty, the path is treated as relative.
func SidecarPath(projectDir string) string {
	if projectDir == "" {
		return SidecarRelativePath
	}
	return filepath.Join(projectDir, SidecarRelativePath)
}

// LoadSidecar reads the sidecar at path. Returns (nil, nil) when the file is
// absent or carries an unsupported schema version (forward / backward
// compatibility is not promised — operators re-run analysis on upgrade).
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
	if sc.Version != SidecarSchemaVersion {
		return nil, nil
	}
	return &sc, nil
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
