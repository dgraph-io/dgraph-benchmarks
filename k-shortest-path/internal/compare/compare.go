// Package compare diffs a Dgraph k-shortest weight vector against the oracle's.
//
// The comparison is on sorted total-path COSTS, never on path identity. With
// road-network weights many distinct paths tie on cost, and Dgraph and the
// oracle (gonum Yen) break ties differently — so requiring identical paths
// would manufacture false failures. The k-th smallest cost, however, is
// deterministic, so comparing the sorted cost vectors is both sound and
// tie-robust.
package compare

import "math"

// Verdict classifies a single (src,dst) comparison.
type Verdict int

const (
	// OK: Dgraph returned the same number of paths as the oracle and every
	// cost matches within tolerance.
	OK Verdict = iota
	// CountMismatch: Dgraph returned a different number of paths than the
	// oracle — typically dropping paths under a frontier cap.
	CountMismatch
	// WeightMismatch: same count, but at least one cost differs beyond
	// tolerance — Dgraph substituted a worse (or wrong) path for a real one.
	WeightMismatch
)

func (v Verdict) String() string {
	switch v {
	case OK:
		return "ok"
	case CountMismatch:
		return "count_mismatch"
	case WeightMismatch:
		return "weight_mismatch"
	default:
		return "unknown"
	}
}

// Result is the outcome of comparing one weight vector pair.
type Result struct {
	Verdict Verdict
	OracleK int // number of paths the oracle found (== len(oracle))
	DgraphK int // number of paths Dgraph returned (== len(got))
	// WorstRelErr is the largest per-rank relative error among the ranks both
	// vectors cover. 0 when counts differ and no overlap mismatches.
	WorstRelErr float64
}

// Correct reports whether the comparison passed.
func (r Result) Correct() bool { return r.Verdict == OK }

// Vectors compares the oracle's expected cost vector against Dgraph's returned
// cost vector. Both must already be sorted non-decreasing (oracle.TopK and
// client.ShortestResult.Weights both guarantee this). tol is a relative
// tolerance (e.g. 1e-4); an exact-equal absolute path is taken first so that
// two empty vectors, or identical integer-valued costs, always pass.
func Vectors(oracle, got []float64, tol float64) Result {
	r := Result{OracleK: len(oracle), DgraphK: len(got)}
	if len(oracle) != len(got) {
		// Still surface the worst error over the overlapping prefix — useful
		// for spotting whether the paths Dgraph *did* return are also wrong.
		r.WorstRelErr = worstRelErr(oracle, got)
		r.Verdict = CountMismatch
		return r
	}
	worst := worstRelErr(oracle, got)
	r.WorstRelErr = worst
	if worst > tol {
		r.Verdict = WeightMismatch
		return r
	}
	r.Verdict = OK
	return r
}

func worstRelErr(a, b []float64) float64 {
	n := min(len(a), len(b))
	worst := 0.0
	for i := 0; i < n; i++ {
		if e := relErr(a[i], b[i]); e > worst {
			worst = e
		}
	}
	return worst
}

func relErr(expected, got float64) float64 {
	if expected == got { // covers both-zero and both-Inf
		return 0
	}
	if math.IsInf(expected, 0) || math.IsInf(got, 0) {
		return math.Inf(+1)
	}
	denom := math.Abs(expected)
	if denom == 0 {
		denom = 1
	}
	return math.Abs(expected-got) / denom
}
