// Package stats provides a tiny latency-recording utility with p50/p95/p99.
// Backed by a sorted slice; fine for sample counts up to a few million.
package stats

import (
	"math"
	"sort"
	"sync"
	"time"
)

type Recorder struct {
	mu      sync.Mutex
	samples []time.Duration
	errs    int64
}

func New() *Recorder { return &Recorder{} }

func (r *Recorder) Record(d time.Duration) {
	r.mu.Lock()
	r.samples = append(r.samples, d)
	r.mu.Unlock()
}

func (r *Recorder) RecordError() {
	r.mu.Lock()
	r.errs++
	r.mu.Unlock()
}

type Summary struct {
	Count    int           `json:"count"`
	Errors   int64         `json:"errors"`
	Min      time.Duration `json:"min_ns"`
	Max      time.Duration `json:"max_ns"`
	Mean     time.Duration `json:"mean_ns"`
	P50      time.Duration `json:"p50_ns"`
	P95      time.Duration `json:"p95_ns"`
	P99      time.Duration `json:"p99_ns"`
	P999     time.Duration `json:"p999_ns"`
	Duration time.Duration `json:"wall_ns"`
	QPS      float64       `json:"qps"`
}

// Summarize returns percentile stats. Pass the wall-clock duration the
// recorder was active for so QPS can be computed.
func (r *Recorder) Summarize(wall time.Duration) Summary {
	r.mu.Lock()
	defer r.mu.Unlock()

	s := Summary{Count: len(r.samples), Errors: r.errs, Duration: wall}
	if len(r.samples) == 0 {
		return s
	}
	sort.Slice(r.samples, func(i, j int) bool { return r.samples[i] < r.samples[j] })
	s.Min = r.samples[0]
	s.Max = r.samples[len(r.samples)-1]

	var sum int64
	for _, v := range r.samples {
		sum += int64(v)
	}
	s.Mean = time.Duration(sum / int64(len(r.samples)))
	s.P50 = quantile(r.samples, 0.50)
	s.P95 = quantile(r.samples, 0.95)
	s.P99 = quantile(r.samples, 0.99)
	s.P999 = quantile(r.samples, 0.999)
	if wall > 0 {
		s.QPS = float64(len(r.samples)) / wall.Seconds()
	}
	return s
}

func quantile(sorted []time.Duration, q float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(math.Ceil(q*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}
