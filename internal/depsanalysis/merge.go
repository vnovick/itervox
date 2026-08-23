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

// DedupeInferredEdges collapses duplicate (source, target) pairs, which occur
// naturally when two chunks both surface the same relation. The
// highest-confidence copy is kept. On a tie (equal confidence, including the
// common case where neither copy carries a confidence score), the copy with
// the NEWER InferredAt wins (#50 — a deliberate choice: fresher evidence
// wins ties, since a later chunk/pass re-confirming a relation is at least
// as trustworthy as an earlier one and may reflect updated issue content).
// If InferredAt is ALSO equal (e.g. both zero-valued, unstamped), the first
// occurrence wins, so order is preserved and untouched by a total tie.
func DedupeInferredEdges(edges []InferredEdge) []InferredEdge {
	if len(edges) == 0 {
		return nil
	}
	indexByKey := make(map[edgeKey]int, len(edges))
	out := make([]InferredEdge, 0, len(edges))
	for _, e := range edges {
		k := edgeKey{Source: e.Source, Target: e.Target}
		if idx, dup := indexByKey[k]; dup {
			switch {
			case e.Confidence > out[idx].Confidence:
				out[idx] = e
			case e.Confidence == out[idx].Confidence && e.InferredAt.After(out[idx].InferredAt):
				out[idx] = e
			}
			continue
		}
		indexByKey[k] = len(out)
		out = append(out, e)
	}
	return out
}
