package converter

import (
	"sort"
	"strconv"
	"strings"

	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/parser"
)

const (
	mysqlPracticalRowLimitBytes = 8126
	mysqlRowSizeSafetyMargin    = 512
	innodbRowHeaderBytes        = 64
	innodbVarlenDirectoryBytes  = 2
	innodbNullBitmapBaseBytes   = 1
)

type degradationCandidateKind int

const (
	degradationCandidateString degradationCandidateKind = iota
	degradationCandidateNumeric
)

type degradationCandidate struct {
	name       string
	sourceType string
	bytes      int
	kind       degradationCandidateKind
}

func (tc *TableConverter) rowSizeSafeLimit() int {
	return mysqlPracticalRowLimitBytes - mysqlRowSizeSafetyMargin
}

func (tc *TableConverter) indexedColumnSet(tableDDL *parser.TableDDL) map[string]bool {
	indexedColumns := make(map[string]bool)
	if tableDDL.PrimaryKey != nil {
		for _, col := range tableDDL.PrimaryKey.Columns {
			indexedColumns[col] = true
		}
	}
	for _, index := range tableDDL.Indexes {
		for _, col := range index.Columns {
			indexedColumns[col] = true
		}
	}
	return indexedColumns
}

func (tc *TableConverter) estimateRowBytes(tableDDL *parser.TableDDL, forceTextColumns map[string]bool) int {
	estimatedBytes := innodbRowHeaderBytes + innodbNullBitmapBaseBytes + ((len(tableDDL.Columns) + 7) / 8)
	for _, column := range tableDDL.Columns {
		width, variable := tc.estimatedInlineBytesAfterForcing(column, forceTextColumns)
		estimatedBytes += width
		if variable {
			estimatedBytes += innodbVarlenDirectoryBytes
		}
	}
	return estimatedBytes
}

func (tc *TableConverter) estimatedInlineBytesAfterForcing(column parser.ColumnDef, forceTextColumns map[string]bool) (int, bool) {
	if forceTextColumns[column.Name] {
		return textInlineOverheadBytes, true
	}
	width, candidate := tc.estimatedInlineBytes(column)
	if candidate {
		return width, true
	}
	return width, false
}

func (tc *TableConverter) aggressiveCandidates(tableDDL *parser.TableDDL, forceTextColumns map[string]bool, allowNumeric bool) []degradationCandidate {
	indexedColumns := tc.indexedColumnSet(tableDDL)
	candidates := make([]degradationCandidate, 0)

	for _, column := range tableDDL.Columns {
		if forceTextColumns[column.Name] || indexedColumns[column.Name] {
			continue
		}
		cleanType := tc.typeMapper.CleanCollation(column.Type)
		typePart := tc.extractType(cleanType)
		sqlType := strings.TrimSpace(strings.ToLower(typePart))

		if bytes, ok := estimateStringCandidateBytes(sqlType); ok {
			candidates = append(candidates, degradationCandidate{
				name:       column.Name,
				sourceType: typePart,
				bytes:      bytes,
				kind:       degradationCandidateString,
			})
			continue
		}

		if allowNumeric && isNumericCandidate(sqlType) {
			width, _ := tc.estimatedInlineBytes(column)
			candidates = append(candidates, degradationCandidate{
				name:       column.Name,
				sourceType: typePart,
				bytes:      width,
				kind:       degradationCandidateNumeric,
			})
		}
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].kind != candidates[j].kind {
			return candidates[i].kind < candidates[j].kind
		}
		if candidates[i].bytes != candidates[j].bytes {
			return candidates[i].bytes > candidates[j].bytes
		}
		return candidates[i].name < candidates[j].name
	})
	return candidates
}

func estimateStringCandidateBytes(sqlType string) (int, bool) {
	if matches := reNvarchar.FindStringSubmatch(sqlType); len(matches) > 0 {
		n, _ := strconv.Atoi(matches[1])
		return n * 4, true
	}
	if matches := reVarchar.FindStringSubmatch(sqlType); len(matches) > 0 {
		if matches[1] == "max" {
			return 0, false
		}
		n, _ := strconv.Atoi(matches[1])
		return n * 4, true
	}
	if matches := reNchar.FindStringSubmatch(sqlType); len(matches) > 0 {
		n, _ := strconv.Atoi(matches[1])
		return n * 4, true
	}
	if matches := reChar.FindStringSubmatch(sqlType); len(matches) > 0 {
		n, _ := strconv.Atoi(matches[1])
		return n * 4, true
	}
	return 0, false
}

func isNumericCandidate(sqlType string) bool {
	return strings.HasPrefix(sqlType, "numeric") || strings.HasPrefix(sqlType, "decimal")
}
