package compare

import "testing"

const tol = 1e-4

func TestVectors_Exact(t *testing.T) {
	r := Vectors([]float64{3, 6}, []float64{3, 6}, tol)
	if !r.Correct() || r.Verdict != OK {
		t.Fatalf("expected OK, got %v", r.Verdict)
	}
}

// Tie vector: identical costs at ranks 2 and 3. Must pass — this is the whole
// reason we compare costs not paths.
func TestVectors_Tie(t *testing.T) {
	r := Vectors([]float64{3, 10, 10}, []float64{3, 10, 10}, tol)
	if !r.Correct() {
		t.Fatalf("tie vector should be OK, got %v (worst=%g)", r.Verdict, r.WorstRelErr)
	}
}

// Dgraph dropped a path under a frontier cap.
func TestVectors_CountMismatch(t *testing.T) {
	r := Vectors([]float64{3, 6}, []float64{3}, tol)
	if r.Verdict != CountMismatch {
		t.Fatalf("expected count_mismatch, got %v", r.Verdict)
	}
	if r.OracleK != 2 || r.DgraphK != 1 {
		t.Fatalf("counts wrong: oracle=%d dgraph=%d", r.OracleK, r.DgraphK)
	}
}

// Same count, but Dgraph substituted a worse second path (the STATUS.md bug
// shape: returns a path far longer than the true one).
func TestVectors_WeightMismatch(t *testing.T) {
	r := Vectors([]float64{4058.67, 4100.0}, []float64{4058.67, 17636.65}, tol)
	if r.Verdict != WeightMismatch {
		t.Fatalf("expected weight_mismatch, got %v", r.Verdict)
	}
	if r.WorstRelErr <= tol {
		t.Fatalf("worst rel err should exceed tol, got %g", r.WorstRelErr)
	}
}

// Within tolerance counts as OK (float noise on summed facets).
func TestVectors_WithinTolerance(t *testing.T) {
	r := Vectors([]float64{4058.67}, []float64{4058.6701}, tol)
	if !r.Correct() {
		t.Fatalf("tiny diff should pass, got %v (worst=%g)", r.Verdict, r.WorstRelErr)
	}
}

// Both unreachable (empty vectors) is a correct match.
func TestVectors_BothEmpty(t *testing.T) {
	r := Vectors(nil, nil, tol)
	if !r.Correct() {
		t.Fatalf("both empty should be OK, got %v", r.Verdict)
	}
}

// Oracle found a path, Dgraph found none — count mismatch, not a silent pass.
func TestVectors_DgraphEmpty(t *testing.T) {
	r := Vectors([]float64{3}, nil, tol)
	if r.Verdict != CountMismatch {
		t.Fatalf("expected count_mismatch, got %v", r.Verdict)
	}
}
