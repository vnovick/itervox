package depsanalysis

import "sort"

// ChunkIssues sorts the issue set and splits it into chunks of at most size.
//
// Sorting by state then identifier clusters related work, which reduces (but
// cannot eliminate) the chance that a real dependency spans two chunks. An edge
// between issues in different chunks is invisible to the analyzer — that is the
// accepted cost of a bounded prompt, and the caller is expected to log the chunk count
// so the risk is visible rather than silent.
//
// A non-positive size falls back to one chunk rather than dividing by zero.
func ChunkIssues(issues []AnalyzerIssue, size int) [][]AnalyzerIssue {
	if len(issues) == 0 {
		return nil
	}
	if size <= 0 {
		size = len(issues)
	}

	sorted := make([]AnalyzerIssue, len(issues))
	copy(sorted, issues)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].State != sorted[j].State {
			return sorted[i].State < sorted[j].State
		}
		return sorted[i].Identifier < sorted[j].Identifier
	})

	out := make([][]AnalyzerIssue, 0, (len(sorted)+size-1)/size)
	for start := 0; start < len(sorted); start += size {
		out = append(out, sorted[start:min(start+size, len(sorted))])
	}
	return out
}

// ScopeTrackerEdges returns the subset of edges whose source or target is in
// the chunk. The full tracker-edge list grows with the backlog, so passing all
// of it to every chunk would reintroduce the unbounded prompt that chunking
// exists to prevent.
func ScopeTrackerEdges(chunk []AnalyzerIssue, edges []TrackerEdge) []TrackerEdge {
	if len(chunk) == 0 || len(edges) == 0 {
		return nil
	}
	in := make(map[string]struct{}, len(chunk))
	for _, i := range chunk {
		in[i.Identifier] = struct{}{}
	}
	out := make([]TrackerEdge, 0, len(edges))
	for _, e := range edges {
		if _, ok := in[e.Source]; ok {
			out = append(out, e)
			continue
		}
		if _, ok := in[e.Target]; ok {
			out = append(out, e)
		}
	}
	return out
}
