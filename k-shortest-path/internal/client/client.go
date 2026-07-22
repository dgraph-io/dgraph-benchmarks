// Package client wraps a dgo gRPC client with the few helpers shortest-path
// benchmarks need: schema setup, a graphalytics_id → uid map fetched in one
// query, and a Shortest(...) call that returns the total path weight.
package client

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dgraph-io/dgo/v250"
	"github.com/dgraph-io/dgo/v250/protos/api"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Schema is the DQL schema applied before any benchmark run. graphalytics_id
// is the LDBC vertex id; connected carries the weight as a facet.
const Schema = `
graphalytics_id: int @index(int) .
connected: [uid] @reverse .
`

type Client struct {
	conn *grpc.ClientConn
	dg   *dgo.Dgraph
}

// Open dials the Alpha at addr (typically "localhost:9080") with insecure
// credentials.
func Open(addr string) (*Client, error) {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("grpc dial %s: %w", addr, err)
	}
	return &Client{
		conn: conn,
		dg:   dgo.NewDgraphClient(api.NewDgraphClient(conn)),
	}, nil
}

func (c *Client) Close() error { return c.conn.Close() }

// Alter applies the package Schema.
func (c *Client) Alter(ctx context.Context) error {
	return c.dg.Alter(ctx, &api.Operation{Schema: Schema})
}

// FetchUIDMap returns a graphalytics_id → uid mapping for every Vertex
// node in the database. Uses paginated reads so the query never returns
// more than `pageSize` results at once.
func (c *Client) FetchUIDMap(ctx context.Context, pageSize int) (map[int64]string, error) {
	out := make(map[int64]string)
	offset := 0
	for {
		q := fmt.Sprintf(`
		{
			vertices(func: has(graphalytics_id), first: %d, offset: %d) {
				uid
				graphalytics_id
			}
		}`, pageSize, offset)

		txn := c.dg.NewReadOnlyTxn().BestEffort()
		resp, err := txn.Query(ctx, q)
		_ = txn.Discard(ctx)
		if err != nil {
			return nil, fmt.Errorf("uidmap page offset=%d: %w", offset, err)
		}

		var page struct {
			Vertices []struct {
				UID            string `json:"uid"`
				GraphalyticsID int64  `json:"graphalytics_id"`
			} `json:"vertices"`
		}
		if err := json.Unmarshal(resp.Json, &page); err != nil {
			return nil, fmt.Errorf("uidmap decode: %w", err)
		}
		if len(page.Vertices) == 0 {
			break
		}
		for _, v := range page.Vertices {
			out[v.GraphalyticsID] = v.UID
		}
		if len(page.Vertices) < pageSize {
			break
		}
		offset += pageSize
	}
	return out, nil
}

// SaveUIDMap writes the graphalytics_id → uid map to a TSV file at path.
// Format: one line per entry, "<graphalytics_id>\t<uid>". This cache is valid
// as long as the underlying Dgraph p/ directory isn't re-bulk-loaded — UIDs
// persist across Alpha restarts and PR-build swaps.
func SaveUIDMap(path string, m map[int64]string) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	defer f.Close()
	bw := bufio.NewWriter(f)
	defer bw.Flush()
	for gid, uid := range m {
		if _, err := fmt.Fprintf(bw, "%d\t%s\n", gid, uid); err != nil {
			return err
		}
	}
	return nil
}

// LoadUIDMap reads a TSV cache file written by SaveUIDMap.
func LoadUIDMap(path string) (map[int64]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := make(map[int64]string)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		tab := strings.IndexByte(line, '\t')
		if tab < 0 {
			continue
		}
		gid, err := strconv.ParseInt(line[:tab], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse graphalytics_id %q: %w", line[:tab], err)
		}
		out[gid] = line[tab+1:]
	}
	return out, sc.Err()
}

// ShortestOptions parameterises a single shortest-path query.
type ShortestOptions struct {
	SrcUID      string
	DstUID      string
	EdgePred    string // DQL edge predicate, e.g. connected
	NumPaths    int
	MaxFrontier int // <= 0 omits the cap
	Timeout     time.Duration
}

// ShortestResult is the parsed outcome of a shortest-path query.
type ShortestResult struct {
	// Distance is the smallest total path weight (the classic shortest
	// distance). math.Inf if no path was returned. Kept for the SSSP top-1
	// correctness mode.
	Distance float64
	// Weights is the per-path total weights from every entry in _path_, sorted
	// non-decreasing. This is the weight VECTOR compared against the oracle's
	// TopK output: comparing sorted costs (not path identity) is what makes the
	// k-shortest correctness check robust to ties. Len == PathCount.
	Weights []float64
	// Latency is the wall time spent in the gRPC round-trip.
	Latency time.Duration
	// PathCount is the number of distinct paths in the response (1 for the
	// classic shortest, up to NumPaths for k-shortest).
	PathCount int
	// SelfConsistent is true when, for every returned path, the sum of the
	// per-edge weight facets equals the path's reported _weight_ (within
	// tolerance). This needs no oracle: a path whose own edges don't add up to
	// its claimed cost is a bug. False if any path fails or can't be walked.
	SelfConsistent bool
	// Loopless is true when no returned path revisits a uid. Dgraph claims to
	// prune cyclical paths; a repeated uid would contradict that.
	Loopless bool
	// MaxWeightErr is the largest absolute |summed - reported| across paths,
	// for diagnostics when SelfConsistent is false.
	MaxWeightErr float64
}

// Shortest issues one `shortest` query and decodes total weight from the
// `_path_` response. Returns Distance=math.Inf if no path is found.
func (c *Client) Shortest(ctx context.Context, opts ShortestOptions) (ShortestResult, error) {
	if opts.NumPaths < 1 {
		opts.NumPaths = 1
	}
	edge := opts.EdgePred
	if edge == "" {
		edge = "connected"
	}
	frontier := ""
	if opts.MaxFrontier > 0 {
		frontier = fmt.Sprintf(", maxfrontiersize: %d", opts.MaxFrontier)
	}
	q := fmt.Sprintf(`
	{
		path as shortest(from: %s, to: %s, numpaths: %d%s) {
			%s @facets(weight)
		}
		result(func: uid(path)) {
			uid
		}
	}`, opts.SrcUID, opts.DstUID, opts.NumPaths, frontier, edge)

	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}

	txn := c.dg.NewReadOnlyTxn().BestEffort()
	defer func() { _ = txn.Discard(ctx) }()

	start := time.Now()
	resp, err := txn.Query(ctx, q)
	elapsed := time.Since(start)
	if err != nil {
		return ShortestResult{Latency: elapsed, Distance: math.Inf(+1)}, err
	}

	// Top-level _path_ array; each entry has a `_weight_` field. Pick the
	// smallest weight as the shortest distance.
	var decoded struct {
		Paths []map[string]any `json:"_path_"`
	}
	if err := json.Unmarshal(resp.Json, &decoded); err != nil {
		return ShortestResult{Latency: elapsed}, fmt.Errorf("decode _path_: %w", err)
	}
	if len(decoded.Paths) == 0 {
		return ShortestResult{Latency: elapsed, Distance: math.Inf(+1), PathCount: 0}, nil
	}

	weights := make([]float64, 0, len(decoded.Paths))
	selfConsistent, loopless := true, true
	maxErr := 0.0
	for _, p := range decoded.Paths {
		reported, hasReported := toFloat(p["_weight_"])
		if hasReported {
			weights = append(weights, reported)
		}
		// Walk the nested path to sum edge facets and check for loops.
		summed, noLoop, ok := walkPath(p, edge)
		if !ok || !noLoop {
			loopless = loopless && noLoop
			selfConsistent = false
			continue
		}
		if hasReported {
			if e := math.Abs(summed - reported); e > maxErr {
				maxErr = e
			}
			// 1e-6 absolute plus a relative term absorbs float-facet summation
			// noise without masking a real discrepancy.
			if math.Abs(summed-reported) > 1e-6+1e-9*math.Abs(reported) {
				selfConsistent = false
			}
		}
	}
	sort.Float64s(weights)
	best := math.Inf(+1)
	if len(weights) > 0 {
		best = weights[0]
	}
	return ShortestResult{
		Distance:       best,
		Weights:        weights,
		Latency:        elapsed,
		PathCount:      len(decoded.Paths),
		SelfConsistent: selfConsistent,
		Loopless:       loopless,
		MaxWeightErr:   maxErr,
	}, nil
}

// walkPath descends the singly-nested path under the edge predicate, summing
// the per-edge weight facets (key "<edge>|weight", carried on each child node
// next to its uid) and checking that no uid repeats. Returns the summed edge
// weight, whether the path is loopless, and whether the structure parsed
// cleanly. Matches the real _path_ wire format:
//
//	{"_weight_":3,"uid":"0x1","connected":{"connected|weight":1,"uid":"0x2",
//	  "connected":{...}}}
func walkPath(root map[string]any, edge string) (total float64, noLoop bool, ok bool) {
	facetKey := edge + "|weight"
	seen := make(map[string]bool)
	cur := root
	if u, has := cur["uid"].(string); has {
		seen[u] = true
	}
	for {
		childRaw, has := cur[edge]
		if !has {
			return total, true, true // reached the leaf — clean linear path
		}
		child, isMap := childRaw.(map[string]any)
		if !isMap {
			// A linear path nests a single object; an array (branching) is
			// unexpected for k-shortest and we flag it rather than guess.
			if arr, isArr := childRaw.([]any); isArr && len(arr) == 1 {
				child, isMap = arr[0].(map[string]any)
			}
			if !isMap {
				return total, true, false
			}
		}
		w, wok := toFloat(child[facetKey])
		if !wok {
			return total, true, false
		}
		total += w
		if u, has := child["uid"].(string); has {
			if seen[u] {
				return total, false, true // a uid repeated — not loopless
			}
			seen[u] = true
		}
		cur = child
	}
}

func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}
