package stats

import (
	"testing"
	"time"
)

func TestSummarizePercentiles(t *testing.T) {
	r := New()
	for i := 1; i <= 100; i++ {
		r.Record(time.Duration(i) * time.Millisecond)
	}
	s := r.Summarize(100 * time.Millisecond)
	if s.Count != 100 {
		t.Fatalf("count=%d", s.Count)
	}
	if s.P50 != 50*time.Millisecond {
		t.Errorf("p50=%v want 50ms", s.P50)
	}
	if s.P95 != 95*time.Millisecond {
		t.Errorf("p95=%v want 95ms", s.P95)
	}
	if s.P99 != 99*time.Millisecond {
		t.Errorf("p99=%v want 99ms", s.P99)
	}
	if s.QPS <= 0 {
		t.Errorf("qps=%v", s.QPS)
	}
}

func TestSummarizeEmpty(t *testing.T) {
	r := New()
	s := r.Summarize(time.Second)
	if s.Count != 0 || s.QPS != 0 {
		t.Fatalf("unexpected empty summary: %+v", s)
	}
}
