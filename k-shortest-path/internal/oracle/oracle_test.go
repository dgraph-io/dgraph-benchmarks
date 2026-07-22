package oracle

import (
	"math"
	"testing"
)

const eps = 1e-9

func vecEqual(a, b []float64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if math.Abs(a[i]-b[i]) > eps {
			return false
		}
	}
	return true
}

func mustAdd(t *testing.T, g *Graph, src, dst int64, w float64) {
	t.Helper()
	if err := g.AddEdge(src, dst, w); err != nil {
		t.Fatalf("AddEdge(%d,%d,%g): %v", src, dst, w, err)
	}
}

// Distinct top-2: 1-2-4 = 3 (shortest), 1-3-4 = 6 (second). No third path.
// Verifies the basic top-k cost vector and that asking for more than exists
// returns only what exists.
func TestTopK_Distinct(t *testing.T) {
	g := New()
	mustAdd(t, g, 1, 2, 1)
	mustAdd(t, g, 2, 4, 2)
	mustAdd(t, g, 1, 3, 1)
	mustAdd(t, g, 3, 4, 5)

	got, err := g.TopK(1, 4, 2)
	if err != nil {
		t.Fatal(err)
	}
	if want := []float64{3, 6}; !vecEqual(got, want) {
		t.Fatalf("top-2: got %v want %v", got, want)
	}

	// k=5 but only 2 paths exist.
	got, err = g.TopK(1, 4, 5)
	if err != nil {
		t.Fatal(err)
	}
	if want := []float64{3, 6}; !vecEqual(got, want) {
		t.Fatalf("top-5 (only 2 exist): got %v want %v", got, want)
	}
}

// Tie case — the whole point of comparing weight vectors. Three paths:
//
//	10->40 direct      = 3   (shortest)
//	10-20-40           = 10
//	10-30-40           = 10  (ties with the above)
//
// Yen may return either tied path in either slot; the cost vector [3,10,10] is
// invariant. If this passes, ties cannot produce false correctness failures.
func TestTopK_Tie(t *testing.T) {
	g := New()
	mustAdd(t, g, 10, 40, 3)
	mustAdd(t, g, 10, 20, 5)
	mustAdd(t, g, 20, 40, 5)
	mustAdd(t, g, 10, 30, 5)
	mustAdd(t, g, 30, 40, 5)

	got, err := g.TopK(10, 40, 3)
	if err != nil {
		t.Fatal(err)
	}
	if want := []float64{3, 10, 10}; !vecEqual(got, want) {
		t.Fatalf("tie top-3: got %v want %v", got, want)
	}
}

// Parallel edges collapse to the minimum weight: adding a more expensive 1->2
// after the cheap one must not change the shortest cost.
func TestTopK_MinWeightDedup(t *testing.T) {
	g := New()
	mustAdd(t, g, 1, 2, 1)
	mustAdd(t, g, 1, 2, 100) // more expensive parallel edge — must be ignored
	mustAdd(t, g, 2, 4, 2)

	got, err := g.TopK(1, 4, 1)
	if err != nil {
		t.Fatal(err)
	}
	if want := []float64{3}; !vecEqual(got, want) {
		t.Fatalf("min-weight dedup: got %v want %v", got, want)
	}

	// Insertion order independence: cheaper edge added second must win too.
	g2 := New()
	mustAdd(t, g2, 1, 2, 100)
	mustAdd(t, g2, 1, 2, 1) // cheaper, added second
	mustAdd(t, g2, 2, 4, 2)
	got, err = g2.TopK(1, 4, 1)
	if err != nil {
		t.Fatal(err)
	}
	if want := []float64{3}; !vecEqual(got, want) {
		t.Fatalf("min-weight dedup (reverse order): got %v want %v", got, want)
	}
}

// Unreachable target yields an empty vector, not an error.
func TestTopK_Unreachable(t *testing.T) {
	g := New()
	mustAdd(t, g, 1, 2, 1)
	mustAdd(t, g, 3, 4, 1) // disconnected component
	// node 4 must exist for the lookup; it does (added as edge endpoint).
	got, err := g.TopK(1, 4, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("unreachable: got %v want empty", got)
	}
}

// Direction matters: an edge a->b does not imply b->a. Confirms we built a
// directed graph, matching Dgraph's directed `connected` traversal.
func TestTopK_Directed(t *testing.T) {
	g := New()
	mustAdd(t, g, 1, 2, 1)
	got, err := g.TopK(2, 1, 1) // reverse direction — no edge
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("directed: 2->1 should be unreachable, got %v", got)
	}
}

// Mirrors cmd/handprobe's hand graph exactly. Confirms the oracle predicts the
// LOOPLESS top-2 = [3, 6]; the disjoint alternative (1-5-4 = 20) is correctly
// NOT in the top 2. This is the number the gate probe checks Dgraph against.
func TestTopK_HandprobeGraph(t *testing.T) {
	g := New()
	mustAdd(t, g, 1, 2, 1)
	mustAdd(t, g, 2, 3, 1)
	mustAdd(t, g, 3, 4, 1)
	mustAdd(t, g, 2, 4, 5)
	mustAdd(t, g, 1, 5, 10)
	mustAdd(t, g, 5, 4, 10)

	got, err := g.TopK(1, 4, 2)
	if err != nil {
		t.Fatal(err)
	}
	if want := []float64{3, 6}; !vecEqual(got, want) {
		t.Fatalf("handprobe top-2: got %v want %v (loopless)", got, want)
	}
}

func TestAddEdge_NegativeWeight(t *testing.T) {
	g := New()
	if err := g.AddEdge(1, 2, -1); err == nil {
		t.Fatal("expected error on negative weight")
	}
}
