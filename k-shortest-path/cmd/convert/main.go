// Command convert turns an LDBC Graphalytics datagen dataset into Dgraph-
// loadable artefacts: a gzipped RDF file with weight facets, and a schema
// file. The output is consumed by `dgraph bulk` for the fastest possible
// initial load.
//
//	convert -dataset ./datasets/datagen-7_5-fb \
//	        -out    ./datasets/datagen-7_5-fb/dgraph
//
// Produces:
//
//	<out>/graph.rdf.gz   — blank-node RDF for vertices + edges
//	<out>/graph.schema   — DQL schema with graphalytics_id index
//
// Then run:
//
//	dgraph bulk -f graph.rdf.gz -s graph.schema --zero localhost:5080 --out ./bulk-out
package main

import (
	"bufio"
	"compress/gzip"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/dgraph-io/dgraph-benchmarks/k-shortest-path/internal/client"
	"github.com/dgraph-io/dgraph-benchmarks/k-shortest-path/internal/ldbc"
)

func main() {
	dataset := flag.String("dataset", "", "path to extracted LDBC dataset (datasets/<name>/)")
	outDir := flag.String("out", "", "output directory (defaults to <dataset>/dgraph)")
	flag.Parse()

	if *dataset == "" {
		log.Fatal("-dataset is required")
	}
	if *outDir == "" {
		*outDir = filepath.Join(*dataset, "dgraph")
	}
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		log.Fatal(err)
	}

	ds, err := ldbc.LoadDataset(*dataset)
	if err != nil {
		log.Fatalf("load dataset: %v", err)
	}

	if err := writeSchema(filepath.Join(*outDir, "graph.schema")); err != nil {
		log.Fatalf("write schema: %v", err)
	}

	rdfPath := filepath.Join(*outDir, "graph.rdf.gz")
	start := time.Now()
	nV, nE, err := writeRDF(rdfPath, ds)
	if err != nil {
		log.Fatalf("write rdf: %v", err)
	}
	log.Printf("[convert] wrote %d vertices, %d edge-triples to %s in %s",
		nV, nE, rdfPath, time.Since(start).Round(time.Millisecond))
	log.Printf("[next] dgraph bulk -f %s -s %s --zero localhost:5080 --out %s",
		rdfPath,
		filepath.Join(*outDir, "graph.schema"),
		filepath.Join(*outDir, "bulk-out"))
}

func writeSchema(path string) error {
	return os.WriteFile(path, []byte(client.Schema), 0o644)
}

func writeRDF(path string, ds *ldbc.Dataset) (vertexCount, edgeCount int, err error) {
	f, err := os.Create(path)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	defer gz.Close()
	bw := bufio.NewWriterSize(gz, 1<<20)
	defer bw.Flush()

	verts, err := ldbc.ReadVertices(ds.VertexFile)
	if err != nil {
		return 0, 0, fmt.Errorf("read vertices: %w", err)
	}
	for _, v := range verts {
		// Vertex triples: graphalytics_id + dgraph.type.
		if _, err := fmt.Fprintf(bw, "_:v%d <graphalytics_id> \"%d\"^^<xs:int> .\n", v, v); err != nil {
			return 0, 0, err
		}
		if _, err := fmt.Fprintf(bw, "_:v%d <dgraph.type> \"Vertex\" .\n", v); err != nil {
			return 0, 0, err
		}
	}
	vertexCount = len(verts)

	// Stream edges; emit reverse triples too for undirected graphs.
	directed := ds.Properties.Directed
	err = ldbc.ScanEdges(ds.EdgeFile, func(e ldbc.Edge) error {
		if err := writeEdgeTriple(bw, e.Src, e.Dst, e.Weight); err != nil {
			return err
		}
		edgeCount++
		if !directed {
			if err := writeEdgeTriple(bw, e.Dst, e.Src, e.Weight); err != nil {
				return err
			}
			edgeCount++
		}
		return nil
	})
	if err != nil {
		return vertexCount, edgeCount, fmt.Errorf("scan edges: %w", err)
	}
	return vertexCount, edgeCount, nil
}

func writeEdgeTriple(bw *bufio.Writer, src, dst int64, weight float64) error {
	_, err := fmt.Fprintf(bw, "_:v%d <connected> _:v%d (weight=%s) .\n",
		src, dst, formatWeight(weight))
	return err
}

// formatWeight emits a weight value Dgraph will always type as a float facet.
// strconv 'f' avoids scientific notation; appending ".0" for integer-valued
// floats prevents Dgraph from inferring an int facet for, e.g., weight=1.0.
func formatWeight(w float64) string {
	s := strconv.FormatFloat(w, 'f', -1, 64)
	if !strings.ContainsAny(s, ".eE") {
		s += ".0"
	}
	return s
}
