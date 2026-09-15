package diagnostics

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func testRecorder(t *testing.T) *Recorder {
	t.Helper()
	r, err := Open(t.TempDir(), []byte(strings.Repeat("private-password-dsn", 3)), time.Second, Settings{}, "test", "user:password@tcp(10.2.3.4)/secret")
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Scope([]string{"business-secret"}, map[string]string{"business-secret": "/sensitive/absolute/path.csv"}, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close(false, false) })
	return r
}
func TestReportLifecycleAndRedaction(t *testing.T) {
	r := testRecorder(t)
	r.TableStats("business-secret").Add(func(c *Counters) { c.ConsumedRows = 12; c.AffectedRows = 24 })
	r.TableStats("business-secret").Observe(Exec, 4*time.Millisecond)
	r.State("business-secret", "success", "match", 12)
	r.Failure("business-secret", Exec, "business-value-password", 1062)
	if err := r.Close(true, false); err != nil {
		t.Fatal(err)
	}
	result, err := ReadReport(r.Dir())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Summary.Success || !result.Summary.Complete || result.Summary.Counters.ConsumedRows != 12 {
		t.Fatalf("bad summary: %+v", result.Summary)
	}
	// Even arbitrary local prose/logs must not enter the export.
	if err := os.WriteFile(filepath.Join(r.Dir(), "report.md"), []byte("business-secret raw DSN password"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.Dir(), "errors.log"), []byte("business-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "export.tar.gz")
	if err := Export(r.Dir(), dest); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	count := 0
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		count++
		data, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{"business-secret", "business-value", "password", "10.2.3.4", "/sensitive", "private-password-dsn"} {
			if strings.Contains(string(data), secret) {
				t.Fatalf("%s leaks %q", h.Name, secret)
			}
		}
	}
	if count != 5 {
		t.Fatalf("got %d artifacts", count)
	}
}
func TestReportPartialAndCorruptRejected(t *testing.T) {
	r := testRecorder(t)
	if _, err := ReadReport(r.Dir()); err == nil {
		t.Fatal("running report accepted")
	}
	r.State("business-secret", "success", "match", 0)
	if err := r.Close(true, false); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.Dir(), "metrics.jsonl"), []byte("{truncated"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadReport(r.Dir()); err == nil {
		t.Fatal("corrupt report accepted")
	}
}
func TestReportOutputFailureIsIncomplete(t *testing.T) {
	r := testRecorder(t)
	path := filepath.Join(r.Dir(), "tables.jsonl")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(true, false); err == nil {
		t.Fatal("output failure ignored")
	}
	var s Summary
	if err := decodeFile(filepath.Join(r.Dir(), "summary.json"), &s); err != nil {
		t.Fatal(err)
	}
	if s.Complete {
		t.Fatal("failed output marked complete")
	}
}
func TestConcurrentStatsAndCancellation(t *testing.T) {
	r := testRecorder(t)
	stats := r.TableStats("business-secret")
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 1000 {
				stats.Add(func(c *Counters) { c.ParsedRows++ })
				stats.Observe(Exec, time.Millisecond)
			}
		})
	}
	wg.Wait()
	if err := r.Close(false, true); err != nil {
		t.Fatal(err)
	}
	report, err := ReadReport(r.Dir())
	if err != nil {
		t.Fatal(err)
	}
	if report.Summary.Counters.ParsedRows != 8000 || report.Summary.Times["exec"].Samples != 8000 || report.Summary.Success || !report.Summary.Cancelled {
		t.Fatal("lost concurrent stats/cancel status")
	}
	if _, err := Compare(r.Dir(), r.Dir()); err == nil {
		t.Fatal("cancelled report compared")
	}
}
func TestReportInitializationAndScopeLimits(t *testing.T) {
	for _, key := range [][]byte{nil, []byte("short")} {
		if _, err := Open(t.TempDir(), key, time.Second, Settings{}, "test", ""); err == nil {
			t.Fatal("weak key accepted")
		}
	}
	r := testRecorder(t)
	if err := r.Scope(make([]string, MaxTables+1), nil, 0); err == nil {
		t.Fatal("unbounded scope accepted")
	}
}
func TestHistogram(t *testing.T) {
	var d Distribution
	for _, value := range []time.Duration{0, time.Microsecond, 3 * time.Microsecond} {
		d.observe(value)
	}
	if d.Samples != 3 || d.TotalNS != 4000 || d.Quantile(.5) != 1000 || d.Quantile(.99) != 4000 {
		t.Fatalf("unexpected histogram: %+v", d)
	}
	// Encoding has fixed space independent of event count.
	data, _ := json.Marshal(d)
	if len(data) > 600 {
		t.Fatal("unbounded histogram")
	}
}
func BenchmarkObservation(b *testing.B) {
	for _, enabled := range []bool{false, true} {
		name := "off"
		var s *Stats
		if enabled {
			name = "on"
			s = &Stats{}
		}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				s.Observe(Exec, time.Millisecond)
				s.Add(func(c *Counters) { c.ConsumedRows += 1000 })
			}
		})
	}
}
