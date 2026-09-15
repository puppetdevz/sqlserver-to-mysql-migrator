// Package csvsample extracts complete logical CSV records without modifying the source.
package csvsample

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Extract copies up to limit logical records from src to dest using encoding/csv.
// If hasHeader is true the header is preserved and does not count toward limit.
// The source file is never modified. Physical-line tools such as head/split are
// not used because quoted fields may contain newlines.
func Extract(src, dest string, limit int, hasHeader bool) (int, error) {
	if src == "" || dest == "" || src == dest {
		return 0, fmt.Errorf("extract requires distinct source and destination files")
	}
	if limit < 1 {
		return 0, fmt.Errorf("extract record limit must be >= 1")
	}
	in, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil && dest != filepath.Base(dest) {
		return 0, err
	}
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return 0, err
	}
	ok := false
	defer func() {
		_ = out.Close()
		if !ok {
			_ = os.Remove(dest)
		}
	}()
	reader := csv.NewReader(in)
	reader.FieldsPerRecord = -1
	// LazyQuotes matches production import for dirty source quotes. Field-count
	// repair is still done by importer.repairDelimitedRow, not by this flag.
	reader.LazyQuotes = true
	writer := csv.NewWriter(out)
	copied := 0
	if hasHeader {
		header, err := reader.Read()
		if err == io.EOF {
			writer.Flush()
			ok = writer.Error() == nil
			return 0, writer.Error()
		}
		if err != nil {
			return 0, err
		}
		if err := writer.Write(header); err != nil {
			return 0, err
		}
	}
	for copied < limit {
		row, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return copied, err
		}
		if err := writer.Write(row); err != nil {
			return copied, err
		}
		copied++
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return copied, err
	}
	ok = true
	return copied, nil
}
