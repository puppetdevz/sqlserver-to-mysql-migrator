package diagnostics

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"sync"
	"time"
)

const Schema = 1
const MaxTables = 10000

// Periodic output is capped independently of run duration; one final snapshot
// is reserved. Dropped samples are disclosed, never treated as business losses.
const maxMetricsBytes = 48 << 20

// Settings is deliberately a whitelist, not an arbitrary map/config/DSN.
type Settings struct {
	BatchSize          int   `json:"batch_size"`
	MaxBatchBytes      int   `json:"max_batch_bytes"`
	MaxWorkers         int   `json:"max_workers"`
	ImportTokens       int   `json:"import_tokens"`
	MaxOpenConns       int   `json:"max_open_conns"`
	MaxIdleConns       int   `json:"max_idle_conns"`
	MaxRowsPerTable    int   `json:"max_rows_per_table"`
	MaxInflightBytes   int64 `json:"max_inflight_bytes"`
	MaxInflightBatches int   `json:"max_inflight_batches"`
	QueueBytes         int64 `json:"queue_bytes"`
	QueueBatches       int   `json:"queue_batches"`
	BatchMemoryBytes   int64 `json:"batch_memory_bytes"`
	SQLCacheBytes      int64 `json:"sql_cache_bytes"`
	PreCount           bool  `json:"pre_count"`
	ValidateCount      bool  `json:"validate_count"`
	Replace            bool  `json:"replace"`
	Adaptive           bool  `json:"adaptive"`
	CaseSensitive      bool  `json:"case_sensitive"`
	HasHeader          bool  `json:"has_header"`
	DryRun             bool  `json:"dry_run"`
	CreateOnly         bool  `json:"create_only"`
}
type ResourceLimits struct {
	MaxInflightBytes   int64 `json:"max_inflight_bytes"`
	MaxInflightBatches int   `json:"max_inflight_batches"`
	QueueBytes         int64 `json:"queue_bytes"`
	QueueBatches       int   `json:"queue_batches"`
	BatchMemoryBytes   int64 `json:"batch_memory_bytes"`
	SQLCacheBytes      int64 `json:"sql_cache_bytes"`
}

type Manifest struct {
	Resources        *ResourceLimits `json:"resources,omitempty"`
	Schema           int             `json:"schema"`
	RunID            string          `json:"run_id"`
	Started          time.Time       `json:"started"`
	Version          string          `json:"version"`
	Commit           string          `json:"commit"`
	Dirty            bool            `json:"dirty"`
	Go               string          `json:"go_version"`
	Driver           string          `json:"driver_version"`
	OS               string          `json:"os"`
	Arch             string          `json:"arch"`
	CPUs             int             `json:"cpus"`
	GOMAXPROCS       int             `json:"gomaxprocs"`
	GOMEMLIMIT       int64           `json:"gomemlimit_bytes"`
	Strategy         string          `json:"strategy"`
	Settings         Settings        `json:"settings"`
	TargetID         string          `json:"target_id"`
	KeyID            string          `json:"key_id"`
	ScopeFingerprint string          `json:"scope_fingerprint"`
	Selected         int             `json:"selected_tables"`
	Skipped          int             `json:"skipped_tables"`
	SampleIntervalNS int64           `json:"sample_interval_ns"`
}
type Table struct {
	ID            string                  `json:"id"`
	FileBytes     int64                   `json:"file_bytes"`
	FileAvailable bool                    `json:"file_available"`
	State         string                  `json:"state"`
	CountStatus   string                  `json:"count_status"`
	Count         int64                   `json:"count"`
	ErrorStage    string                  `json:"error_stage,omitempty"`
	ErrorClass    string                  `json:"error_class,omitempty"`
	ErrorCode     uint16                  `json:"error_code,omitempty"`
	Counters      Counters                `json:"counters"`
	Times         map[string]Distribution `json:"times"`
}
type Pool struct {
	Open              int   `json:"open"`
	InUse             int   `json:"in_use"`
	Idle              int   `json:"idle"`
	WaitCount         int64 `json:"wait_count_delta"`
	WaitNS            int64 `json:"wait_ns_delta"`
	MaxIdleClosed     int64 `json:"max_idle_closed_delta"`
	MaxIdleTimeClosed int64 `json:"max_idle_time_closed_delta"`
	MaxLifetimeClosed int64 `json:"max_lifetime_closed_delta"`
}
type Snapshot struct {
	At                    time.Time               `json:"at"`
	ElapsedNS             int64                   `json:"elapsed_ns"`
	IntervalNS            int64                   `json:"interval_ns"`
	Counters              Counters                `json:"counters"`
	Times                 map[string]Distribution `json:"times"`
	HeapAlloc             uint64                  `json:"heap_alloc"`
	RuntimeMemory         uint64                  `json:"runtime_memory"`
	GCCPUFraction         float64                 `json:"gc_cpu_fraction"`
	GCPauseNS             uint64                  `json:"gc_pause_ns"`
	Goroutines            int                     `json:"goroutines"`
	CPUSeconds            float64                 `json:"process_cpu_seconds"`
	RSSBytes              int64                   `json:"rss_bytes"`
	ProcessMetricsStatus  string                  `json:"process_metrics_status"`
	Pool                  Pool                    `json:"pool"`
	PoolAvailable         bool                    `json:"pool_available"`
	ActiveTables          int                     `json:"active_tables"`
	QueuedTables          int                     `json:"queued_tables"`
	UsedTokens            int                     `json:"used_tokens"`
	DynamicLimit          int                     `json:"dynamic_limit"`
	PressureEvents        uint64                  `json:"pressure_events"`
	PressureTypes         [5]uint64               `json:"pressure_types_slow_retry_connection_lock_capacity"`
	RecoveryEvents        uint64                  `json:"recovery_events"`
	InflightBytes         int64                   `json:"inflight_estimated_bytes"`
	CSVBytesPerSecond     float64                 `json:"pipeline_csv_bytes_per_second"`
	ConsumedRowsPerSecond float64                 `json:"consumed_rows_per_second"`
}
type Summary struct {
	Schema                int                     `json:"schema"`
	RunID                 string                  `json:"run_id"`
	Complete              bool                    `json:"report_complete"`
	Success               bool                    `json:"success"`
	Cancelled             bool                    `json:"cancelled"`
	WallNS                int64                   `json:"wall_ns"`
	TailNS                int64                   `json:"tail_few_tables_ns"`
	DroppedSnapshots      uint64                  `json:"dropped_snapshots"`
	Missing               int                     `json:"missing_tables"`
	Failed                int                     `json:"failed_tables"`
	Counters              Counters                `json:"counters"`
	Times                 map[string]Distribution `json:"times"`
	DatabaseMetricsStatus string                  `json:"database_metrics_status"`
}

type Recorder struct {
	Stats      Stats
	mu         sync.Mutex
	dir        string
	key        []byte
	manifest   Manifest
	tables     map[string]*Table
	tableStats []*Stats
	stats      map[string]*Stats
	start      time.Time
	stop       chan struct{}
	done       chan struct{}
	once       sync.Once
	outputErr  error
	db         *sql.DB
	scheduler  Snapshot
	tailStart  time.Time
	tailNS     int64
	dropped    uint64
}

// Open creates an exclusive run directory; all output files are opened before
// returning so explicitly requested diagnostics fail before any database writes.
// The caller retains the private key; it is never placed in the report directory.
func Open(root string, key []byte, interval time.Duration, settings Settings, version, target string, started ...time.Time) (*Recorder, error) {
	if root == "" || len(key) < 32 || interval < time.Second {
		return nil, errors.New("diagnostics requires explicit directory, >=32-byte key and interval >=1s")
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	now := time.Now()
	if len(started) > 0 && !started[0].IsZero() && !started[0].After(now) {
		now = started[0]
	}
	id := hex.EncodeToString(nonce[:])
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		return nil, err
	}
	r := &Recorder{dir: dir, key: append([]byte(nil), key...), tables: make(map[string]*Table), stats: make(map[string]*Stats), start: now, stop: make(chan struct{}), done: make(chan struct{})}
	r.manifest = Manifest{Schema: Schema, RunID: id, Started: now.UTC(), Version: version, Go: runtime.Version(), OS: runtime.GOOS, Arch: runtime.GOARCH, CPUs: runtime.NumCPU(), GOMAXPROCS: runtime.GOMAXPROCS(0), GOMEMLIMIT: debug.SetMemoryLimit(-1), Strategy: "prepared", Settings: settings, SampleIntervalNS: int64(interval)}
	emptyScope := sha256.Sum256(nil)
	r.manifest.ScopeFingerprint = hex.EncodeToString(emptyScope[:])
	r.manifest.TargetID = r.Pseudonym("target:" + target)
	r.manifest.KeyID = r.Pseudonym("key-id")
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, s := range info.Settings {
			if s.Key == "vcs.revision" {
				r.manifest.Commit = s.Value
			}
			if s.Key == "vcs.modified" {
				r.manifest.Dirty = s.Value == "true"
			}
		}
		for _, d := range info.Deps {
			if d.Path == "github.com/go-sql-driver/mysql" {
				r.manifest.Driver = d.Version
			}
		}
	}
	if r.manifest.Commit == "" {
		r.manifest.Commit = "unavailable"
	}
	if r.manifest.Driver == "" {
		r.manifest.Driver = "unavailable"
	}
	for _, name := range []string{"manifest.json", "summary.json", "tables.jsonl", "metrics.jsonl", "report.md"} {
		f, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return nil, err
		}
		if err = f.Close(); err != nil {
			return nil, err
		}
	}
	if err := r.writeJSON("manifest.json", r.manifest); err != nil {
		return nil, err
	}
	if err := r.writeJSON("summary.json", Summary{Schema: Schema, RunID: id, Complete: false}); err != nil {
		return nil, err
	}
	metricsFile, err := os.OpenFile(filepath.Join(dir, "metrics.jsonl"), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	go r.sampleLoop(interval, metricsFile)
	return r, nil
}
func (r *Recorder) Dir() string {
	if r == nil {
		return ""
	}
	return r.dir
}
func (r *Recorder) Pseudonym(name string) string {
	h := hmac.New(sha256.New, r.key)
	h.Write([]byte(name))
	return hex.EncodeToString(h.Sum(nil))
}

// SetResources runs before database access; old P0 baseline reports omit this field.
func (r *Recorder) SetResources(limits ResourceLimits) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.manifest.Resources = &limits
	return r.writeJSON("manifest.json", r.manifest)
}

func (r *Recorder) SetDB(db *sql.DB) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.db = db
	r.mu.Unlock()
}
func (r *Recorder) Global() *Stats {
	if r == nil {
		return nil
	}
	return &r.Stats
}
func (r *Recorder) TableStats(name string) *Stats {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stats[r.Pseudonym(name)]
}

// Scope uses file metadata, never scans file contents. Call before writes.
func (r *Recorder) Scope(names []string, files map[string]string, skipped int) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(names) > MaxTables {
		return errors.New("diagnostic table limit exceeded before writes")
	}
	if len(r.tables) > 0 {
		return errors.New("diagnostic scope already initialized")
	}
	ids := make([]string, 0, len(names))
	for _, name := range names {
		id := r.Pseudonym(name)
		t := &Table{ID: id, State: "pending", CountStatus: "not_run"}
		if info, err := os.Stat(files[name]); err == nil && info.Mode().IsRegular() {
			t.FileBytes = info.Size()
			t.FileAvailable = true
		}
		stats := &Stats{}
		r.tables[id] = t
		r.stats[id] = stats
		r.tableStats = append(r.tableStats, stats)
		ids = append(ids, fmt.Sprintf("%s:%d:%t", id, t.FileBytes, t.FileAvailable))
	}
	sort.Strings(ids)
	h := sha256.New()
	for _, id := range ids {
		fmt.Fprintln(h, id)
	}
	r.manifest.ScopeFingerprint = hex.EncodeToString(h.Sum(nil))
	r.manifest.Selected = len(names)
	r.manifest.Skipped = skipped
	r.scheduler.QueuedTables = len(names)
	return r.writeJSON("manifest.json", r.manifest)
}

// State accepts only enumerated states; unknown strings cannot leak business data.
func (r *Recorder) State(name, state, countStatus string, count int64) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	t := r.tables[r.Pseudonym(name)]
	if t == nil {
		return
	}
	switch state {
	case "pending", "active", "success", "failed", "missing", "skipped", "cancelled":
		if t.State == "pending" {
			r.scheduler.QueuedTables--
		}
		if t.State == "active" {
			r.scheduler.ActiveTables--
		}
		t.State = state
		if state == "pending" {
			r.scheduler.QueuedTables++
		}
		if state == "active" {
			r.scheduler.ActiveTables++
		}
		tail := r.scheduler.ActiveTables > 0 && r.scheduler.ActiveTables <= 2 && r.scheduler.QueuedTables == 0
		if tail && r.tailStart.IsZero() {
			r.tailStart = time.Now()
		}
		if !tail && !r.tailStart.IsZero() {
			r.tailNS += int64(time.Since(r.tailStart))
			r.tailStart = time.Time{}
		}
	}
	switch countStatus {
	case "not_run", "disabled", "match", "mismatch", "error":
		t.CountStatus = countStatus
		t.Count = count
	}
}
func (r *Recorder) Failure(name string, stage Metric, class string, code uint16) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	t := r.tables[r.Pseudonym(name)]
	if t == nil {
		return
	}
	if stage < metricCount {
		t.ErrorStage = metricNames[stage]
	}
	switch class {
	case "database", "unknown_commit", "cancelled", "structure", "io", "other":
		t.ErrorClass = class
	default:
		t.ErrorClass = "other"
	}
	t.ErrorCode = code
}
func (r *Recorder) Scheduling(used, limit int, pressure string, recovery bool) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.scheduler.UsedTokens = used
	r.scheduler.DynamicLimit = limit
	for i, name := range []string{"slow_batch", "retry", "connection", "lock_wait", "capacity"} {
		if pressure == name {
			r.scheduler.PressureEvents++
			r.scheduler.PressureTypes[i]++
		}
	}
	if recovery {
		r.scheduler.RecoveryEvents++
	}
}
func (r *Recorder) Inflight(delta int64) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.scheduler.InflightBytes += delta
	r.mu.Unlock()
}
func (r *Recorder) writeJSON(name string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(r.dir, name+".tmp")
	if err = os.WriteFile(tmp, append(data, '\n'), 0600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(r.dir, name))
}
func (r *Recorder) snapshot(previous sql.DBStats, last Counters, elapsed time.Duration) (Snapshot, sql.DBStats) {
	r.mu.Lock()
	s := r.scheduler
	db := r.db
	tableStats := r.tableStats
	r.mu.Unlock()
	s.At = time.Now().UTC()
	s.ElapsedNS = int64(time.Since(r.start))
	s.IntervalNS = int64(elapsed)
	s.Counters, s.Times = r.Stats.Snapshot()
	// Scope publishes immutable Stats pointers before business workers start.
	// Merge without allocating thousands of maps or holding the recorder mutex.
	for _, stats := range tableStats {
		stats.mergeInto(&s.Counters, s.Times)
	}
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	s.HeapAlloc = m.HeapAlloc
	s.RuntimeMemory = m.Sys - m.HeapReleased
	s.GCCPUFraction = m.GCCPUFraction
	s.GCPauseNS = m.PauseTotalNs
	s.Goroutines = runtime.NumGoroutine()
	s.CPUSeconds, s.RSSBytes, s.ProcessMetricsStatus = processMetrics()
	if elapsed > 0 {
		s.CSVBytesPerSecond = float64(s.Counters.CSVBytes-last.CSVBytes) / elapsed.Seconds()
		s.ConsumedRowsPerSecond = float64(s.Counters.ConsumedRows-last.ConsumedRows) / elapsed.Seconds()
	}
	if db != nil {
		curr := db.Stats()
		s.PoolAvailable = true
		s.Pool = Pool{curr.OpenConnections, curr.InUse, curr.Idle, curr.WaitCount - previous.WaitCount, int64(curr.WaitDuration - previous.WaitDuration), curr.MaxIdleClosed - previous.MaxIdleClosed, curr.MaxIdleTimeClosed - previous.MaxIdleTimeClosed, curr.MaxLifetimeClosed - previous.MaxLifetimeClosed}
		previous = curr
	}
	return s, previous
}
func addCounters(a *Counters, b Counters) {
	a.ActualBatchRows += b.ActualBatchRows
	a.ActualBatchBytes += b.ActualBatchBytes
	if b.MinBatchRows > 0 && (a.MinBatchRows == 0 || b.MinBatchRows < a.MinBatchRows) {
		a.MinBatchRows = b.MinBatchRows
	}
	if b.MinBatchBytes > 0 && (a.MinBatchBytes == 0 || b.MinBatchBytes < a.MinBatchBytes) {
		a.MinBatchBytes = b.MinBatchBytes
	}
	a.MaxBatchRows = max(a.MaxBatchRows, b.MaxBatchRows)
	a.MaxBatchBytes = max(a.MaxBatchBytes, b.MaxBatchBytes)
	a.ParsedRows += b.ParsedRows
	a.ConsumedRows += b.ConsumedRows
	a.AffectedRows += b.AffectedRows
	a.StructureErrors += b.StructureErrors
	a.Repairs += b.Repairs
	a.CSVBytes += b.CSVBytes
	a.PrescanBytes += b.PrescanBytes
	a.BatchRows += b.BatchRows
	a.BatchBytes += b.BatchBytes
	a.SubBatches += b.SubBatches
	a.BatchesCached += b.BatchesCached
	a.BatchesUncached += b.BatchesUncached
	a.BatchesSplit += b.BatchesSplit
	a.Prepares += b.Prepares
	a.CacheHits += b.CacheHits
	a.Retries += b.Retries
	a.UnknownCommits += b.UnknownCommits
}
func (r *Recorder) sampleLoop(interval time.Duration, f *os.File) {
	defer close(r.done)
	warned := false
	warn := func() {
		if r.outputErr != nil && !warned {
			fmt.Fprintln(os.Stderr, "Diagnostics output failed: report incomplete; no database writes will be replayed.")
			warned = true
		}
	}
	defer func() {
		if err := f.Close(); err != nil {
			r.outputErr = errors.Join(r.outputErr, err)
		}
		warn()
	}()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var prev sql.DBStats
	var last Counters
	lastAt := r.start
	var written int64
	emit := func(final bool) {
		defer warn()
		now := time.Now()
		elapsed := now.Sub(lastAt)
		if missed := int(elapsed/interval) - 1; missed > 0 {
			r.dropped += uint64(missed)
		}
		s, p := r.snapshot(prev, last, elapsed)
		prev = p
		last = s.Counters
		lastAt = now
		data, err := json.Marshal(s)
		if err != nil {
			r.outputErr = err
			return
		}
		if !final && written+int64(len(data)+1) > maxMetricsBytes {
			r.dropped++
			return
		}
		n, err := f.Write(append(data, '\n'))
		written += int64(n)
		if err != nil {
			r.outputErr = err
		}
	}
	for {
		select {
		case <-ticker.C:
			emit(false)
		case <-r.stop:
			emit(true)
			if err := f.Sync(); err != nil {
				r.outputErr = err
			}
			return
		}
	}
}

// Close must run after all business workers have stopped. A crash never creates
// a complete summary. Telemetry failures never trigger database retries.
func (r *Recorder) Close(success, cancelled bool) error {
	if r == nil {
		return nil
	}
	r.once.Do(func() {
		close(r.stop)
		<-r.done
		r.mu.Lock()
		defer r.mu.Unlock()
		fail := func(err error) {
			if err != nil {
				r.outputErr = errors.Join(r.outputErr, err)
			}
		}
		summary := Summary{Schema: Schema, RunID: r.manifest.RunID, Success: success, Cancelled: cancelled, WallNS: int64(time.Since(r.start)), TailNS: r.tailNS, DroppedSnapshots: r.dropped, DatabaseMetricsStatus: "unavailable: no authorized CN/DN sampler"}
		if !r.tailStart.IsZero() {
			summary.TailNS += int64(time.Since(r.tailStart))
		}
		summary.Counters, summary.Times = r.Stats.Snapshot()
		f, err := os.OpenFile(filepath.Join(r.dir, "tables.jsonl"), os.O_WRONLY|os.O_TRUNC, 0600)
		fail(err)
		ids := make([]string, 0, len(r.tables))
		for id := range r.tables {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			t := r.tables[id]
			if s := r.stats[id]; s != nil {
				t.Counters, t.Times = s.Snapshot()
			}
			addCounters(&summary.Counters, t.Counters)
			for name, d := range t.Times {
				g := summary.Times[name]
				g.Samples += d.Samples
				g.TotalNS += d.TotalNS
				for i, n := range d.Buckets {
					g.Buckets[i] += n
				}
				summary.Times[name] = g
			}
			if t.State == "missing" {
				summary.Missing++
			}
			if t.State == "failed" {
				summary.Failed++
			}
			if t.State == "active" || t.State == "pending" {
				if cancelled {
					t.State = "cancelled"
				}
				summary.Success = false
			}
			if f != nil {
				fail(json.NewEncoder(f).Encode(t))
			}
		}
		if f != nil {
			fail(f.Sync())
			fail(f.Close())
		}
		summary.Complete = r.outputErr == nil
		fail(os.WriteFile(filepath.Join(r.dir, "report.md"), []byte(renderReport(r.manifest, summary)), 0600))
		summary.Complete = r.outputErr == nil
		fail(r.writeJSON("summary.json", summary))
	})
	return r.outputErr
}
func renderReport(m Manifest, s Summary) string {
	text := fmt.Sprintf("# Migration diagnostics (schema %d)\n\nRun: `%s`\n\nReport complete: **%t**; migration success: **%t**; cancelled: %t.\n\nWall time: %.3fs (includes COUNT and closeout up to report generation). Consumed rows: %d; affected rows: %d (diagnostic only).\n\n", Schema, m.RunID, s.Complete, s.Success, s.Cancelled, float64(s.WallNS)/1e9, s.Counters.ConsumedRows, s.Counters.AffectedRows)
	text += "## Timing evidence\n\nService times overlap across stages/workers and MUST NOT be summed as wall time. Fixed power-of-two microsecond buckets; quantiles are bucket upper bounds; -1 means overflow.\n\n|Stage|Samples|Service seconds|p50 ns|p95 ns|p99 ns|\n|---|---:|---:|---:|---:|---:|\n"
	for _, name := range metricNames {
		d := s.Times[name]
		text += fmt.Sprintf("|%s|%d|%.6f|%d|%d|%d|\n", name, d.Samples, float64(d.TotalNS)/1e9, d.Quantile(.5), d.Quantile(.95), d.Quantile(.99))
	}
	text += "\n## Interpretation and missing evidence\n\n- read_parse is whole-batch CSV reading/parsing/normalization/repair service time. convert_repair is per-row repair+preprocess and overlaps read_parse; do not add them as wall time. CSV bytes are parser-consumed file bytes, NOT SQL or network bytes. Pre-scan is separate.\n- Pool waits are process-wide interval deltas, not per-table attribution. Exec includes driver/network/database service and may include pool waits; retry sleep is separate.\n- CPU seconds are cumulative user+system time; 100% utilization means one logical core. GOMEMLIMIT is a soft Go runtime limit, not RSS.\n- RSS availability and measurement semantics are recorded per snapshot. Driver copies and OS buffers are not included in estimated in-flight bytes.\n- CN/DN metrics unavailable: no authorized sampler. No GoldenDB compatibility or speedup is established by this report.\n- If Exec latency/pressure rise, do not increase concurrency; ask DBA for aligned CN/DN CPU, log/disk/replica waits. If writer waits for input, inspect parsing, conversion and GC first.\n- Compare only complete successful reports with identical data, settings, resource budgets and isolated initial states; external writers must be excluded manually.\n"
	return text
}
