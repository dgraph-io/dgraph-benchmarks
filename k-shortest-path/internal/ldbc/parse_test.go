package ldbc

import (
	"path/filepath"
	"testing"
)

func TestReadProperties_graphalyticsFormat(t *testing.T) {
	path := filepath.Join("..", "..", "datasets", "datagen-7_5-fb", "datagen-7_5-fb.properties")
	p, err := ReadProperties(path)
	if err != nil {
		t.Fatal(err)
	}
	if p.SourceVertex != 6 {
		t.Fatalf("SourceVertex = %d, want 6", p.SourceVertex)
	}
	if p.Directed {
		t.Fatal("expected undirected graph")
	}
	if !p.WeightedSSSP {
		t.Fatal("expected weighted SSSP")
	}
}
