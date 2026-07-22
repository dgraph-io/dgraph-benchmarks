// Command handprobe settles the one semantic question the whole k-shortest
// comparison rests on: when Dgraph is asked for numpaths=2, does it return the
// 2nd-best LOOPLESS path (Yen's definition — what our oracle computes), or the
// 2nd-best node/edge-DISJOINT path (a different problem)?
//
// It loads a tiny hand graph into a FRESH (empty) alpha, where the two
// definitions give provably different answers, then runs numpaths=2 and prints
// what came back. Run it against one PR binary's empty alpha:
//
//	go run ./cmd/handprobe -alpha localhost:9080
//
// The graph (source=1, target=4):
//
//	1 ->2 ->3 ->4   weights 1,1,1   => path cost 3   (shortest)
//	     \-------->4 weight 5        (2 ->4)          => 1-2-4 cost 6  (shares node 2 + edge 1->2 with the shortest)
//	1 ->5 ->4       weights 10,10                     => 1-5-4 cost 20 (fully disjoint from the shortest)
//
// Interpretation of the numpaths=2 result:
//
//	costs {3, 6}   => LOOPLESS  (Yen)      => our gonum oracle is the right reference. PROCEED.
//	costs {3, 20}  => DISJOINT             => oracle approach is wrong; STOP and rethink.
//	costs {3}      => only 1 path returned => check numpaths handling on this binary.
//
// It also dumps the raw _path_ JSON so the self-consistency parser (task #5)
// can be written against the real wire format of this exact binary, not a guess.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"sort"
	"time"

	"github.com/dgraph-io/dgo/v250"
	"github.com/dgraph-io/dgo/v250/protos/api"
	"github.com/dgraph-io/dgraph-benchmarks/k-shortest-path/internal/client"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const handGraphRDF = `
_:n1 <graphalytics_id> "1"^^<xs:int> .
_:n2 <graphalytics_id> "2"^^<xs:int> .
_:n3 <graphalytics_id> "3"^^<xs:int> .
_:n4 <graphalytics_id> "4"^^<xs:int> .
_:n5 <graphalytics_id> "5"^^<xs:int> .
_:n1 <connected> _:n2 (weight=1.0) .
_:n2 <connected> _:n3 (weight=1.0) .
_:n3 <connected> _:n4 (weight=1.0) .
_:n2 <connected> _:n4 (weight=5.0) .
_:n1 <connected> _:n5 (weight=10.0) .
_:n5 <connected> _:n4 (weight=10.0) .
`

func main() {
	alpha := flag.String("alpha", "localhost:9080", "Dgraph alpha gRPC address (point at a FRESH/empty alpha)")
	numpaths := flag.Int("numpaths", 2, "numpaths for the probe query")
	maxFrontier := flag.Int("maxfrontier", 0, "maxfrontiersize for the probe query (0 = unbounded)")
	timeout := flag.Duration("timeout", 30*time.Second, "hard timeout on the shortest query so a hanging binary can't wedge the session")
	flag.Parse()

	ctx := context.Background()

	conn, err := grpc.NewClient(*alpha, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("dial %s: %v", *alpha, err)
	}
	defer conn.Close()
	dg := dgo.NewDgraphClient(api.NewDgraphClient(conn))

	// Schema + load. Reuses the exact schema the real bench uses.
	if err := dg.Alter(ctx, &api.Operation{Schema: client.Schema}); err != nil {
		log.Fatalf("alter schema: %v", err)
	}
	txn := dg.NewTxn()
	if _, err := txn.Mutate(ctx, &api.Mutation{SetNquads: []byte(handGraphRDF), CommitNow: true}); err != nil {
		log.Fatalf("mutate hand graph: %v (is the alpha empty? re-run needs a fresh alpha)", err)
	}
	// Indexing is async after CommitNow in some builds; give it a beat.
	time.Sleep(500 * time.Millisecond)

	src := lookupUID(ctx, dg, 1)
	dst := lookupUID(ctx, dg, 4)
	fmt.Printf("src(gid=1)=%s  dst(gid=4)=%s\n\n", src, dst)

	frontier := ""
	if *maxFrontier > 0 {
		frontier = fmt.Sprintf(", maxfrontiersize: %d", *maxFrontier)
	}
	q := fmt.Sprintf(`
	{
		path as shortest(from: %s, to: %s, numpaths: %d%s) {
			connected @facets(weight)
		}
		result(func: uid(path)) { uid graphalytics_id }
	}`, src, dst, *numpaths, frontier)

	// Hard deadline: if the binary hangs on numpaths>=2, the client gives up
	// instead of blocking forever (which, with an unbounded query, can balloon
	// alpha's memory and get the SSH session OOM-killed).
	qctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	rtxn := dg.NewReadOnlyTxn().BestEffort()
	resp, err := rtxn.Query(qctx, q)
	_ = rtxn.Discard(ctx)
	if err != nil {
		log.Fatalf("shortest query (timeout=%s, maxfrontier=%d): %v\n"+
			"  a timeout here means this binary does not terminate numpaths=%d on a 6-node graph — that is itself the finding.",
			*timeout, *maxFrontier, err, *numpaths)
	}

	fmt.Println("=== RAW _path_ JSON (write the self-consistency parser against THIS) ===")
	fmt.Println(prettyJSON(resp.Json))

	weights := parseWeights(resp.Json)
	fmt.Printf("\n=== parsed per-path costs (sorted): %v\n\n", weights)

	fmt.Println("=== VERDICT ===")
	switch {
	case len(weights) >= 2 && approx(weights[0], 3) && approx(weights[1], 6):
		fmt.Println("LOOPLESS (Yen). numpaths=2 returned {3, 6} — the gonum oracle IS the right reference. PROCEED.")
	case len(weights) >= 2 && approx(weights[0], 3) && approx(weights[1], 20):
		fmt.Println("DISJOINT. numpaths=2 returned {3, 20} — oracle approach is WRONG for this semantics. STOP and rethink.")
	case len(weights) == 1 && approx(weights[0], 3):
		fmt.Println("ONLY 1 PATH. numpaths=2 returned a single path — investigate numpaths handling on this binary.")
	default:
		fmt.Printf("UNEXPECTED: %v — inspect the raw JSON above before drawing conclusions.\n", weights)
	}
}

func lookupUID(ctx context.Context, dg *dgo.Dgraph, gid int64) string {
	q := fmt.Sprintf(`{ q(func: eq(graphalytics_id, %d)) { uid } }`, gid)
	txn := dg.NewReadOnlyTxn().BestEffort()
	resp, err := txn.Query(ctx, q)
	_ = txn.Discard(ctx)
	if err != nil {
		log.Fatalf("lookup gid=%d: %v", gid, err)
	}
	var d struct {
		Q []struct {
			UID string `json:"uid"`
		} `json:"q"`
	}
	if err := json.Unmarshal(resp.Json, &d); err != nil {
		log.Fatalf("decode lookup gid=%d: %v", gid, err)
	}
	if len(d.Q) == 0 {
		log.Fatalf("gid=%d not found — did the mutation/indexing complete?", gid)
	}
	return d.Q[0].UID
}

func parseWeights(raw []byte) []float64 {
	var decoded struct {
		Paths []map[string]any `json:"_path_"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil
	}
	var out []float64
	for _, p := range decoded.Paths {
		if w, ok := p["_weight_"]; ok {
			switch x := w.(type) {
			case float64:
				out = append(out, x)
			case json.Number:
				if f, err := x.Float64(); err == nil {
					out = append(out, f)
				}
			}
		}
	}
	sort.Float64s(out)
	return out
}

func approx(a, b float64) bool {
	d := a - b
	return d < 1e-6 && d > -1e-6
}

func prettyJSON(raw []byte) string {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return string(raw)
	}
	return string(b)
}
