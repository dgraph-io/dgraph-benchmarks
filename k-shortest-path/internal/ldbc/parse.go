// Package ldbc parses the file artefacts of an LDBC Graphalytics datagen
// dataset: the .v vertex list, the .e edge list, the .properties metadata
// file, and the validation/<dataset>-SSSP reference output.
//
// All parsers are streaming where useful; the .v and SSSP files comfortably
// fit in memory at M-scale (633K vertices ~ a few MB), but the .e file at
// 34M edges does not — use ScanEdges for that one.
package ldbc

import (
	"bufio"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Properties carries the subset of `.properties` fields we care about.
type Properties struct {
	Directed     bool
	WeightedSSSP bool
	SourceVertex int64
}

// ReadProperties parses a Java-properties style file. Only the keys we use
// are extracted; everything else is silently ignored.
func ReadProperties(path string) (Properties, error) {
	f, err := os.Open(path)
	if err != nil {
		return Properties{}, err
	}
	defer f.Close()

	var p Properties
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		switch {
		case key == "graph.directed" || strings.HasSuffix(key, ".directed"):
			p.Directed = strings.EqualFold(val, "true")
		case key == "graph.weights" || strings.HasSuffix(key, ".sssp.weight-property"):
			p.WeightedSSSP = true
		case key == "algorithms.sssp.source-vertex" || strings.HasSuffix(key, ".sssp.source-vertex"):
			n, err := strconv.ParseInt(val, 10, 64)
			if err != nil {
				return p, fmt.Errorf("parse sssp source: %w", err)
			}
			p.SourceVertex = n
		case p.SourceVertex == 0 && (key == "algorithms.bfs.source-vertex" || strings.HasSuffix(key, ".bfs.source-vertex")):
			// Fall back to BFS source-vertex when the dataset doesn't declare
			// an SSSP source (e.g. unweighted graphs like cit-Patents).
			n, err := strconv.ParseInt(val, 10, 64)
			if err != nil {
				return p, fmt.Errorf("parse bfs source: %w", err)
			}
			p.SourceVertex = n
		}
	}
	if err := sc.Err(); err != nil {
		return p, err
	}
	return p, nil
}

// ReadVertices reads the entire `.v` file into a slice. Each line is a
// single int64 vertex id.
func ReadVertices(path string) ([]int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []int64
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		s := strings.TrimSpace(sc.Text())
		if s == "" {
			continue
		}
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("vertex parse %q: %w", s, err)
		}
		out = append(out, n)
	}
	return out, sc.Err()
}

// Edge is a parsed line from the `.e` file. Weight is 1.0 for unweighted
// graphs (the third field is then absent).
type Edge struct {
	Src    int64
	Dst    int64
	Weight float64
}

// ScanEdges streams the `.e` file. The callback is invoked once per edge;
// returning an error stops the scan.
func ScanEdges(path string, fn func(Edge) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return fmt.Errorf("malformed edge line %q", line)
		}
		src, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			return fmt.Errorf("edge src parse %q: %w", fields[0], err)
		}
		dst, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return fmt.Errorf("edge dst parse %q: %w", fields[1], err)
		}
		w := 1.0
		if len(fields) >= 3 {
			w, err = strconv.ParseFloat(fields[2], 64)
			if err != nil {
				return fmt.Errorf("edge weight parse %q: %w", fields[2], err)
			}
		}
		if err := fn(Edge{Src: src, Dst: dst, Weight: w}); err != nil {
			return err
		}
	}
	return sc.Err()
}

// ReadSSSP loads a reference SSSP file produced by LDBC. Lines look like
//
//	<vertex-id> <distance-or-Infinity>
//
// Returns a map keyed by vertex id; unreachable vertices have value
// math.Inf(+1).
func ReadSSSP(path string) (map[int64]float64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	out := make(map[int64]float64)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return nil, fmt.Errorf("malformed SSSP line %q", line)
		}
		vid, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("vertex parse %q: %w", fields[0], err)
		}
		var dist float64
		switch {
		case strings.EqualFold(fields[1], "Infinity"):
			dist = math.Inf(+1)
		case fields[1] == "9223372036854775807":
			// max int64 — LDBC BFS reference uses this as the unreachable marker.
			dist = math.Inf(+1)
		default:
			dist, err = strconv.ParseFloat(fields[1], 64)
			if err != nil {
				return nil, fmt.Errorf("distance parse %q: %w", fields[1], err)
			}
		}
		out[vid] = dist
	}
	return out, sc.Err()
}

// Dataset captures the absolute file paths for a single Graphalytics dataset.
// All fields point at files that exist; an error is returned otherwise.
type Dataset struct {
	Name         string
	Root         string
	VertexFile   string
	EdgeFile     string
	PropsFile    string
	SSSPRefFile  string
	Properties   Properties
}

// LoadDataset inspects `root` (typically datasets/<name>/) for the expected
// layout. It verifies file presence and reads `.properties` eagerly but does
// not touch `.v`, `.e`, or the reference file. If the dataset has no SSSP
// reference but does have a BFS reference, SSSPRefFile falls back to that —
// useful for unweighted graphs where BFS hop counts and SSSP distances align
// (i.e. when every edge weight is 1).
func LoadDataset(root string) (*Dataset, error) {
	name := filepath.Base(strings.TrimRight(root, string(filepath.Separator)))
	d := &Dataset{
		Name:        name,
		Root:        root,
		VertexFile:  filepath.Join(root, name+".v"),
		EdgeFile:    filepath.Join(root, name+".e"),
		PropsFile:   filepath.Join(root, name+".properties"),
	}
	for label, p := range map[string]string{
		"vertex": d.VertexFile, "edge": d.EdgeFile, "properties": d.PropsFile,
	} {
		if _, err := os.Stat(p); err != nil {
			return nil, fmt.Errorf("%s file %s: %w", label, p, err)
		}
	}
	// Prefer SSSP reference; fall back to BFS if SSSP isn't present.
	for _, suffix := range []string{"-SSSP", "-BFS"} {
		candidate := filepath.Join(root, "validation", name+suffix)
		if _, err := os.Stat(candidate); err == nil {
			d.SSSPRefFile = candidate
			break
		}
	}
	if d.SSSPRefFile == "" {
		// Set the canonical SSSP path even if missing, so error messages elsewhere
		// point users at the expected location.
		d.SSSPRefFile = filepath.Join(root, "validation", name+"-SSSP")
	}
	props, err := ReadProperties(d.PropsFile)
	if err != nil {
		return nil, err
	}
	d.Properties = props
	return d, nil
}

var _ io.Reader // silence unused-import on some builds
