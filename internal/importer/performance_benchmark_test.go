package importer

import (
	"encoding/csv"
	"fmt"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/diagnostics"
	"strings"
	"testing"
	"time"
)

var benchmarkArgs []any
var benchmarkSQL string
var benchmarkRanges []insertRange

func benchmarkRows(n, columns int) [][]any {
	rows := make([][]any, n)
	for i := range rows {
		rows[i] = make([]any, columns)
		for j := range rows[i] {
			rows[i][j] = "Unicode中文 <p>text</p> {\"key\":123}"
		}
	}
	return rows
}

// Baseline deliberately preserves the pre-P1 allocation algorithm.
func BenchmarkFlatten(b *testing.B) {
	rows := benchmarkRows(1000, 32)
	for _, preallocate := range []bool{false, true} {
		b.Run(fmt.Sprintf("preallocate=%t", preallocate), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				var args []any
				if preallocate {
					args = make([]any, 0, len(rows)*len(rows[0]))
				}
				for _, row := range rows {
					args = append(args, row...)
				}
				benchmarkArgs = args
			}
		})
	}
}

func BenchmarkBuildInsertQuery(b *testing.B) {
	columns := make([]string, 32)
	for i := range columns {
		columns[i] = fmt.Sprintf("column_%d", i)
	}
	bi := NewBatchInserter(nil, "synthetic", columns, 1000, "replace")
	b.ReportAllocs()
	for b.Loop() {
		benchmarkSQL = bi.buildInsertQuery(1000)
	}
}

func BenchmarkPlanInsertRanges(b *testing.B) {
	rows := benchmarkRows(1000, 32)
	b.ReportAllocs()
	for b.Loop() {
		benchmarkRanges = planInsertRanges(rows, 500, 64*1024)
	}
}

func BenchmarkCSVParseNormalize(b *testing.B) {
	data := strings.Repeat("123,\"Unicode中文, text\",\"line1\nline2\",NULL,\n", 100)
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	for b.Loop() {
		reader := csv.NewReader(strings.NewReader(data))
		for {
			row, err := reader.Read()
			if err != nil {
				break
			}
			benchmarkArgs = PreprocessRow(row)
		}
	}
}

// This is a synthetic client-only batch, not a database throughput benchmark.
// The enabled case includes the actual five-second background sampler.
func BenchmarkOfflineBatchObservation(b *testing.B) {
	data := strings.Repeat("123,\"Unicode中文, text\",\"line1\nline2\",NULL,\n", 1000)
	for _, enabled := range []bool{false, true} {
		b.Run(fmt.Sprintf("enabled=%t", enabled), func(b *testing.B) {
			var r *diagnostics.Recorder
			var stats *diagnostics.Stats
			if enabled {
				var err error
				r, err = diagnostics.Open(b.TempDir(), []byte(strings.Repeat("x", 32)), 5*time.Second, diagnostics.Settings{}, "benchmark", "synthetic")
				if err != nil {
					b.Fatal(err)
				}
				if err = r.Scope([]string{"T"}, nil, 0); err != nil {
					b.Fatal(err)
				}
				stats = r.TableStats("T")
				defer r.Close(false, false)
			}
			b.ReportAllocs()
			b.SetBytes(int64(len(data)))
			for b.Loop() {
				done := stats.Start(diagnostics.ReadParse)
				reader := csv.NewReader(strings.NewReader(data))
				rows := make([][]any, 0, 1000)
				for {
					row, err := reader.Read()
					if err != nil {
						break
					}
					rows = append(rows, PreprocessRow(row))
				}
				done()
				stats.Add(func(c *diagnostics.Counters) { c.ParsedRows += int64(len(rows)); c.CSVBytes += int64(len(data)) })
				done = stats.Start(diagnostics.Plan)
				benchmarkRanges = planInsertRanges(rows, 500, 65536)
				done()
				// Approximate remaining per-batch instrumentation without simulating DB latency.
				for range 8 {
					finish := stats.Start(diagnostics.Batch)
					finish()
				}
				stats.Add(func(c *diagnostics.Counters) { c.ConsumedRows += int64(len(rows)); c.AffectedRows += int64(len(rows)) })
			}
		})
	}
}

func BenchmarkCSVRepair(b *testing.B) {
	columns := []dbColumnInfo{{Name: "value", Type: "text"}}
	row := []string{"HTML <p>中文</p> JSON", "with delimiter"}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := repairDelimitedRow(row, columns); err != nil {
			b.Fatal(err)
		}
	}
}
