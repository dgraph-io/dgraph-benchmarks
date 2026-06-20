// Command bench is the workhorse: two modes, --mode=correctness and
// --mode=perf, both running against a Dgraph cluster already populated by
// `dgraph bulk` (see cmd/convert).
//
// correctness mode:
//
//	full SSSP-style sweep from the dataset's declared source vertex to a
//	sampled set of targets, distances diffed against the LDBC reference
//	output with 0.01% epsilon. The discriminator for any PR that throttles
//	or evicts incorrectly under maxfrontiersize.
//
// perf mode:
//
//	N (src,dst) pairs sampled uniformly from the vertex set, dispatched
//	through a worker pool. Records p50/p95/p99 latency + QPS.
//
// Output is one JSON file per run, intended to be aggregated across PRs
// with `cmd/compare` (TODO) or eyeballed directly.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"time"

	"github.com/dgraph-io/dgraph-benchmarks/k-shortest-path/internal/client"
	"github.com/dgraph-io/dgraph-benchmarks/k-shortest-path/internal/ldbc"
	"github.com/dgraph-io/dgraph-benchmarks/k-shortest-path/internal/stats"
)

const epsilon = 0.0001 // LDBC's 0.01% tolerance.

type config struct {
	mode        string
	datasetDir  string
	alpha       string
	out         string
	maxFrontier int
	numPaths    int
	edgePred    string
	timeout     time.Duration
	seed        int64

	// correctness flags
	targets int

	// perf flags
	pairs       int
	concurrency int

	// kshortest flags
	frontiers string
	tol       float64
	bandLo    float64
	bandHi    float64
	label     string

	// uid-map cache
	uidMapCache    string
	refreshUIDMap  bool
}

func main() {
	cfg := config{}
	flag.StringVar(&cfg.mode, "mode", "perf", "correctness | perf | kshortest")
	flag.StringVar(&cfg.datasetDir, "dataset", "", "path to extracted LDBC dataset")
	flag.StringVar(&cfg.alpha, "alpha", "localhost:9080", "Dgraph alpha gRPC address")
	flag.StringVar(&cfg.out, "out", "", "output JSON path (defaults to results/<mode>.json)")
	flag.IntVar(&cfg.maxFrontier, "maxfrontier", 0, "maxfrontiersize for shortest queries (0 = unset)")
	flag.IntVar(&cfg.numPaths, "numpaths", 1, "numpaths for shortest queries")
	flag.StringVar(&cfg.edgePred, "edge", "connected", "DQL edge predicate for shortest")
	flag.DurationVar(&cfg.timeout, "timeout", 60*time.Second, "per-query timeout")
	flag.Int64Var(&cfg.seed, "seed", 1, "RNG seed for sampling")
	flag.IntVar(&cfg.targets, "targets", 1000, "[correctness] number of target vertices to sample (-1 = all)")
	flag.IntVar(&cfg.pairs, "pairs", 5000, "[perf] number of (src,dst) pairs")
	flag.IntVar(&cfg.concurrency, "concurrency", runtime.GOMAXPROCS(0), "worker count (applies to both correctness and perf modes)")
	flag.StringVar(&cfg.uidMapCache, "uidmap-cache", "", "path to cached uid map TSV (defaults to <dataset>/dgraph/uid-map.tsv). Reused across PR-build swaps to skip the slow refetch.")
	flag.BoolVar(&cfg.refreshUIDMap, "refresh-uidmap", false, "ignore the uid map cache and refetch from Dgraph (do this after a fresh bulk-load)")
	flag.StringVar(&cfg.frontiers, "frontiers", "0,10000,5000,1000,500,200,100", "[kshortest] comma-separated maxfrontiersize sweep; 0 = unlimited")
	flag.Float64Var(&cfg.tol, "tol", 0.0001, "[kshortest] relative tolerance for weight-vector comparison")
	flag.Float64Var(&cfg.bandLo, "band-lo", 0.0005, "[kshortest] low edge of the SSSP-distance band to draw targets from (fraction of distance-sorted reachable vertices)")
	flag.Float64Var(&cfg.bandHi, "band-hi", 0.01, "[kshortest] high edge of the SSSP-distance band; far targets blow up the numpaths=2 frontier and time out, so keep this small")
	flag.StringVar(&cfg.label, "label", "", "[kshortest] free-form label embedded in the result JSON (e.g. branch@sha) so each file is self-identifying")
	flag.Parse()

	if cfg.datasetDir == "" {
		log.Fatal("-dataset is required")
	}
	if cfg.out == "" {
		cfg.out = filepath.Join("results", cfg.mode+".json")
	}
	if err := os.MkdirAll(filepath.Dir(cfg.out), 0o755); err != nil {
		log.Fatal(err)
	}

	ds, err := ldbc.LoadDataset(cfg.datasetDir)
	if err != nil {
		log.Fatalf("load dataset: %v", err)
	}

	c, err := client.Open(cfg.alpha)
	if err != nil {
		log.Fatalf("open client: %v", err)
	}
	defer c.Close()

	ctx := context.Background()
	if cfg.uidMapCache == "" {
		cfg.uidMapCache = filepath.Join(cfg.datasetDir, "dgraph", "uid-map.tsv")
	}

	var uidMap map[int64]string
	if !cfg.refreshUIDMap {
		if m, lerr := client.LoadUIDMap(cfg.uidMapCache); lerr == nil {
			log.Printf("[bench] loaded uid map from cache %s: %d entries", cfg.uidMapCache, len(m))
			uidMap = m
		}
	}
	if uidMap == nil {
		log.Printf("[bench] fetching graphalytics_id → uid map from Dgraph (will cache to %s)...", cfg.uidMapCache)
		start := time.Now()
		m, ferr := c.FetchUIDMap(ctx, 10000)
		if ferr != nil {
			log.Fatalf("uid map: %v", ferr)
		}
		log.Printf("[bench] uid map: %d entries in %s", len(m), time.Since(start).Round(time.Millisecond))
		uidMap = m
		// Never cache an empty map: a transient "alpha serving no data" would
		// otherwise poison the cache and make every subsequent branch fail.
		if len(uidMap) == 0 {
			log.Fatal("uid map empty — alpha has no graphalytics_id nodes (cluster not serving the bulk p/?). NOT caching the empty result; fix the cluster and retry.")
		}
		if err := os.MkdirAll(filepath.Dir(cfg.uidMapCache), 0o755); err == nil {
			if serr := client.SaveUIDMap(cfg.uidMapCache, uidMap); serr != nil {
				log.Printf("[bench] warning: failed to save uid map cache to %s: %v", cfg.uidMapCache, serr)
			} else {
				log.Printf("[bench] saved uid map cache to %s", cfg.uidMapCache)
			}
		}
	}
	if len(uidMap) == 0 {
		log.Fatal("uid map empty — did you run cmd/convert and dgraph bulk?")
	}

	switch cfg.mode {
	case "correctness":
		runCorrectness(ctx, cfg, c, ds, uidMap)
	case "perf":
		runPerf(ctx, cfg, c, uidMap)
	case "kshortest":
		runKShortest(ctx, cfg, c, ds, uidMap)
	default:
		log.Fatalf("unknown mode %q", cfg.mode)
	}
}

// -----------------------------------------------------------------------------
// correctness mode
// -----------------------------------------------------------------------------

type correctnessFailure struct {
	Vertex   int64
	Expected float64
	Got      float64
	RelErr   float64
}

func (f correctnessFailure) MarshalJSON() ([]byte, error) {
	type out struct {
		Vertex   int64   `json:"vertex"`
		Expected any     `json:"expected"`
		Got      any     `json:"got"`
		RelErr   float64 `json:"rel_err,omitempty"`
	}
	return json.Marshal(out{
		Vertex: f.Vertex, Expected: jsonDist(f.Expected), Got: jsonDist(f.Got), RelErr: f.RelErr,
	})
}

func jsonDist(d float64) any {
	if math.IsInf(d, 1) {
		return "Infinity"
	}
	if math.IsInf(d, -1) {
		return "-Infinity"
	}
	return d
}

type correctnessResult struct {
	Dataset      string               `json:"dataset"`
	SourceVertex int64                `json:"source_vertex"`
	MaxFrontier  int                  `json:"max_frontier"`
	Concurrency  int                  `json:"concurrency"`
	Sampled      int                  `json:"targets_sampled"`
	Epsilon      float64              `json:"epsilon"`
	Passed       int                  `json:"passed"`
	Failed       int                  `json:"failed"`
	InfMismatch  int                  `json:"infinity_mismatches"`
	QueryErrors  int                  `json:"query_errors"`
	Latency      stats.Summary        `json:"latency"`
	FirstFails   []correctnessFailure `json:"first_failures,omitempty"`
}

func runCorrectness(ctx context.Context, cfg config, c *client.Client, ds *ldbc.Dataset, uidMap map[int64]string) {
	if _, err := os.Stat(ds.SSSPRefFile); err != nil {
		log.Fatalf("reference SSSP file missing at %s — extract validation/ archive first", ds.SSSPRefFile)
	}
	ref, err := ldbc.ReadSSSP(ds.SSSPRefFile)
	if err != nil {
		log.Fatalf("read reference: %v", err)
	}
	srcUID, ok := uidMap[ds.Properties.SourceVertex]
	if !ok {
		log.Fatalf("source vertex %d not in graph", ds.Properties.SourceVertex)
	}

	targets := pickTargets(ref, uidMap, ds.Properties.SourceVertex, cfg.targets, cfg.seed)
	log.Printf("[correctness] source=%d targets=%d maxfrontier=%d concurrency=%d",
		ds.Properties.SourceVertex, len(targets), cfg.maxFrontier, cfg.concurrency)

	rec := stats.New()
	res := correctnessResult{
		Dataset:      ds.Name,
		SourceVertex: ds.Properties.SourceVertex,
		MaxFrontier:  cfg.maxFrontier,
		Concurrency:  cfg.concurrency,
		Sampled:      len(targets),
		Epsilon:      epsilon,
	}

	type queryResult struct {
		tgt int64
		sr  client.ShortestResult
		err error
	}

	jobs := make(chan int64, cfg.concurrency*2)
	out := make(chan queryResult, cfg.concurrency*2)
	var wg sync.WaitGroup
	for w := 0; w < cfg.concurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for tgt := range jobs {
				dstUID := uidMap[tgt]
				sr, err := c.Shortest(ctx, client.ShortestOptions{
					SrcUID:      srcUID,
					DstUID:      dstUID,
					EdgePred:    cfg.edgePred,
					NumPaths:    cfg.numPaths,
					MaxFrontier: cfg.maxFrontier,
					Timeout:     cfg.timeout,
				})
				out <- queryResult{tgt: tgt, sr: sr, err: err}
			}
		}()
	}
	go func() {
		for _, t := range targets {
			jobs <- t
		}
		close(jobs)
		wg.Wait()
		close(out)
	}()

	startAll := time.Now()
	lastLog := startAll
	const logEvery = 10
	done := 0
	for qr := range out {
		done++
		expected := ref[qr.tgt]
		switch {
		case qr.err != nil:
			rec.RecordError()
			res.QueryErrors++
		default:
			rec.Record(qr.sr.Latency)
			switch {
			case math.IsInf(expected, +1) && math.IsInf(qr.sr.Distance, +1):
				res.Passed++
			case math.IsInf(expected, +1) != math.IsInf(qr.sr.Distance, +1):
				res.InfMismatch++
				res.Failed++
				if len(res.FirstFails) < 20 {
					res.FirstFails = append(res.FirstFails, correctnessFailure{
						Vertex: qr.tgt, Expected: expected, Got: qr.sr.Distance,
					})
				}
			case relErr(expected, qr.sr.Distance) > epsilon:
				res.Failed++
				if len(res.FirstFails) < 20 {
					res.FirstFails = append(res.FirstFails, correctnessFailure{
						Vertex: qr.tgt, Expected: expected, Got: qr.sr.Distance,
						RelErr: relErr(expected, qr.sr.Distance),
					})
				}
			default:
				res.Passed++
			}
		}
		if done%logEvery == 0 || done == len(targets) || time.Since(lastLog) >= 10*time.Second {
			elapsed := time.Since(startAll)
			rate := float64(done) / elapsed.Seconds()
			var eta time.Duration
			if rate > 0 {
				eta = time.Duration(float64(len(targets)-done)/rate) * time.Second
			}
			log.Printf("[correctness] %d/%d done elapsed=%s last_latency=%s rate=%.1f q/s eta=%s passed=%d failed=%d errors=%d",
				done, len(targets), elapsed.Round(time.Second),
				qr.sr.Latency.Round(time.Millisecond), rate, eta.Round(time.Second),
				res.Passed, res.Failed, res.QueryErrors)
			lastLog = time.Now()
		}
	}
	res.Latency = rec.Summarize(time.Since(startAll))

	writeJSON(cfg.out, res)
	log.Printf("[correctness] passed=%d failed=%d inf_mismatch=%d errors=%d",
		res.Passed, res.Failed, res.InfMismatch, res.QueryErrors)
	log.Printf("[correctness] latency p50=%s p95=%s p99=%s",
		res.Latency.P50, res.Latency.P95, res.Latency.P99)
}

func pickTargets(ref map[int64]float64, uidMap map[int64]string, source int64, n int, seed int64) []int64 {
	all := make([]int64, 0, len(ref))
	for v := range ref {
		if v == source {
			continue
		}
		if _, ok := uidMap[v]; !ok {
			continue
		}
		all = append(all, v)
	}
	// Go's map iteration is randomized per program start, so we must sort the
	// candidate slice before shuffling — otherwise the same -seed produces
	// different target sets across runs, and per-PR comparisons aren't fair.
	sort.Slice(all, func(i, j int) bool { return all[i] < all[j] })
	if n <= 0 || n >= len(all) {
		return all
	}
	rng := rand.New(rand.NewSource(seed))
	rng.Shuffle(len(all), func(i, j int) { all[i], all[j] = all[j], all[i] })
	return all[:n]
}

func relErr(expected, got float64) float64 {
	if expected == 0 {
		if got == 0 {
			return 0
		}
		return math.Abs(got)
	}
	return math.Abs(expected-got) / math.Abs(expected)
}

// -----------------------------------------------------------------------------
// perf mode
// -----------------------------------------------------------------------------

type perfResult struct {
	Dataset     string        `json:"dataset"`
	MaxFrontier int           `json:"max_frontier"`
	NumPaths    int           `json:"num_paths"`
	Pairs       int           `json:"pairs"`
	Concurrency int           `json:"concurrency"`
	Latency     stats.Summary `json:"latency"`
	HeapMaxMB   uint64        `json:"heap_max_mb"`
}

func runPerf(ctx context.Context, cfg config, c *client.Client, uidMap map[int64]string) {
	ids := make([]int64, 0, len(uidMap))
	for k := range uidMap {
		ids = append(ids, k)
	}
	// Sort for the same reason as in pickTargets: Go map iteration is
	// randomized, so without sorting, the same -seed gives different pair
	// samples across runs.
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	if len(ids) < 2 {
		log.Fatal("perf needs at least 2 vertices")
	}
	rng := rand.New(rand.NewSource(cfg.seed))
	pairs := make([][2]string, cfg.pairs)
	for i := range pairs {
		a := ids[rng.Intn(len(ids))]
		b := ids[rng.Intn(len(ids))]
		for a == b {
			b = ids[rng.Intn(len(ids))]
		}
		pairs[i] = [2]string{uidMap[a], uidMap[b]}
	}

	log.Printf("[perf] pairs=%d concurrency=%d maxfrontier=%d numpaths=%d",
		cfg.pairs, cfg.concurrency, cfg.maxFrontier, cfg.numPaths)

	rec := stats.New()
	jobs := make(chan [2]string, cfg.concurrency*2)
	var wg sync.WaitGroup
	var heapPeak uint64
	var heapMu sync.Mutex

	for w := 0; w < cfg.concurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range jobs {
				sr, err := c.Shortest(ctx, client.ShortestOptions{
					SrcUID:      p[0],
					DstUID:      p[1],
					EdgePred:    cfg.edgePred,
					NumPaths:    cfg.numPaths,
					MaxFrontier: cfg.maxFrontier,
					Timeout:     cfg.timeout,
				})
				if err != nil {
					rec.RecordError()
					continue
				}
				rec.Record(sr.Latency)
			}
		}()
	}

	stopHeap := make(chan struct{})
	go func() {
		t := time.NewTicker(500 * time.Millisecond)
		defer t.Stop()
		var ms runtime.MemStats
		for {
			select {
			case <-stopHeap:
				return
			case <-t.C:
				runtime.ReadMemStats(&ms)
				heapMu.Lock()
				if ms.HeapAlloc > heapPeak {
					heapPeak = ms.HeapAlloc
				}
				heapMu.Unlock()
			}
		}
	}()

	startAll := time.Now()
	for _, p := range pairs {
		jobs <- p
	}
	close(jobs)
	wg.Wait()
	close(stopHeap)
	wall := time.Since(startAll)

	res := perfResult{
		Dataset:     filepath.Base(cfg.datasetDir),
		MaxFrontier: cfg.maxFrontier,
		NumPaths:    cfg.numPaths,
		Pairs:       cfg.pairs,
		Concurrency: cfg.concurrency,
		Latency:     rec.Summarize(wall),
		HeapMaxMB:   heapPeak / (1024 * 1024),
	}
	writeJSON(cfg.out, res)
	log.Printf("[perf] qps=%.0f p50=%s p95=%s p99=%s heap=%dMB errors=%d",
		res.Latency.QPS, res.Latency.P50, res.Latency.P95, res.Latency.P99,
		res.HeapMaxMB, res.Latency.Errors)
}

// writeJSON writes v atomically: encode to <path>.tmp, then rename over <path>.
// Atomicity matters because kshortest now writes after every frontier — a kill
// mid-encode must not corrupt or truncate the last-good result.
func writeJSON(path string, v any) {
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		log.Fatalf("create %s: %v", tmp, err)
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		f.Close()
		log.Fatalf("encode %s: %v", tmp, err)
	}
	if err := f.Close(); err != nil {
		log.Fatalf("close %s: %v", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		log.Fatalf("rename %s -> %s: %v", tmp, path, err)
	}
}
