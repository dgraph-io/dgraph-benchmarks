// Package oracle is the ground truth for k-shortest-path correctness. It wraps
// gonum's YenKShortestPaths (v0.17, well past the v0.14 fix for the loop /
// missing-path bug) to compute, for a given (src,dst), the sorted vector of
// total path weights of the k shortest LOOPLESS paths.
//
// The central design choice — and the thing that makes the comparison robust
// to ties — is that we compare the *weight vector* [W1, W2, ...], not path
// identity. With road-network weights (especially after rounding), many
// distinct paths tie on total cost; which path realises a given cost is
// implementation-dependent and differs between Yen and Dgraph. But the k-th
// smallest *cost* is deterministic. So a correct comparison checks the sorted
// costs, never the node sequences.
//
// The graph is built to mirror exactly the edge set Dgraph traverses for the
// `connected` predicate: feed it the same `.e` stream the converter reads, with
// the same directedness rule (see cmd/convert: undirected datasets emit both
// directions). Parallel edges collapse to the minimum weight, matching
// Dgraph's set-valued [uid] predicate (one edge per (src,dst)).
package oracle

import (
	"fmt"
	"math"
	"sort"

	"gonum.org/v1/gonum/graph"
	"gonum.org/v1/gonum/graph/path"
	"gonum.org/v1/gonum/graph/simple"
)

// Graph is a weighted directed graph backing the oracle.
type Graph struct {
	g *simple.WeightedDirectedGraph
}

// New returns an empty oracle graph. self=0 (node-to-itself weight) and
// absent=+Inf (no edge) are the standard shortest-path conventions.
func New() *Graph {
	return &Graph{g: simple.NewWeightedDirectedGraph(0, math.Inf(1))}
}

// AddEdge inserts a directed edge src->dst with the given weight. Parallel
// edges collapse to the minimum weight so the oracle's path costs agree with
// Dgraph's single-edge-per-pair model. Self-loops are dropped — they cannot
// appear in a loopless path. YenKShortestPaths panics on negative weights, so
// reject them here with a clear error path instead.
func (gr *Graph) AddEdge(src, dst int64, weight float64) error {
	if weight < 0 {
		return fmt.Errorf("negative edge weight %g on %d->%d (Yen requires non-negative)", weight, src, dst)
	}
	if src == dst {
		return nil
	}
	if e := gr.g.WeightedEdge(src, dst); e != nil {
		if weight >= e.Weight() {
			return nil // existing edge is at least as cheap; keep it
		}
		gr.g.RemoveEdge(src, dst)
	}
	gr.g.SetWeightedEdge(gr.g.NewWeightedEdge(simple.Node(src), simple.Node(dst), weight))
	return nil
}

// AddNode ensures a vertex exists even if it has no edges. Needed so the SSSP
// reference lists isolated/unreachable vertices (as +Inf) rather than omitting
// them.
func (gr *Graph) AddNode(id int64) {
	if gr.g.Node(id) == nil {
		gr.g.AddNode(simple.Node(id))
	}
}

// Nodes returns the number of vertices currently in the graph.
func (gr *Graph) Nodes() int { return gr.g.Nodes().Len() }

// TopK returns the sorted (non-decreasing) total weights of up to k shortest
// loopless paths from src to dst. The slice has length <= k; a length shorter
// than k means fewer than k distinct loopless paths exist. An empty slice means
// dst is unreachable from src.
//
// This weight vector is the oracle's answer. It is deterministic regardless of
// how ties among equal-cost paths are broken.
func (gr *Graph) TopK(src, dst int64, k int) ([]float64, error) {
	s := gr.g.Node(src)
	if s == nil {
		return nil, fmt.Errorf("src %d not in graph", src)
	}
	t := gr.g.Node(dst)
	if t == nil {
		return nil, fmt.Errorf("dst %d not in graph", dst)
	}
	paths := path.YenKShortestPaths(gr.g, k, math.Inf(1), s, t)
	weights := make([]float64, 0, len(paths))
	for _, p := range paths {
		w, err := gr.pathWeight(p)
		if err != nil {
			return nil, err
		}
		weights = append(weights, w)
	}
	sort.Float64s(weights)
	return weights, nil
}

// SSSP returns single-source shortest-path distances from src to every node,
// via gonum's Dijkstra on the same graph used for TopK. Unreachable nodes map
// to +Inf. This is the top-1 reference for the existing correctness mode, and
// computing it on the identical graph guarantees it agrees with the oracle.
func (gr *Graph) SSSP(src int64) (map[int64]float64, error) {
	s := gr.g.Node(src)
	if s == nil {
		return nil, fmt.Errorf("src %d not in graph", src)
	}
	tree := path.DijkstraFrom(s, gr.g)
	out := make(map[int64]float64)
	nodes := gr.g.Nodes()
	for nodes.Next() {
		id := nodes.Node().ID()
		out[id] = tree.WeightTo(id)
	}
	return out, nil
}

// pathWeight sums the edge weights along a node sequence returned by Yen.
func (gr *Graph) pathWeight(nodes []graph.Node) (float64, error) {
	total := 0.0
	for i := 0; i+1 < len(nodes); i++ {
		w, ok := gr.g.Weight(nodes[i].ID(), nodes[i+1].ID())
		if !ok || math.IsInf(w, 1) {
			return 0, fmt.Errorf("returned path traverses missing edge %d->%d", nodes[i].ID(), nodes[i+1].ID())
		}
		total += w
	}
	return total, nil
}
