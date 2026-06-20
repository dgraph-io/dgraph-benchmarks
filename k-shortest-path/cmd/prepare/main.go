// Command prepare turns a road-network dataset, fetched from a URL (or local
// path), into the LDBC layout the rest of the harness consumes:
//
//	datasets/<name>/<name>.v             vertex ids, one per line
//	datasets/<name>/<name>.e             "src dst weight", the edge list
//	datasets/<name>/<name>.properties    metadata (directed flag, source vertex)
//	datasets/<name>/validation/<name>-SSSP  Dijkstra reference from the source
//
// Two input formats, selected with -format:
//
//	dimacs  DIMACS challenge9 .gr (.gz ok): "p sp n m" + "a u v w" arcs.
//	        Arcs are already bidirectional, so the graph is treated as directed
//	        and edges are emitted as-is. Vertices are 1..n. (e.g. roadCOL, roadNY)
//	csv     a .zip containing edges.csv (source,target,travel_time,distance,cat)
//	        and nodes.csv (index,...). Undirected; weight = the distance column;
//	        self-loops and duplicate reverse edges dropped; both directions
//	        emitted. Matches the legacy csv_road_to_ldbc.py exactly. (e.g. NY)
//
// The SSSP reference is computed with gonum Dijkstra on the *same* graph the
// oracle builds for k-shortest, so the top-1 reference can never disagree with
// the oracle. Examples:
//
//	prepare -format dimacs -name roadCOL -source 1 \
//	  -url http://www.diag.uniroma1.it/challenge9/data/USA-road-t/USA-road-t.COL.gr.gz
//	prepare -format csv -name roadNY -source 1 -url ./NY.csv.zip
package main

import (
	"archive/zip"
	"bufio"
	"compress/gzip"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dgraph-io/dgraph-benchmarks/k-shortest-path/internal/ldbc"
	"github.com/dgraph-io/dgraph-benchmarks/k-shortest-path/internal/oracle"
)

type dataset struct {
	vertices []int64     // explicit vertex ids, sorted
	edges    []ldbc.Edge // unique edges as parsed (one per pair for undirected)
	directed bool
}

func main() {
	url := flag.String("url", "", "source URL or local path to the dataset file")
	format := flag.String("format", "", "dimacs | csv")
	name := flag.String("name", "", "dataset name (output goes to <out>/<name>/)")
	outDir := flag.String("out", "datasets", "output root directory")
	source := flag.Int64("source", 1, "SSSP source vertex id")
	flag.Parse()

	if *url == "" || *format == "" || *name == "" {
		log.Fatal("-url, -format, and -name are all required")
	}

	local, cleanup, err := fetch(*url)
	if err != nil {
		log.Fatalf("fetch %s: %v", *url, err)
	}
	defer cleanup()

	var ds dataset
	switch *format {
	case "dimacs":
		ds, err = parseDIMACS(local)
	case "csv":
		ds, err = parseCSVZip(local)
	default:
		log.Fatalf("unknown -format %q (want dimacs|csv)", *format)
	}
	if err != nil {
		log.Fatalf("parse %s: %v", *format, err)
	}
	log.Printf("[prepare] %s: %d vertices, %d unique edges, directed=%v",
		*name, len(ds.vertices), len(ds.edges), ds.directed)

	root := filepath.Join(*outDir, *name)
	if err := os.MkdirAll(filepath.Join(root, "validation"), 0o755); err != nil {
		log.Fatal(err)
	}

	if err := writeVertices(filepath.Join(root, *name+".v"), ds.vertices); err != nil {
		log.Fatalf("write .v: %v", err)
	}
	nE, err := writeEdges(filepath.Join(root, *name+".e"), ds)
	if err != nil {
		log.Fatalf("write .e: %v", err)
	}
	if err := writeProperties(filepath.Join(root, *name+".properties"), *name, ds, nE, *source); err != nil {
		log.Fatalf("write .properties: %v", err)
	}

	// SSSP reference on the same effective graph the oracle/Dgraph see.
	log.Printf("[prepare] building graph + Dijkstra reference from source %d...", *source)
	start := time.Now()
	g := oracle.New()
	for _, v := range ds.vertices {
		g.AddNode(v)
	}
	if err := forEachEffectiveEdge(ds, func(s, t int64, w float64) error {
		return g.AddEdge(s, t, w)
	}); err != nil {
		log.Fatalf("build graph: %v", err)
	}
	dist, err := g.SSSP(*source)
	if err != nil {
		log.Fatalf("sssp: %v", err)
	}
	if err := writeSSSP(filepath.Join(root, "validation", *name+"-SSSP"), ds.vertices, dist); err != nil {
		log.Fatalf("write SSSP: %v", err)
	}
	reachable := 0
	for _, d := range dist {
		if !math.IsInf(d, +1) {
			reachable++
		}
	}
	log.Printf("[prepare] SSSP: %d/%d reachable in %s", reachable, len(ds.vertices), time.Since(start).Round(time.Millisecond))
	log.Printf("[prepare] done -> %s/", root)
	log.Printf("[next] convert -dataset %s && dgraph bulk ...", root)
}

// -----------------------------------------------------------------------------
// fetch
// -----------------------------------------------------------------------------

// fetch returns a local file path for url. A url without a "://" scheme is
// treated as an existing local path (no copy). Remote urls are downloaded to a
// temp file. The returned cleanup removes any temp file.
func fetch(url string) (path string, cleanup func(), err error) {
	noop := func() {}
	if !strings.Contains(url, "://") {
		if _, statErr := os.Stat(url); statErr != nil {
			return "", noop, fmt.Errorf("local path: %w", statErr)
		}
		return url, noop, nil
	}
	resp, err := http.Get(url)
	if err != nil {
		return "", noop, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", noop, fmt.Errorf("http %s: %s", url, resp.Status)
	}
	tmp, err := os.CreateTemp("", "prepare-*"+filepath.Ext(url))
	if err != nil {
		return "", noop, err
	}
	log.Printf("[prepare] downloading %s ...", url)
	if _, err := io.Copy(tmp, resp.Body); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", noop, err
	}
	tmp.Close()
	return tmp.Name(), func() { os.Remove(tmp.Name()) }, nil
}

// openMaybeGz opens path, transparently decompressing if it is gzip.
func openMaybeGz(path string) (io.ReadCloser, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	if strings.HasSuffix(path, ".gz") {
		gz, err := gzip.NewReader(f)
		if err != nil {
			f.Close()
			return nil, err
		}
		return gzReadCloser{gz: gz, f: f}, nil
	}
	return f, nil
}

type gzReadCloser struct {
	gz *gzip.Reader
	f  *os.File
}

func (g gzReadCloser) Read(p []byte) (int, error) { return g.gz.Read(p) }
func (g gzReadCloser) Close() error {
	_ = g.gz.Close()
	return g.f.Close()
}

// -----------------------------------------------------------------------------
// DIMACS .gr
// -----------------------------------------------------------------------------

func parseDIMACS(path string) (dataset, error) {
	r, err := openMaybeGz(path)
	if err != nil {
		return dataset{}, err
	}
	defer r.Close()

	var n int64
	var edges []ldbc.Edge
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		switch line[0] {
		case 'c':
			continue
		case 'p':
			// p sp <n> <m>
			f := strings.Fields(line)
			if len(f) >= 3 {
				n, err = strconv.ParseInt(f[2], 10, 64)
				if err != nil {
					return dataset{}, fmt.Errorf("problem line %q: %w", line, err)
				}
			}
		case 'a':
			f := strings.Fields(line)
			if len(f) != 4 {
				return dataset{}, fmt.Errorf("arc line %q: want 4 fields", line)
			}
			u, err1 := strconv.ParseInt(f[1], 10, 64)
			v, err2 := strconv.ParseInt(f[2], 10, 64)
			w, err3 := strconv.ParseFloat(f[3], 64)
			if err1 != nil || err2 != nil || err3 != nil {
				return dataset{}, fmt.Errorf("arc parse %q", line)
			}
			if u == v {
				continue
			}
			edges = append(edges, ldbc.Edge{Src: u, Dst: v, Weight: w})
		}
	}
	if err := sc.Err(); err != nil {
		return dataset{}, err
	}
	if n == 0 {
		return dataset{}, fmt.Errorf("no problem line (p sp n m) found")
	}
	verts := make([]int64, n)
	for i := int64(0); i < n; i++ {
		verts[i] = i + 1 // DIMACS vertices are 1-indexed
	}
	// DIMACS arcs already encode both directions, so treat as directed and
	// emit as-is (don't synthesize reverse edges).
	return dataset{vertices: verts, edges: edges, directed: true}, nil
}

// -----------------------------------------------------------------------------
// CSV zip (edges.csv + nodes.csv)
// -----------------------------------------------------------------------------

func parseCSVZip(path string) (dataset, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return dataset{}, fmt.Errorf("open zip: %w", err)
	}
	defer zr.Close()

	var edgesF, nodesF *zip.File
	for _, f := range zr.File {
		switch filepath.Base(f.Name) {
		case "edges.csv":
			edgesF = f
		case "nodes.csv":
			nodesF = f
		}
	}
	if edgesF == nil || nodesF == nil {
		return dataset{}, fmt.Errorf("zip must contain edges.csv and nodes.csv")
	}

	// Vertex count = number of data rows in nodes.csv (0-indexed ids 0..N-1).
	nNodes, err := countCSVRows(nodesF)
	if err != nil {
		return dataset{}, fmt.Errorf("nodes.csv: %w", err)
	}

	// Edges: undirected dedup, skip self-loops, weight = distance column (idx 3).
	rc, err := edgesF.Open()
	if err != nil {
		return dataset{}, err
	}
	defer rc.Close()
	seen := make(map[[2]int64]struct{})
	var edges []ldbc.Edge
	var skipSelf, skipDup int
	sc := bufio.NewScanner(rc)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Split(line, ",")
		if len(parts) < 4 {
			continue
		}
		s, err1 := strconv.ParseInt(strings.TrimSpace(parts[0]), 10, 64)
		t, err2 := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
		w, err3 := strconv.ParseFloat(strings.TrimSpace(parts[3]), 64) // distance
		if err1 != nil || err2 != nil || err3 != nil {
			return dataset{}, fmt.Errorf("edge parse %q", line)
		}
		if s == t {
			skipSelf++
			continue
		}
		key := [2]int64{s, t}
		if s > t {
			key = [2]int64{t, s}
		}
		if _, dup := seen[key]; dup {
			skipDup++
			continue
		}
		seen[key] = struct{}{}
		edges = append(edges, ldbc.Edge{Src: s, Dst: t, Weight: w})
	}
	if err := sc.Err(); err != nil {
		return dataset{}, err
	}
	log.Printf("[prepare] csv: %d unique edges (skipped %d self-loops, %d dup-reverse)",
		len(edges), skipSelf, skipDup)

	verts := make([]int64, nNodes)
	for i := int64(0); i < nNodes; i++ {
		verts[i] = i
	}
	return dataset{vertices: verts, edges: edges, directed: false}, nil
}

func countCSVRows(f *zip.File) (int64, error) {
	rc, err := f.Open()
	if err != nil {
		return 0, err
	}
	defer rc.Close()
	var n int64
	sc := bufio.NewScanner(rc)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		n++
	}
	return n, sc.Err()
}

// -----------------------------------------------------------------------------
// effective edges + writers
// -----------------------------------------------------------------------------

// forEachEffectiveEdge invokes fn for every directed edge actually present in
// the graph: as-is for directed datasets, both directions for undirected ones.
// This is the single source of truth shared by .e writing and graph building,
// so they can never diverge.
func forEachEffectiveEdge(ds dataset, fn func(s, t int64, w float64) error) error {
	for _, e := range ds.edges {
		if err := fn(e.Src, e.Dst, e.Weight); err != nil {
			return err
		}
		if !ds.directed {
			if err := fn(e.Dst, e.Src, e.Weight); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeVertices(path string, verts []int64) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	bw := bufio.NewWriter(f)
	defer bw.Flush()
	for _, v := range verts {
		if _, err := fmt.Fprintf(bw, "%d\n", v); err != nil {
			return err
		}
	}
	return nil
}

func writeEdges(path string, ds dataset) (int, error) {
	f, err := os.Create(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	bw := bufio.NewWriter(f)
	defer bw.Flush()
	count := 0
	err = forEachEffectiveEdge(ds, func(s, t int64, w float64) error {
		count++
		_, e := fmt.Fprintf(bw, "%d %d %s\n", s, t, strconv.FormatFloat(w, 'f', 6, 64))
		return e
	})
	return count, err
}

func writeProperties(path, name string, ds dataset, nEdges int, source int64) error {
	content := fmt.Sprintf(`graph.%[1]s.vertex-file = %[1]s.v
graph.%[1]s.edge-file = %[1]s.e

graph.%[1]s.meta.vertices = %[2]d
graph.%[1]s.meta.edges = %[3]d

graph.%[1]s.directed = %[4]v

graph.%[1]s.edge-properties.names = weight
graph.%[1]s.edge-properties.types = real

graph.%[1]s.algorithms = sssp

graph.%[1]s.sssp.weight-property = weight
graph.%[1]s.sssp.source-vertex = %[5]d
`, name, len(ds.vertices), nEdges, ds.directed, source)
	return os.WriteFile(path, []byte(content), 0o644)
}

func writeSSSP(path string, verts []int64, dist map[int64]float64) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	bw := bufio.NewWriter(f)
	defer bw.Flush()
	// verts is already in id order from parsing; ensure deterministic output.
	sort.Slice(verts, func(i, j int) bool { return verts[i] < verts[j] })
	for _, v := range verts {
		d, ok := dist[v]
		if !ok || math.IsInf(d, +1) {
			if _, err := fmt.Fprintf(bw, "%d infinity\n", v); err != nil {
				return err
			}
			continue
		}
		if _, err := fmt.Fprintf(bw, "%d %s\n", v, strconv.FormatFloat(d, 'f', 6, 64)); err != nil {
			return err
		}
	}
	return nil
}
