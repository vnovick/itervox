package orchestrator

import "sort"

// sccResult holds an iterative-Tarjan strongly-connected-component
// decomposition of a TickGraph's node set. Component ids are assigned in the
// order SCCs are finalized (Tarjan's stack-pop order) over nodes visited in
// sorted order, so a given TickGraph always yields the same component ids —
// callers (ComputeGraphMetrics, ExtractCycles) rely on this determinism.
type sccResult struct {
	compOf   map[string]int // node identifier -> component id
	compSize map[int]int    // component id -> member count
	numComp  int
}

// sortedNodes returns g's node identifiers in sorted order, the canonical
// visiting order every tick_graph.go traversal uses for determinism.
func sortedNodes(g TickGraph) []string {
	nodes := make([]string, 0, len(g.Nodes))
	for n := range g.Nodes {
		nodes = append(nodes, n)
	}
	sort.Strings(nodes)
	return nodes
}

// adjacency builds a defensive, node-set-filtered copy of g.Out: any target
// not present in g.Nodes is dropped. BuildTickGraph never produces such
// edges, but ComputeGraphMetrics/ExtractCycles also accept hand-built
// TickGraph values directly (as pure functions should), so this keeps the
// SCC walk from indexing an identifier that isn't a node.
func adjacency(g TickGraph, nodes []string) map[string][]string {
	adj := make(map[string][]string, len(nodes))
	for _, n := range nodes {
		out := g.Out[n]
		if len(out) == 0 {
			continue
		}
		filtered := make([]string, 0, len(out))
		for _, to := range out {
			if _, ok := g.Nodes[to]; ok {
				filtered = append(filtered, to)
			}
		}
		adj[n] = filtered
	}
	return adj
}

// hasSelfEdge reports whether adj contains a node -> node edge.
func hasSelfEdge(adj map[string][]string, node string) bool {
	for _, to := range adj[node] {
		if to == node {
			return true
		}
	}
	return false
}

// tarjanFrame is one level of the explicit call stack tarjanSCC uses in
// place of recursion.
type tarjanFrame struct {
	node     string
	children []string
	idx      int
}

// tarjanSCC computes the strongly connected components of the graph (nodes,
// adj) using Tarjan's algorithm rewritten iteratively — an explicit stack of
// tarjanFrame replaces the call stack — so component discovery cannot
// overflow the goroutine stack on a large candidate set. nodes must already
// be in the desired deterministic visiting order (sortedNodes).
func tarjanSCC(nodes []string, adj map[string][]string) sccResult {
	indices := make(map[string]int, len(nodes))
	lowlink := make(map[string]int, len(nodes))
	onStack := make(map[string]bool, len(nodes))
	var tstack []string
	compOf := make(map[string]int, len(nodes))
	compSize := make(map[int]int)
	nextIndex := 0
	nextComp := 0

	for _, start := range nodes {
		if _, seen := indices[start]; seen {
			continue
		}

		work := []*tarjanFrame{{node: start, children: adj[start]}}
		indices[start] = nextIndex
		lowlink[start] = nextIndex
		nextIndex++
		tstack = append(tstack, start)
		onStack[start] = true

		for len(work) > 0 {
			top := work[len(work)-1]
			if top.idx < len(top.children) {
				child := top.children[top.idx]
				top.idx++
				if _, seen := indices[child]; !seen {
					indices[child] = nextIndex
					lowlink[child] = nextIndex
					nextIndex++
					tstack = append(tstack, child)
					onStack[child] = true
					work = append(work, &tarjanFrame{node: child, children: adj[child]})
				} else if onStack[child] {
					if indices[child] < lowlink[top.node] {
						lowlink[top.node] = indices[child]
					}
				}
				continue
			}

			work = work[:len(work)-1]
			if len(work) > 0 {
				parent := work[len(work)-1]
				if lowlink[top.node] < lowlink[parent.node] {
					lowlink[parent.node] = lowlink[top.node]
				}
			}
			if lowlink[top.node] == indices[top.node] {
				for {
					w := tstack[len(tstack)-1]
					tstack = tstack[:len(tstack)-1]
					onStack[w] = false
					compOf[w] = nextComp
					compSize[nextComp]++
					if w == top.node {
						break
					}
				}
				nextComp++
			}
		}
	}

	return sccResult{compOf: compOf, compSize: compSize, numComp: nextComp}
}

// sortedIntKeys returns the keys of an int-keyed set in ascending order, the
// deterministic iteration order ComputeGraphMetrics needs when walking
// condensation edges.
func sortedIntKeys(m map[int]struct{}) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Ints(out)
	return out
}

// kahnTopoOrder returns a topological order (sources first) of the
// condensation DAG described by condOut/inDegree, breaking ties by smallest
// component id for determinism. The condensation is guaranteed acyclic
// (condOut never contains a self-edge — ComputeGraphMetrics skips those when
// building it), so every component is visited exactly once.
//
// Processes one "wave" (all currently-zero-indegree components) at a time:
// each wave is sorted once before being appended to topo and drained, rather
// than re-sorting the whole frontier on every single enqueue (#51's
// O(V²logV) note — a full re-sort per node discovered is quadratic-ish
// across a wide DAG). Sorting a wave once and appending the next wave's
// discoveries afterward is equivalent for determinism: within a wave, ties
// are still broken by component id, and cross-wave order is fixed by the
// DAG's indegree structure regardless of discovery order within a wave.
func kahnTopoOrder(condOut []map[int]struct{}, inDegree []int) []int {
	remaining := make([]int, len(inDegree))
	copy(remaining, inDegree)

	wave := make([]int, 0, len(condOut))
	for c, d := range remaining {
		if d == 0 {
			wave = append(wave, c)
		}
	}
	sort.Ints(wave)

	topo := make([]int, 0, len(condOut))
	for len(wave) > 0 {
		topo = append(topo, wave...)

		var next []int
		for _, c := range wave {
			targets := sortedIntKeys(condOut[c])
			for _, t := range targets {
				remaining[t]--
				if remaining[t] == 0 {
					next = append(next, t)
				}
			}
		}
		sort.Ints(next)
		wave = next
	}
	return topo
}
