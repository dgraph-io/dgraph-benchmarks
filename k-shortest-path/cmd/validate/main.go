// Command validate is a standalone differ for two SSSP-format files. Useful
// when you've captured a Dgraph SSSP output to disk and want to diff it
// against the LDBC reference without spinning up the bench harness.
//
//	validate -reference datasets/datagen-7_5-fb/validation/datagen-7_5-fb-SSSP \
//	         -actual    results/pr-9607/sssp.txt
package main

import (
	"flag"
	"fmt"
	"log"
	"math"
	"os"

	"github.com/dgraph-io/dgraph-benchmarks/k-shortest-path/internal/ldbc"
)

const epsilon = 0.0001

func main() {
	ref := flag.String("reference", "", "LDBC reference SSSP output")
	act := flag.String("actual", "", "actual SSSP output to validate")
	maxShow := flag.Int("max-show", 10, "max failures to print")
	flag.Parse()
	if *ref == "" || *act == "" {
		log.Fatal("-reference and -actual are both required")
	}

	expected, err := ldbc.ReadSSSP(*ref)
	if err != nil {
		log.Fatalf("read reference: %v", err)
	}
	got, err := ldbc.ReadSSSP(*act)
	if err != nil {
		log.Fatalf("read actual: %v", err)
	}

	if len(expected) != len(got) {
		fmt.Fprintf(os.Stderr,
			"WARN: vertex set sizes differ — expected=%d actual=%d\n",
			len(expected), len(got))
	}

	var passed, failed int
	var fails []string
	for v, exp := range expected {
		g, ok := got[v]
		if !ok {
			failed++
			if len(fails) < *maxShow {
				fails = append(fails, fmt.Sprintf("vertex %d: missing from actual", v))
			}
			continue
		}
		if math.IsInf(exp, +1) && math.IsInf(g, +1) {
			passed++
			continue
		}
		if math.IsInf(exp, +1) != math.IsInf(g, +1) {
			failed++
			if len(fails) < *maxShow {
				fails = append(fails, fmt.Sprintf("vertex %d: inf mismatch exp=%v got=%v", v, exp, g))
			}
			continue
		}
		denom := math.Abs(exp)
		if denom == 0 {
			denom = 1
		}
		if math.Abs(exp-g)/denom > epsilon {
			failed++
			if len(fails) < *maxShow {
				fails = append(fails, fmt.Sprintf("vertex %d: exp=%v got=%v rel=%.4f",
					v, exp, g, math.Abs(exp-g)/denom))
			}
			continue
		}
		passed++
	}

	fmt.Printf("passed=%d failed=%d total=%d epsilon=%g\n", passed, failed, passed+failed, epsilon)
	for _, s := range fails {
		fmt.Println("  ", s)
	}
	if failed > 0 {
		os.Exit(1)
	}
}
