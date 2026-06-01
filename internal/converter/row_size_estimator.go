package converter

import (
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/parser"
)

const (
	innodbRowHeaderBytes       = 64
	innodbVarlenDirectoryBytes = 2
	innodbNullBitmapBaseBytes  = 1
)

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
