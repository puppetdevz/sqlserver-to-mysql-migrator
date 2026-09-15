// Package diagnostics provides opt-in, bounded, file-only migration telemetry.
// No API accepts SQL, row values, DSNs or raw errors for serialization.
package diagnostics

import (
	"math"
	"sync"
	"time"
)

type Metric uint8

const (
	Initialization Metric = iota
	DDL
	Metadata
	Create
	Truncate
	Prescan
	Pipeline
	Validation
	Registration
	ReadParse
	ConvertRepair
	Plan
	SQL
	Prepare
	Exec
	ResultRead
	EnqueueWait
	DequeueWait
	TokenWait
	RetryWait
	Batch
	metricCount
)

var metricNames = [...]string{"initialization", "ddl", "metadata", "create", "truncate", "prescan", "pipeline", "validation", "registration", "read_parse", "convert_repair", "plan", "sql", "prepare", "exec", "result_read", "enqueue_wait", "dequeue_wait", "token_wait", "retry_wait", "batch"}

// Fixed logarithmic buckets: <=1us,2us,...,~35min,+Inf. Quantiles
// are upper bounds, not interpolated exact observations. Service times overlap.
type Distribution struct {
	Samples uint64     `json:"samples"`
	TotalNS int64      `json:"total_ns"`
	Buckets [33]uint64 `json:"buckets"`
}

func (d *Distribution) observe(elapsed time.Duration) {
	if elapsed < 0 {
		elapsed = 0
	}
	d.Samples++
	d.TotalNS += int64(elapsed)
	i, upper := 0, time.Microsecond
	for i < 32 && elapsed > upper {
		i++
		upper *= 2
	}
	d.Buckets[i]++
}
func (d Distribution) Quantile(q float64) int64 {
	if d.Samples == 0 {
		return 0
	}
	target := uint64(math.Ceil(float64(d.Samples) * q))
	if target < 1 {
		target = 1
	}
	var count uint64
	for i, n := range d.Buckets {
		count += n
		if count >= target {
			if i == 32 {
				return -1
			}
			return int64(time.Microsecond) << i
		}
	}
	return -1
}

type Counters struct {
	ActualBatchRows  int64 `json:"actual_exec_input_rows"`
	ActualBatchBytes int64 `json:"actual_exec_estimated_bytes"`
	MinBatchRows     int64 `json:"min_exec_batch_rows"`
	MaxBatchRows     int64 `json:"max_exec_batch_rows"`
	MinBatchBytes    int64 `json:"min_exec_batch_bytes"`
	MaxBatchBytes    int64 `json:"max_exec_batch_bytes"`
	ParsedRows       int64 `json:"parsed_rows"`
	ConsumedRows     int64 `json:"consumed_rows"`
	AffectedRows     int64 `json:"affected_rows"`
	StructureErrors  int64 `json:"structure_errors"`
	Repairs          int64 `json:"repairs"`
	CSVBytes         int64 `json:"pipeline_csv_bytes"`
	PrescanBytes     int64 `json:"prescan_csv_bytes"`
	BatchRows        int64 `json:"batch_input_rows"`
	BatchBytes       int64 `json:"batch_estimated_bytes"`
	SubBatches       int64 `json:"sub_batches"`
	BatchesCached    int64 `json:"batches_cached_stmt"`
	BatchesUncached  int64 `json:"batches_uncached_stmt"`
	BatchesSplit     int64 `json:"batches_split"`
	Prepares         int64 `json:"prepares"`
	CacheHits        int64 `json:"cache_hits"`
	Retries          int64 `json:"retries"`
	UnknownCommits   int64 `json:"unknown_commits"`
}

type Stats struct {
	mu       sync.Mutex
	counters Counters
	times    [metricCount]Distribution
}

func (s *Stats) Observe(metric Metric, elapsed time.Duration) {
	if s == nil || metric >= metricCount {
		return
	}
	s.mu.Lock()
	s.times[metric].observe(elapsed)
	s.mu.Unlock()
}
func (s *Stats) Start(metric Metric) func() {
	if s == nil {
		return func() {}
	}
	start := time.Now()
	return func() { s.Observe(metric, time.Since(start)) }
}

// Add executes only a small in-memory update; never call blocking work in f.
func (s *Stats) Add(f func(*Counters)) {
	if s == nil {
		return
	}
	s.mu.Lock()
	f(&s.counters)
	s.mu.Unlock()
}

// mergeInto aggregates without allocating a map per table on periodic samples.
func (s *Stats) mergeInto(counters *Counters, times map[string]Distribution) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	addCounters(counters, s.counters)
	for i, name := range metricNames {
		if s.times[i].Samples == 0 {
			continue
		}
		d := s.times[i]
		g := times[name]
		g.Samples += d.Samples
		g.TotalNS += d.TotalNS
		for j, n := range d.Buckets {
			g.Buckets[j] += n
		}
		times[name] = g
	}
}

func (s *Stats) Snapshot() (Counters, map[string]Distribution) {
	out := make(map[string]Distribution, metricCount)
	if s == nil {
		return Counters{}, out
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, name := range metricNames {
		out[name] = s.times[i]
	}
	return s.counters, out
}
