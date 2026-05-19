package converter

import (
	"strings"
	"testing"

	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/config"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/parser"
)

func TestConvertToMySQL_ThresholdConversion(t *testing.T) {
	cfg := config.ConverterConfig{
		MaxVarcharToTextColumns:  10,
		MaxNvarcharToTextColumns: 10,
	}
	tc := NewTableConverter(cfg)

	t.Run("11 varchar(300) cols exceed threshold, convert to TEXT", func(t *testing.T) {
		cols := make([]parser.ColumnDef, 11)
		for i := range cols {
			cols[i] = parser.ColumnDef{
				Name:     "varchar_col_" + string(rune('0'+i)),
				Type:     "varchar(300) NULL",
				Nullable: true,
			}
		}
		tableDDL := &parser.TableDDL{
			TableName: "test_table",
			Columns:   cols,
		}
		ddl, err := tc.ConvertToMySQL(tableDDL)
		if err != nil {
			t.Fatalf("ConvertToMySQL returned error: %v", err)
		}
		// All large varchar cols should be TEXT
		for i := 0; i < 11; i++ {
			name := "varchar_col_" + string(rune('0'+i))
			if !strings.Contains(ddl, "`"+name+"` text") {
				t.Errorf("DDL should contain `%s` text, got:\n%s", name, ddl)
			}
		}
		// Should not contain varchar(300)
		if strings.Contains(ddl, "varchar(300)") {
			t.Errorf("DDL should not contain varchar(300), got:\n%s", ddl)
		}
	})

	t.Run("9 varchar(300) cols below threshold, keep VARCHAR", func(t *testing.T) {
		cols := make([]parser.ColumnDef, 9)
		for i := range cols {
			cols[i] = parser.ColumnDef{
				Name:     "small_varchar_" + string(rune('0'+i)),
				Type:     "varchar(300) NULL",
				Nullable: true,
			}
		}
		tableDDL := &parser.TableDDL{
			TableName: "test_table2",
			Columns:   cols,
		}
		ddl, err := tc.ConvertToMySQL(tableDDL)
		if err != nil {
			t.Fatalf("ConvertToMySQL returned error: %v", err)
		}
		// Should contain varchar(300)
		if !strings.Contains(ddl, "varchar(300)") {
			t.Errorf("DDL should contain varchar(300), got:\n%s", ddl)
		}
		// Should not contain TEXT for these columns
		for i := 0; i < 9; i++ {
			name := "small_varchar_" + string(rune('0'+i))
			if strings.Contains(ddl, "`"+name+"` text") {
				t.Errorf("DDL should not contain `%s` text (below threshold), got:\n%s", name, ddl)
			}
		}
	})

	t.Run("11 nvarchar(200) cols exceed threshold, convert to TEXT", func(t *testing.T) {
		cols := make([]parser.ColumnDef, 11)
		for i := range cols {
			cols[i] = parser.ColumnDef{
				Name:     "nvarchar_col_" + string(rune('0'+i)),
				Type:     "nvarchar(200) NULL",
				Nullable: true,
			}
		}
		tableDDL := &parser.TableDDL{
			TableName: "test_table3",
			Columns:   cols,
		}
		ddl, err := tc.ConvertToMySQL(tableDDL)
		if err != nil {
			t.Fatalf("ConvertToMySQL returned error: %v", err)
		}
		// Should not contain varchar(200) - should be TEXT
		if strings.Contains(ddl, "varchar(200)") {
			t.Errorf("DDL should not contain varchar(200), got:\n%s", ddl)
		}
	})

	t.Run("ROW_FORMAT is DYNAMIC", func(t *testing.T) {
		tableDDL := &parser.TableDDL{
			TableName: "test_table4",
			Columns: []parser.ColumnDef{
				{Name: "id", Type: "int NOT NULL", Nullable: false},
			},
		}
		ddl, err := tc.ConvertToMySQL(tableDDL)
		if err != nil {
			t.Fatalf("ConvertToMySQL returned error: %v", err)
		}
		if !strings.Contains(ddl, "ROW_FORMAT=DYNAMIC") {
			t.Errorf("DDL should contain ROW_FORMAT=DYNAMIC, got:\n%s", ddl)
		}
		if strings.Contains(ddl, "COMPRESSED") || strings.Contains(ddl, "KEY_BLOCK_SIZE") {
			t.Errorf("DDL should not contain COMPRESSED or KEY_BLOCK_SIZE, got:\n%s", ddl)
		}
	})
}