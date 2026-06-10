package depsanalysis

import "sort"

// EdgeOrigin identifies whether an edge came from the tracker or the LLM
// analyzer pass. Surfaced on the snapshot so the UI can style the two
// differently.
type EdgeOrigin string

const (
	OriginTracker  EdgeOrigin = "tracker"
	OriginInferred EdgeOrigin = "inferred"
)

// TrackerEdge is one tracker-declared blocker → blocked relation. JSON tags
// are lowercase so the analyzer prompt presents tracker edges with the same
// key naming the analyzer must use in its own output.
type TrackerEdge struct {
	Source string `json:"source"`
	Target string `json:"target"`
}

// MergedEdge is the post-merge wire shape used to enrich the snapshot.
type MergedEdge struct {
	Source   string
	Target   string
	Origin   EdgeOrigin
	Evidence string // populated only when Origin == OriginInferred
}

// MergeEdges combines tracker and inferred edges into a single deduplicated
// list. When the same (source, target) pair appears in both passes, the
// tracker copy wins and the inferred copy is dropped — tracker-declared edges
// are the authoritative source of truth.
//
// The output is sorted by (source, target) so callers see a stable order.
func MergeEdges(tracker []TrackerEdge, inferred []InferredEdge) []MergedEdge {
	seen := make(map[edgeKey]struct{}, len(tracker)+len(inferred))
	out := make([]MergedEdge, 0, len(tracker)+len(inferred))
	for _, e := range tracker {
		if e.Source == "" || e.Target == "" {
			continue
		}
		key := edgeKey(e)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, MergedEdge{Source: e.Source, Target: e.Target, Origin: OriginTracker})
	}
	for _, e := range inferred {
		if e.Source == "" || e.Target == "" {
			continue
		}
		key := edgeKey{Source: e.Source, Target: e.Target}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, MergedEdge{
			Source: e.Source, Target: e.Target,
			Origin: OriginInferred, Evidence: e.Evidence,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Source != out[j].Source {
			return out[i].Source < out[j].Source
		}
		return out[i].Target < out[j].Target
	})
	return out
}

type edgeKey struct {
	Source string
	Target string
}
