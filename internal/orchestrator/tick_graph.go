package orchestrator

import (
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/domain"
)

// Edge kind labels shared by TickGraph.EdgeKinds and DependencyCycle.Kind.
const (
	edgeKindTracker  = "tracker"
	edgeKindInferred = "inferred"
)

// TickGraph is the per-tick dependency graph over the candidate issue set:
// nodes are candidate identifiers, edges point blocker -> blocked (i.e. "the
// blocked issue waits on the blocker"). It is the single source ordering and
// cycle/attention alerting are built from (design doc: "Tick graph (single
// source for ordering + alerts)").
type TickGraph struct {
	Nodes map[string]struct{}
	// Out[blocker] = sorted, deduplicated list of blocked identifiers.
	Out map[string][]string
	// EdgeKinds["blocker->blocked"] = set of {"tracker","inferred"} that
	// produced the edge. An edge asserted by both sources carries both kinds.
	EdgeKinds map[string]map[string]struct{}
}

// BuildTickGraph is a pure function that assembles the tick graph from the
// candidate issue set, this tick's reconciled inferred edges, and dispatch
// state. It performs no I/O and mutates nothing.
//
// Edge selection mirrors exactly the edges that hold dispatch (design doc §
// Tick graph):
//   - tracker: for each candidate issue, each issue.BlockedBy entry NOT
//     resolved per blockerResolvedForDispatch, with a non-nil, non-empty
//     Identifier that is itself a candidate node.
//   - inferred: for each candidate target, each InferredDepEntry with
//     Gating == true whose Source is a candidate node.
//
// Edges with either endpoint outside the candidate set are dropped for
// ordering/cycle purposes (sub-1's unknown-source rule). Out-lists are
// sorted for determinism.
//
// Scope consequence (verified, #59): because the node set is exactly
// FetchCandidateIssues' output (active states only), a blocker parked in a
// non-active, non-terminal state — "In Review", "Blocked" — is not a node and
// its edge is dropped. This does NOT affect the ordering of any dispatchable
// issue: the dropped edge points blocker -> blocked, and the metrics count
// only DOWNSTREAM nodes, so removing an inbound edge leaves the blocked
// candidate's TransitiveDependents/LongestChain unchanged. The only metrics
// that change belong to the parked issue itself, which is not in the
// candidate set and is therefore never sorted for dispatch.
//
// The real limitation is narrower and deeper: a parked issue sitting in the
// MIDDLE of a chain (X -> parked Y -> Z) severs transitive reachability, so X
// under-counts Z. That case is not fixable by widening the node set, because
// the parked issue's own BlockedBy list is never fetched — the graph only
// ever learns edges from candidates' BlockedBy plus inferred edges. Closing
// it would require fetching non-candidate blockers, which is a tracker-cost
// tradeoff, not a local change here.
func BuildTickGraph(candidates []domain.Issue, inferred map[string][]InferredDepEntry, state State) TickGraph {
	g := TickGraph{
		Nodes:     make(map[string]struct{}, len(candidates)),
		Out:       make(map[string][]string),
		EdgeKinds: make(map[string]map[string]struct{}),
	}
	for _, c := range candidates {
		if c.Identifier == "" {
			continue
		}
		g.Nodes[c.Identifier] = struct{}{}
	}

	addEdge := func(from, to, kind string) {
		key := from + "->" + to
		kinds, ok := g.EdgeKinds[key]
		if !ok {
			kinds = make(map[string]struct{}, 2)
			g.EdgeKinds[key] = kinds
			g.Out[from] = append(g.Out[from], to)
		}
		kinds[kind] = struct{}{}
	}

	for _, c := range candidates {
		if _, ok := g.Nodes[c.Identifier]; !ok {
			continue
		}
		for _, blocker := range c.BlockedBy {
			if blockerResolvedForDispatch(blocker, state) {
				continue
			}
			if blocker.Identifier == nil || *blocker.Identifier == "" {
				continue
			}
			if _, ok := g.Nodes[*blocker.Identifier]; !ok {
				continue
			}
			addEdge(*blocker.Identifier, c.Identifier, edgeKindTracker)
		}
	}

	for target, entries := range inferred {
		if _, ok := g.Nodes[target]; !ok {
			continue
		}
		for _, entry := range entries {
			if !entry.Gating || entry.Source == "" {
				continue
			}
			if _, ok := g.Nodes[entry.Source]; !ok {
				continue
			}
			addEdge(entry.Source, target, edgeKindInferred)
		}
	}

	for from := range g.Out {
		sort.Strings(g.Out[from])
	}

	return g
}

// GraphMetrics is the per-identifier ordering/alert metrics computed over a
// TickGraph's SCC condensation. All members of one strongly connected
// component share the component's numbers (design doc: "all members of an
// SCC share the component's downstream counts").
type GraphMetrics struct {
	// TransitiveDependents[id] = count of distinct downstream NODES (not
	// components/paths) reachable from id's component via the condensation.
	TransitiveDependents map[string]int
	// LongestChain[id] = longest downstream path length, measured in EDGES
	// over the condensation DAG (inter-component edges only). A cycle
	// collapses to a single component, so a 2-cycle counts as one hop
	// regardless of how many edges are inside it — the "longest path through
	// a cycle" is otherwise ill-defined, so this is a deliberate
	// simplification, not an oversight.
	LongestChain map[string]int
	// CycleMembers[id] = SCC index, present only for SCCs of size > 1 or a
	// single-node SCC with a self-edge (id -> id).
	CycleMembers map[string]int
}

// ComputeGraphMetrics is a pure function computing GraphMetrics for g. It
// runs iterative Tarjan SCC (no recursion — candidate sets can be large),
// builds the SCC condensation DAG, then computes per-component transitive
// dependents and longest chain via a single reverse-topological pass so
// cycles cannot wedge the traversal.
//
// This is a single-consumer convenience wrapper around the shared SCC
// decomposition — it recomputes Tarjan on every call. Callers that also need
// ExtractCycles' output for the same TickGraph in the same tick should call
// ComputeTickGraphAnalysis instead, which computes the SCC pass once and
// feeds both consumers from it.
func ComputeGraphMetrics(g TickGraph) GraphMetrics {
	nodes := sortedNodes(g)
	adj := adjacency(g, nodes)
	scc := tarjanSCC(nodes, adj)
	return computeGraphMetrics(g, nodes, adj, scc)
}

// computeGraphMetrics is the shared core of ComputeGraphMetrics: it consumes
// an already-computed SCC decomposition (nodes/adj/scc) instead of deriving
// its own, so ComputeTickGraphAnalysis can fuse this with extractCycles over
// a single Tarjan pass.
func computeGraphMetrics(g TickGraph, nodes []string, adj map[string][]string, scc sccResult) GraphMetrics {
	condOut := make([]map[int]struct{}, scc.numComp)
	inDegree := make([]int, scc.numComp)
	for c := range condOut {
		condOut[c] = make(map[int]struct{})
	}
	for _, from := range nodes {
		cFrom := scc.compOf[from]
		for _, to := range adj[from] {
			cTo := scc.compOf[to]
			if cFrom == cTo {
				continue
			}
			condOut[cFrom][cTo] = struct{}{}
		}
	}
	for c := range condOut {
		for t := range condOut[c] {
			inDegree[t]++
		}
	}

	topo := kahnTopoOrder(condOut, inDegree)

	dependents := make([]int, scc.numComp)
	chain := make([]int, scc.numComp)
	downstream := make([]map[int]struct{}, scc.numComp)

	// Reverse-topological: a component's children (condensation targets)
	// always appear later in topo, so processing from the end guarantees
	// every child is finalized before its parent is computed. This is what
	// lets an SCC's members share one downstream set instead of each
	// re-deriving it independently (which is also what would happen if the
	// condensation sharing were broken).
	for i := len(topo) - 1; i >= 0; i-- {
		c := topo[i]
		targets := sortedIntKeys(condOut[c])
		set := make(map[int]struct{})
		maxChildChain := -1
		for _, t := range targets {
			set[t] = struct{}{}
			for d := range downstream[t] {
				set[d] = struct{}{}
			}
			if chain[t] > maxChildChain {
				maxChildChain = chain[t]
			}
		}
		downstream[c] = set
		count := 0
		for comp := range set {
			count += scc.compSize[comp]
		}
		dependents[c] = count
		if maxChildChain >= 0 {
			chain[c] = maxChildChain + 1
		}
	}

	m := GraphMetrics{
		TransitiveDependents: make(map[string]int, len(nodes)),
		LongestChain:         make(map[string]int, len(nodes)),
		CycleMembers:         make(map[string]int),
	}
	for _, node := range nodes {
		c := scc.compOf[node]
		m.TransitiveDependents[node] = dependents[c]
		m.LongestChain[node] = chain[c]
	}

	membersByComp := make(map[int][]string, scc.numComp)
	for _, node := range nodes {
		c := scc.compOf[node]
		membersByComp[c] = append(membersByComp[c], node)
	}
	for c, members := range membersByComp {
		if len(members) > 1 {
			for _, node := range members {
				m.CycleMembers[node] = c
			}
			continue
		}
		node := members[0]
		if hasSelfEdge(adj, node) {
			m.CycleMembers[node] = c
		}
	}

	return m
}

// DependencyCycle describes one strongly connected component (size > 1) or
// self-edge in the tick graph. Members stay blocked — nothing here releases
// or resolves a blocker; it is a read-only alert surface.
type DependencyCycle struct {
	Members    []string // sorted identifiers
	Kind       string   // "tracker" | "inferred" | "mixed"
	DetectedAt time.Time
}

// ExtractCycles is a pure function producing the sorted DependencyCycle list
// for g. DetectedAt is carried forward from prev when the exact sorted
// member set matches an entry there (so the alert timestamp is stable across
// ticks instead of re-stamping every tick); otherwise it is stamped now.
// Output is sorted by first member.
//
// This is a single-consumer convenience wrapper around the shared SCC
// decomposition — it recomputes Tarjan on every call. Callers that also need
// ComputeGraphMetrics' output for the same TickGraph in the same tick should
// call ComputeTickGraphAnalysis instead, which computes the SCC pass once
// and feeds both consumers from it.
func ExtractCycles(g TickGraph, prev []DependencyCycle, now time.Time) []DependencyCycle {
	nodes := sortedNodes(g)
	adj := adjacency(g, nodes)
	scc := tarjanSCC(nodes, adj)
	return extractCycles(g, nodes, adj, scc, prev, now)
}

// extractCycles is the shared core of ExtractCycles: it consumes an
// already-computed SCC decomposition (nodes/adj/scc) instead of deriving its
// own — see computeGraphMetrics's doc comment for why.
func extractCycles(g TickGraph, nodes []string, adj map[string][]string, scc sccResult, prev []DependencyCycle, now time.Time) []DependencyCycle {
	membersByComp := make(map[int][]string, scc.numComp)
	for _, node := range nodes {
		c := scc.compOf[node]
		membersByComp[c] = append(membersByComp[c], node)
	}

	prevDetectedAt := make(map[string]time.Time, len(prev))
	for _, cyc := range prev {
		prevDetectedAt[strings.Join(cyc.Members, ",")] = cyc.DetectedAt
	}

	compIDs := make([]int, 0, len(membersByComp))
	for c := range membersByComp {
		compIDs = append(compIDs, c)
	}
	sort.Ints(compIDs)

	var cycles []DependencyCycle
	for _, c := range compIDs {
		members := membersByComp[c]
		if len(members) == 1 && !hasSelfEdge(adj, members[0]) {
			continue
		}
		sortedMembers := append([]string(nil), members...)
		sort.Strings(sortedMembers)

		memberSet := make(map[string]struct{}, len(sortedMembers))
		for _, m := range sortedMembers {
			memberSet[m] = struct{}{}
		}
		kinds := make(map[string]struct{}, 2)
		for _, from := range sortedMembers {
			for _, to := range adj[from] {
				if _, ok := memberSet[to]; !ok {
					continue
				}
				for k := range g.EdgeKinds[from+"->"+to] {
					kinds[k] = struct{}{}
				}
			}
		}

		key := strings.Join(sortedMembers, ",")
		detectedAt := now
		if t, ok := prevDetectedAt[key]; ok {
			detectedAt = t
		}

		cycles = append(cycles, DependencyCycle{
			Members:    sortedMembers,
			Kind:       cycleKindFromSet(kinds),
			DetectedAt: detectedAt,
		})
	}

	sort.Slice(cycles, func(i, j int) bool {
		return cycles[i].Members[0] < cycles[j].Members[0]
	})
	return cycles
}

func cycleKindFromSet(kinds map[string]struct{}) string {
	_, hasTracker := kinds[edgeKindTracker]
	_, hasInferred := kinds[edgeKindInferred]
	switch {
	case hasTracker && hasInferred:
		return "mixed"
	case hasInferred:
		return edgeKindInferred
	case hasTracker:
		return edgeKindTracker
	default:
		// Every kind set ExtractCycles builds comes from EdgeKinds entries
		// copied off real edges, which are always tagged edgeKindTracker
		// and/or edgeKindInferred — so an empty/malformed set here should
		// never happen in production. Warn loudly instead of silently
		// mislabeling the cycle "tracker", so a future bug (or a test
		// double handing this function garbage) shows up in the logs
		// rather than as a quietly-wrong alert.
		slog.Warn("orchestrator: cycleKindFromSet called with empty/malformed kind set, defaulting to tracker")
		return edgeKindTracker
	}
}

// ComputeTickGraphAnalysis is the fused entry point for the event loop's
// per-tick ordering + alerting pass: it runs Tarjan SCC decomposition over g
// exactly ONCE and feeds both ComputeGraphMetrics' and ExtractCycles' logic
// from that single pass, instead of each running its own redundant Tarjan
// walk over the same TickGraph in the same tick (#51's O(2×Tarjan) note).
// Semantics are identical to calling ComputeGraphMetrics(g) and
// ExtractCycles(g, prevCycles, now) separately — this only shares the SCC
// computation between them.
func ComputeTickGraphAnalysis(g TickGraph, prevCycles []DependencyCycle, now time.Time) (GraphMetrics, []DependencyCycle) {
	nodes := sortedNodes(g)
	adj := adjacency(g, nodes)
	scc := tarjanSCC(nodes, adj)
	metrics := computeGraphMetrics(g, nodes, adj, scc)
	cycles := extractCycles(g, nodes, adj, scc, prevCycles, now)
	return metrics, cycles
}
