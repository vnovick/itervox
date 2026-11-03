package sysmon

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sync"
	"time"

	"github.com/vnovick/itervox/internal/atomicfs"
)

// Activity log rotation: the store keeps at most activityMaxRuns run files
// and at most activityMaxBytes of them, dropping the oldest first.
const (
	activityMaxRuns  = 200
	activityMaxBytes = 64 << 20
	activityIndexVer = 1
)

// ErrActivityNotFound: no activity is recorded for the issue (or run).
var ErrActivityNotFound = errors.New("sysmon: no activity recorded")

// runIDPattern is what a run ID must look like to name a file.
var runIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// activityIndexEntry is one run in index.json.
type activityIndexEntry struct {
	RunID      string    `json:"runId"`
	Identifier string    `json:"identifier"`
	StartedAt  time.Time `json:"startedAt,omitzero"`
	FinishedAt time.Time `json:"finishedAt,omitzero"`
	Bytes      int64     `json:"bytes"`
}

type activityIndex struct {
	Version int                  `json:"version"`
	Runs    []activityIndexEntry `json:"runs"`
}

// activityStore persists finished and live runs' activity under dir
// (<project>/.itervox/runtime/activity). Writes happen on the sampler
// goroutine; reads on HTTP handler goroutines; mu serialises the index.
type activityStore struct {
	dir      string
	maxRuns  int
	maxBytes int64

	mu    sync.Mutex
	idx   []activityIndexEntry
	ready bool
}

func newActivityStore(dir string) *activityStore {
	if dir == "" {
		return nil
	}
	return &activityStore{dir: dir, maxRuns: activityMaxRuns, maxBytes: activityMaxBytes}
}

func (s *activityStore) path(runID string) string {
	return filepath.Join(s.dir, runID+".json")
}

// loadIndexLocked reads index.json once. A missing or malformed index
// starts empty; unindexed run files are removed by the next prune.
func (s *activityStore) loadIndexLocked() {
	if s.ready {
		return
	}
	s.ready = true
	b, err := os.ReadFile(filepath.Join(s.dir, "index.json"))
	if err != nil {
		return
	}
	var idx activityIndex
	if json.Unmarshal(b, &idx) == nil && idx.Version == activityIndexVer {
		s.idx = idx.Runs
	}
}

// save writes r (whose RunID must be set) and records it in the index,
// then rotates.
func (s *activityStore) save(r ActivityReport) error {
	if s == nil || !runIDPattern.MatchString(r.RunID) {
		return nil
	}
	b, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("sysmon: marshal activity: %w", err)
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("sysmon: activity dir: %w", err)
	}
	if err := atomicfs.WriteFile(s.path(r.RunID), b, 0o600); err != nil {
		return fmt.Errorf("sysmon: write activity: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadIndexLocked()
	e := activityIndexEntry{RunID: r.RunID, Identifier: r.Identifier, StartedAt: r.StartedAt, FinishedAt: r.FinishedAt, Bytes: int64(len(b))}
	if i := slices.IndexFunc(s.idx, func(x activityIndexEntry) bool { return x.RunID == r.RunID }); i >= 0 {
		s.idx[i] = e
	} else {
		s.idx = append(s.idx, e)
	}
	s.rotateLocked()
	return s.writeIndexLocked()
}

// rotateLocked drops the oldest runs beyond maxRuns or maxBytes and every
// run file the index does not name.
func (s *activityStore) rotateLocked() {
	slices.SortStableFunc(s.idx, func(a, b activityIndexEntry) int { return a.StartedAt.Compare(b.StartedAt) })
	var total int64
	for _, e := range s.idx {
		total += e.Bytes
	}
	for len(s.idx) > 1 && (len(s.idx) > s.maxRuns || total > s.maxBytes) {
		total -= s.idx[0].Bytes
		_ = os.Remove(s.path(s.idx[0].RunID))
		s.idx = s.idx[1:]
	}
	keep := make(map[string]bool, len(s.idx))
	for _, e := range s.idx {
		keep[e.RunID+".json"] = true
	}
	ents, _ := os.ReadDir(s.dir)
	for _, de := range ents {
		n := de.Name()
		if n != "index.json" && filepath.Ext(n) == ".json" && !keep[n] {
			_ = os.Remove(filepath.Join(s.dir, n))
		}
	}
}

func (s *activityStore) writeIndexLocked() error {
	b, err := json.Marshal(activityIndex{Version: activityIndexVer, Runs: s.idx})
	if err != nil {
		return err
	}
	return atomicfs.WriteFile(filepath.Join(s.dir, "index.json"), b, 0o600)
}

// runs returns the indexed runs of identifier, newest first.
func (s *activityStore) runs(identifier string) []activityIndexEntry {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadIndexLocked()
	var out []activityIndexEntry
	for _, e := range s.idx {
		if e.Identifier == identifier {
			out = append(out, e)
		}
	}
	slices.SortFunc(out, func(a, b activityIndexEntry) int { return b.StartedAt.Compare(a.StartedAt) })
	return out
}

// load reads a run's activity. runID must be in the index (and so match
// runIDPattern), which also keeps a request parameter from naming a path.
func (s *activityStore) load(identifier, runID string) (ActivityReport, error) {
	if s == nil {
		return ActivityReport{}, ErrActivityNotFound
	}
	found := false
	for _, e := range s.runs(identifier) {
		if e.RunID == runID {
			found = true
			break
		}
	}
	if !found {
		return ActivityReport{}, ErrActivityNotFound
	}
	b, err := os.ReadFile(s.path(runID))
	if err != nil {
		return ActivityReport{}, ErrActivityNotFound
	}
	var r ActivityReport
	if err := json.Unmarshal(b, &r); err != nil {
		return ActivityReport{}, fmt.Errorf("sysmon: read activity: %w", err)
	}
	return r, nil
}
