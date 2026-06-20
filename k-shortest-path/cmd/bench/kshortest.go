package main

import (
	"context"
	"errors"
	"log"
	"math"
	"math/rand"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dgraph-io/dgraph-benchmarks/k-shortest-path/internal/client"
	"github.com/dgraph-io/dgraph-benchmarks/k-shortest-path/internal/compare"
	"github.com/dgraph-io/dgraph-benchmarks/k-shortest-path/internal/ldbc"
	"github.com/dgraph-io/dgraph-benchmarks/k-shortest-path/internal/oracle"
	"github.com/dgraph-io/dgraph-benchmarks/k-shortest-path/internal/stats"
)

// kshortest mode is the top-k correctness discriminator. For each sampled
// target it compares Dgraph's returned path-cost VECTOR against the gonum Yen
// oracle's, across a sweep of maxfrontiersize values. Comparing sorted costs
// (not path identity) is tie-robust; the oracle is validated (internal/oracle
// tests) and the loopless-not-disjoint semantics confirmed (cmd/handprobe).
//
// The output JSON keeps full per-target detail (oracle vs Dgraph vectors,
// verdict, latency, self-consistency, the SSSP distance) so any aggregate can
// be re-derived and any individual failure audited later. It also splits
// timeouts out of the correctness denominator: a query that never returned is
// NOT "wrong", so we report correct/returned separately from correct/targets.
//
// Run once per PR binary against its alpha; aggregate the JSONs across PRs.

// kTarget is the full record of one (target, frontier) comparison.
type kTarget struct {
	GID            int64     `json:"gid"`
	SSSPDist       float64   `json:"sssp_dist"`
	DstUID         string    `json:"dst_uid"`
	OracleWeights  []float64 `json:"oracle_weights"`
	DgraphWeights  []float64 `json:"dgraph_weights"`
	PathCount      int       `json:"path_count"`
	Verdict        string    `json:"verdict"` // ok|count_mismatch|weight_mismatch|timeout|error
	WorstRelErr    float64   `json:"worst_rel_err"`
	SelfConsistent bool      `json:"self_consistent"`
	Loopless       bool      `json:"loopless"`
	MaxWeightErr   float64   `json:"max_weight_err"`
	LatencyMS      float64   `json:"latency_ms"`
	Err            string    `json:"err,omitempty"`
}

type kFrontierStat struct {
	MaxFrontier int `json:"max_frontier"` // 0 = unlimited
	Targets     int `json:"targets"`
	Returned    int `json:"returned"` // queries that came back (not timeout/other error)
	Correct     int `json:"correct"`
	// CorrectPct is correct/targets (timeouts counted as failures — pessimistic).
	CorrectPct float64 `json:"correct_pct"`
	// CorrectOfReturnedPct is correct/returned — the honest correctness among
	// queries that actually finished. This is the number to compare across PRs.
	CorrectOfReturnedPct float64       `json:"correct_of_returned_pct"`
	CountMismatch        int           `json:"count_mismatch"`
	WeightMismatch       int           `json:"weight_mismatch"`
	SelfInconsistent     int           `json:"self_inconsistent"`
	Timeouts             int           `json:"timeouts"`
	OtherErrors          int           `json:"other_errors"`
	Latency              stats.Summary `json:"latency"`
	TargetResults        []kTarget     `json:"target_results"`
}

type kResult struct {
	Label            string          `json:"label"` // e.g. "pr-9599@997d5dcb"
	Dataset          string          `json:"dataset"`
	Source           int64           `json:"source_vertex"`
	NumPaths         int             `json:"num_paths"`
	EdgePred         string          `json:"edge_pred"`
	MaxFrontierSweep []int           `json:"max_frontier_sweep"`
	Tolerance        float64         `json:"tolerance"`
	Seed             int64           `json:"seed"`
	BandLo           float64         `json:"band_lo"`
	BandHi           float64         `json:"band_hi"`
	GraphNodes       int             `json:"graph_nodes"`
	Directed         bool            `json:"directed"`
	CandidateTargets int             `json:"candidate_targets"`
	QualifiedTargets int             `json:"qualified_targets"`
	StartedUTC       string          `json:"started_utc"`
	Frontiers        []kFrontierStat `json:"frontiers"`
}

type kCand struct {
	gid  int64
	dist float64
}

func runKShortest(ctx context.Context, cfg config, c *client.Client, ds *ldbc.Dataset, uidMap map[int64]string) {
	numPaths := cfg.numPaths
	if numPaths < 2 {
		log.Printf("[kshortest] numpaths=%d; the top-k comparison is most meaningful at >=2 (set -numpaths 2)", numPaths)
	}
	frontiers := parseFrontiers(cfg.frontiers)

	source := ds.Properties.SourceVertex
	if _, ok := uidMap[source]; !ok {
		log.Fatalf("source vertex %d not in graph", source)
	}

	// Build the oracle graph from the same .e stream + directedness the
	// converter used, so the oracle traverses exactly what Dgraph traverses.
	log.Printf("[kshortest] building oracle graph from %s (directed=%v)...", ds.EdgeFile, ds.Properties.Directed)
	start := time.Now()
	g := oracle.New()
	directed := ds.Properties.Directed
	if err := ldbc.ScanEdges(ds.EdgeFile, func(e ldbc.Edge) error {
		if aerr := g.AddEdge(e.Src, e.Dst, e.Weight); aerr != nil {
			return aerr
		}
		if !directed {
			return g.AddEdge(e.Dst, e.Src, e.Weight)
		}
		return nil
	}); err != nil {
		log.Fatalf("build oracle graph: %v", err)
	}
	log.Printf("[kshortest] oracle graph: %d nodes in %s", g.Nodes(), time.Since(start).Round(time.Millisecond))

	// Select candidate targets from a distance band, not uniformly at random.
	// Uniform random picks targets thousands of hops away, where the numpaths=2
	// frontier explodes (queries time out) and Yen precompute is slow. A
	// near/moderate band keeps both fast while still exercising eviction — the
	// regime where the bug lives.
	candidates := bandedTargets(ds.SSSPRefFile, uidMap, source, cfg.bandLo, cfg.bandHi, cfg.targets, cfg.seed)
	type pair struct {
		gid    int64
		dist   float64
		uid    string
		oracle []float64
	}
	var qualified []pair
	log.Printf("[kshortest] precomputing oracle top-%d for %d candidate targets...", numPaths, len(candidates))
	preStart := time.Now()
	for i, cand := range candidates {
		vec, err := g.TopK(source, cand.gid, numPaths)
		if err != nil || len(vec) < 2 {
			continue
		}
		qualified = append(qualified, pair{gid: cand.gid, dist: cand.dist, uid: uidMap[cand.gid], oracle: vec})
		if (i+1)%50 == 0 {
			log.Printf("[kshortest] oracle precompute %d/%d (%d qualified) elapsed=%s",
				i+1, len(candidates), len(qualified), time.Since(preStart).Round(time.Second))
		}
	}
	if len(qualified) == 0 {
		log.Fatal("[kshortest] no targets with >=2 oracle paths — try more -targets or a different -source")
	}
	log.Printf("[kshortest] %d/%d targets qualified (>=2 paths) in %s",
		len(qualified), len(candidates), time.Since(preStart).Round(time.Second))

	res := kResult{
		Label:            cfg.label,
		Dataset:          ds.Name,
		Source:           source,
		NumPaths:         numPaths,
		EdgePred:         cfg.edgePred,
		MaxFrontierSweep: frontiers,
		Tolerance:        cfg.tol,
		Seed:             cfg.seed,
		BandLo:           cfg.bandLo,
		BandHi:           cfg.bandHi,
		GraphNodes:       g.Nodes(),
		Directed:         directed,
		CandidateTargets: len(candidates),
		QualifiedTargets: len(qualified),
		StartedUTC:       time.Now().UTC().Format(time.RFC3339),
	}

	srcUID := uidMap[source]
	// Sweep largest frontier first: if a binary OOMs/hangs at a big frontier,
	// you learn it before spending time on the cheaper ones.
	for _, fr := range frontiers {
		stat := kFrontierStat{MaxFrontier: fr, Targets: len(qualified)}
		rec := stats.New()
		swStart := time.Now()
		for i, p := range qualified {
			sr, err := c.Shortest(ctx, client.ShortestOptions{
				SrcUID:      srcUID,
				DstUID:      p.uid,
				EdgePred:    cfg.edgePred,
				NumPaths:    numPaths,
				MaxFrontier: fr,
				Timeout:     cfg.timeout,
			})
			tr := kTarget{
				GID: p.gid, SSSPDist: p.dist, DstUID: p.uid,
				OracleWeights: p.oracle,
				LatencyMS:     float64(sr.Latency.Microseconds()) / 1000.0,
			}
			if err != nil {
				rec.RecordError()
				kind := classifyErr(err)
				tr.Verdict = kind
				tr.Err = err.Error()
				if kind == "timeout" {
					stat.Timeouts++
				} else {
					stat.OtherErrors++
				}
				stat.TargetResults = append(stat.TargetResults, tr)
				continue
			}
			rec.Record(sr.Latency)
			stat.Returned++
			tr.DgraphWeights = sr.Weights
			tr.PathCount = sr.PathCount
			tr.SelfConsistent = sr.SelfConsistent
			tr.Loopless = sr.Loopless
			tr.MaxWeightErr = sr.MaxWeightErr
			if !sr.SelfConsistent || !sr.Loopless {
				stat.SelfInconsistent++
			}
			r := compare.Vectors(p.oracle, sr.Weights, cfg.tol)
			tr.WorstRelErr = r.WorstRelErr
			switch r.Verdict {
			case compare.OK:
				stat.Correct++
				tr.Verdict = "ok"
			case compare.CountMismatch:
				stat.CountMismatch++
				tr.Verdict = "count_mismatch"
			case compare.WeightMismatch:
				stat.WeightMismatch++
				tr.Verdict = "weight_mismatch"
			}
			stat.TargetResults = append(stat.TargetResults, tr)
			// Heartbeat inside a frontier: a 100-target phase with 60s timeouts
			// can run quiet for 15+ min otherwise, which looks like a hang.
			if (i+1)%25 == 0 {
				log.Printf("[kshortest]   frontier=%s progress %d/%d (correct=%d wt_mm=%d cnt_mm=%d self_bad=%d timeout=%d)",
					frontierLabel(fr), i+1, len(qualified), stat.Correct, stat.WeightMismatch,
					stat.CountMismatch, stat.SelfInconsistent, stat.Timeouts)
			}
		}
		stat.Latency = rec.Summarize(time.Since(swStart))
		if stat.Targets > 0 {
			stat.CorrectPct = 100 * float64(stat.Correct) / float64(stat.Targets)
		}
		if stat.Returned > 0 {
			stat.CorrectOfReturnedPct = 100 * float64(stat.Correct) / float64(stat.Returned)
		}
		res.Frontiers = append(res.Frontiers, stat)
		log.Printf("[kshortest] frontier=%-9s correct=%d/%d returned=%d (%.1f%% of returned) wt_mm=%d cnt_mm=%d self_bad=%d timeout=%d err=%d p50=%s p95=%s",
			frontierLabel(fr), stat.Correct, stat.Targets, stat.Returned, stat.CorrectOfReturnedPct,
			stat.WeightMismatch, stat.CountMismatch, stat.SelfInconsistent, stat.Timeouts, stat.OtherErrors,
			stat.Latency.P50.Round(time.Millisecond), stat.Latency.P95.Round(time.Millisecond))
		// Persist after EACH frontier so a kill/drop mid-branch keeps the
		// frontiers done so far, instead of losing the whole branch.
		writeJSON(cfg.out, res)
	}

	printKTable(res)
}

// classifyErr distinguishes a query timeout (the binary didn't terminate within
// the deadline) from any other error. A timeout is NOT a wrong answer, so it's
// reported separately rather than folded into the correctness denominator.
func classifyErr(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	s := strings.ToLower(err.Error())
	if strings.Contains(s, "deadline") || strings.Contains(s, "timeout") {
		return "timeout"
	}
	return "error"
}

func printKTable(res kResult) {
	log.Printf("[kshortest] === %s  %s  source=%d  numpaths=%d  tol=%g  qualified=%d ===",
		res.Label, res.Dataset, res.Source, res.NumPaths, res.Tolerance, res.QualifiedTargets)
	log.Printf("[kshortest] %-9s | %-12s | %-8s | %-6s | %-6s | %-8s | %-8s | %-7s | %-8s",
		"frontier", "correct/ret", "ret%", "wt_mm", "cnt_mm", "self_bad", "timeout", "err", "p95")
	for _, s := range res.Frontiers {
		log.Printf("[kshortest] %-9s | %4d/%-7d | %7.1f%% | %-6d | %-6d | %-8d | %-8d | %-7d | %-8s",
			frontierLabel(s.MaxFrontier), s.Correct, s.Returned, s.CorrectOfReturnedPct,
			s.WeightMismatch, s.CountMismatch, s.SelfInconsistent, s.Timeouts, s.OtherErrors,
			s.Latency.P95.Round(time.Millisecond))
	}
}

func frontierLabel(fr int) string {
	if fr <= 0 {
		return "unlimited"
	}
	return strconv.Itoa(fr)
}

func parseFrontiers(s string) []int {
	var out []int
	for _, tok := range strings.Split(s, ",") {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		n, err := strconv.Atoi(tok)
		if err != nil {
			log.Fatalf("bad -frontiers value %q: %v", tok, err)
		}
		out = append(out, n)
	}
	if len(out) == 0 {
		log.Fatal("-frontiers produced no values")
	}
	return out
}

// bandedTargets selects up to n target vertices whose SSSP reference distance
// falls in the percentile window [bandLo, bandHi] of the distance-sorted
// reachable set. This avoids the far-target frontier explosion (which times out
// queries and makes Yen precompute slow) while still hitting multi-hop targets
// where eviction — and the bug — is exercised. Deterministic for a given seed.
func bandedTargets(ssspFile string, uidMap map[int64]string, source int64, bandLo, bandHi float64, n int, seed int64) []kCand {
	ref, err := ldbc.ReadSSSP(ssspFile)
	if err != nil {
		log.Fatalf("[kshortest] banded target selection needs the SSSP reference (%s): %v", ssspFile, err)
	}
	reach := make([]kCand, 0, len(ref))
	for gid, d := range ref {
		if gid == source || d <= 0 || math.IsInf(d, +1) {
			continue
		}
		if _, ok := uidMap[gid]; ok {
			reach = append(reach, kCand{gid, d})
		}
	}
	if len(reach) == 0 {
		log.Fatal("[kshortest] no reachable targets in SSSP reference")
	}
	sort.Slice(reach, func(i, j int) bool {
		if reach[i].dist != reach[j].dist {
			return reach[i].dist < reach[j].dist
		}
		return reach[i].gid < reach[j].gid // stable tie-break for determinism
	})

	lo := int(bandLo * float64(len(reach)))
	hi := int(bandHi * float64(len(reach)))
	lo = max(lo, 0)
	hi = min(hi, len(reach))
	if lo >= hi { // degenerate band — fall back to a single rank
		lo = min(lo, len(reach)-1)
		hi = lo + 1
	}
	band := reach[lo:hi]
	log.Printf("[kshortest] target band [%.4f,%.4f] = ranks %d..%d of %d reachable (dist %.0f..%.0f)",
		bandLo, bandHi, lo, hi, len(reach), band[0].dist, band[len(band)-1].dist)

	idx := make([]int, len(band))
	for i := range idx {
		idx[i] = i
	}
	rng := rand.New(rand.NewSource(seed))
	rng.Shuffle(len(idx), func(i, j int) { idx[i], idx[j] = idx[j], idx[i] })
	if n > 0 && n < len(idx) {
		idx = idx[:n]
	}
	out := make([]kCand, 0, len(idx))
	for _, i := range idx {
		out = append(out, band[i])
	}
	return out
}
