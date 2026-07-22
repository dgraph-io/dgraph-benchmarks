package client

import (
	"encoding/json"
	"math"
	"testing"
)

// The exact _path_ payload captured from a live PR-binary alpha via
// cmd/handprobe (numpaths=2 on the hand graph). The self-consistency walk is
// tested against this real wire format, not a guess.
const realPathJSON = `{
  "_path_": [
    {"_weight_": 3, "uid": "0x1",
      "connected": {"connected|weight": 1, "uid": "0x2",
        "connected": {"connected|weight": 1, "uid": "0x3",
          "connected": {"connected|weight": 1, "uid": "0x4"}}}},
    {"_weight_": 6, "uid": "0x1",
      "connected": {"connected|weight": 1, "uid": "0x2",
        "connected": {"connected|weight": 5, "uid": "0x4"}}}
  ]
}`

func parsePaths(t *testing.T, raw string) []map[string]any {
	t.Helper()
	var d struct {
		Paths []map[string]any `json:"_path_"`
	}
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return d.Paths
}

func TestWalkPath_RealFormat(t *testing.T) {
	paths := parsePaths(t, realPathJSON)
	if len(paths) != 2 {
		t.Fatalf("want 2 paths, got %d", len(paths))
	}

	// Path 0: 1->2->3->4, weights 1+1+1 = 3, loopless.
	total, noLoop, ok := walkPath(paths[0], "connected")
	if !ok || !noLoop {
		t.Fatalf("path0: ok=%v noLoop=%v", ok, noLoop)
	}
	if math.Abs(total-3) > 1e-9 {
		t.Fatalf("path0 summed weight: got %v want 3", total)
	}

	// Path 1: 1->2->4, weights 1+5 = 6, loopless.
	total, noLoop, ok = walkPath(paths[1], "connected")
	if !ok || !noLoop {
		t.Fatalf("path1: ok=%v noLoop=%v", ok, noLoop)
	}
	if math.Abs(total-6) > 1e-9 {
		t.Fatalf("path1 summed weight: got %v want 6", total)
	}
}

// A path that revisits a uid must be flagged as not loopless.
func TestWalkPath_DetectsLoop(t *testing.T) {
	const loopJSON = `{"_path_":[
		{"_weight_": 2, "uid": "0x1",
		  "connected": {"connected|weight": 1, "uid": "0x2",
		    "connected": {"connected|weight": 1, "uid": "0x1"}}}
	]}`
	paths := parsePaths(t, loopJSON)
	_, noLoop, ok := walkPath(paths[0], "connected")
	if !ok {
		t.Fatal("expected clean parse")
	}
	if noLoop {
		t.Fatal("expected loop to be detected (0x1 repeats)")
	}
}

// A missing facet weight should fail the parse (can't verify consistency).
func TestWalkPath_MissingFacet(t *testing.T) {
	const noFacet = `{"_path_":[
		{"_weight_": 1, "uid": "0x1", "connected": {"uid": "0x2"}}
	]}`
	paths := parsePaths(t, noFacet)
	_, _, ok := walkPath(paths[0], "connected")
	if ok {
		t.Fatal("expected ok=false when an edge facet is missing")
	}
}
