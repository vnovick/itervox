package depsanalysis

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
