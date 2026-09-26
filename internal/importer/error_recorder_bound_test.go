package importer

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestErrorRecorderKeepsBoundedSamplesAndExactTotal(t *testing.T) {
	recorder, err := NewErrorRecorder("")
	if err != nil {
		t.Fatal(err)
	}
	defer recorder.Close()
	for i := 0; i < 1000; i++ {
		recorder.RecordError("T", "", nil, errors.New("bad CSV row"))
	}
	if got := recorder.GetErrorCount(); got != 1000 {
		t.Fatalf("count = %d, want 1000", got)
	}
	if got := len(recorder.GetErrors()); got > 128 {
		t.Fatalf("retained %d samples for 1000 errors", got)
	}
}

func TestErrorRecorderLimitsLargeErrorAndRowSample(t *testing.T) {
	path := filepath.Join(t.TempDir(), "errors.log")
	recorder, err := NewErrorRecorder(path)
	if err != nil {
		t.Fatal(err)
	}
	defer recorder.Close()
	large := strings.Repeat("x", 10000)
	for i := 0; i < 150; i++ {
		recorder.RecordBatchError("T", i, [][]any{{large, large}}, errors.New(large))
	}
	if got := recorder.GetErrorCount(); got != 150 {
		t.Fatalf("count = %d, want 150", got)
	}
	for _, sample := range recorder.GetErrors() {
		if len(sample.Error) > 256 || len(sample.RowData) > 3 {
			t.Fatalf("unbounded error sample: error length=%d, fields=%d", len(sample.Error), len(sample.RowData))
		}
		for _, cell := range sample.RowData {
			if len(cell) > 128 {
				t.Fatalf("unbounded row sample length %d", len(cell))
			}
		}
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "batch=149") {
		t.Fatal("later error was not streamed to file after sample cap")
	}
}
