package importer

import (
	"fmt"
	"strings"
)

// These reference algorithms are linked only into migration_baseline builds.
// Correctness fixes (overflow checks, unknown commit, CSV semantics) are shared.
// Keeping A reproducible prevents comparing stale/uninstrumented executables.
func baselinePlanRanges(rows [][]any, maxRows int, maxBytes int64) []insertRange {
	if len(rows) == 0 {
		return nil
	}
	if maxRows < 1 {
		maxRows = 1
	}
	ranges := make([]insertRange, 0, (len(rows)-1)/maxRows+1)
	for i := 0; i < len(rows); {
		end := i + min(maxRows, len(rows)-i)
		if maxBytes > 0 {
			for end > i+1 && estimateBatchBytes(rows[i:end]) > maxBytes {
				end = i + (end-i)/2
			}
		}
		ranges = append(ranges, insertRange{i, end})
		i = end
	}
	return ranges
}
func baselineBuildQuery(bi *BatchInserter, numRows int) string {
	var query strings.Builder
	if bi.onDuplicate == "replace" {
		query.WriteString("REPLACE INTO ")
	} else {
		query.WriteString("INSERT IGNORE INTO ")
	}
	query.WriteString(bi.quotedTable + " ")
	query.WriteString(fmt.Sprintf("(%s) VALUES ", strings.Join(bi.quotedColumns, ", ")))
	values := make([]string, numRows)
	for i := range values {
		placeholders := make([]string, len(bi.columns))
		for j := range placeholders {
			placeholders[j] = "?"
		}
		values[i] = fmt.Sprintf("(%s)", strings.Join(placeholders, ", "))
	}
	query.WriteString(strings.Join(values, ", "))
	return query.String()
}
