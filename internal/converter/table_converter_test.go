package converter

import (
	"strconv"
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
				Name:     "varchar_col_" + strconv.Itoa(i),
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
			name := "varchar_col_" + strconv.Itoa(i)
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
				Name:     "small_varchar_" + strconv.Itoa(i),
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
			name := "small_varchar_" + strconv.Itoa(i)
			if strings.Contains(ddl, "`"+name+"` text") {
				t.Errorf("DDL should not contain `%s` text (below threshold), got:\n%s", name, ddl)
			}
		}
	})

	t.Run("11 nvarchar(200) cols exceed threshold, convert to TEXT", func(t *testing.T) {
		cols := make([]parser.ColumnDef, 11)
		for i := range cols {
			cols[i] = parser.ColumnDef{
				Name:     "nvarchar_col_" + strconv.Itoa(i),
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

func TestConvertToMySQL_SizeThresholdConversion(t *testing.T) {
	cfg := config.ConverterConfig{
		MaxVarcharToTextColumns:  10,
		MaxNvarcharToTextColumns: 10,
		MaxNvarcharToTextSize:    500,
		MaxVarcharToTextSize:     500,
	}
	tc := NewTableConverter(cfg)

	t.Run("nvarchar(500) converts to TEXT by size", func(t *testing.T) {
		cols := []parser.ColumnDef{
			{Name: "col500", Type: "nvarchar(500) NULL", Nullable: true},
		}
		tableDDL := &parser.TableDDL{TableName: "t1", Columns: cols}
		ddl, err := tc.ConvertToMySQL(tableDDL)
		if err != nil {
			t.Fatalf("ConvertToMySQL returned error: %v", err)
		}
		if !strings.Contains(ddl, "`col500` text") {
			t.Errorf("DDL should contain `col500` text, got:\n%s", ddl)
		}
		if strings.Contains(ddl, "varchar(500)") {
			t.Errorf("DDL should not contain varchar(500), got:\n%s", ddl)
		}
	})

	t.Run("nvarchar(499) stays VARCHAR by size", func(t *testing.T) {
		cols := []parser.ColumnDef{
			{Name: "col499", Type: "nvarchar(499) NULL", Nullable: true},
		}
		tableDDL := &parser.TableDDL{TableName: "t2", Columns: cols}
		ddl, err := tc.ConvertToMySQL(tableDDL)
		if err != nil {
			t.Fatalf("ConvertToMySQL returned error: %v", err)
		}
		if !strings.Contains(ddl, "varchar(499)") {
			t.Errorf("DDL should contain varchar(499), got:\n%s", ddl)
		}
	})

	t.Run("varchar(500) converts to TEXT by size", func(t *testing.T) {
		cols := []parser.ColumnDef{
			{Name: "vc500", Type: "varchar(500) NULL", Nullable: true},
		}
		tableDDL := &parser.TableDDL{TableName: "t3", Columns: cols}
		ddl, err := tc.ConvertToMySQL(tableDDL)
		if err != nil {
			t.Fatalf("ConvertToMySQL returned error: %v", err)
		}
		if !strings.Contains(ddl, "`vc500` text") {
			t.Errorf("DDL should contain `vc500` text, got:\n%s", ddl)
		}
	})

	t.Run("varchar(499) stays VARCHAR by size", func(t *testing.T) {
		cols := []parser.ColumnDef{
			{Name: "vc499", Type: "varchar(499) NULL", Nullable: true},
		}
		tableDDL := &parser.TableDDL{TableName: "t4", Columns: cols}
		ddl, err := tc.ConvertToMySQL(tableDDL)
		if err != nil {
			t.Fatalf("ConvertToMySQL returned error: %v", err)
		}
		if !strings.Contains(ddl, "varchar(499)") {
			t.Errorf("DDL should contain varchar(499), got:\n%s", ddl)
		}
	})

	t.Run("size OR count - size triggers even with few columns", func(t *testing.T) {
		cols := []parser.ColumnDef{
			{Name: "c1", Type: "nvarchar(500) NULL", Nullable: true},
			{Name: "c2", Type: "nvarchar(500) NULL", Nullable: true},
			{Name: "c3", Type: "nvarchar(500) NULL", Nullable: true},
		}
		tableDDL := &parser.TableDDL{TableName: "t5", Columns: cols}
		ddl, err := tc.ConvertToMySQL(tableDDL)
		if err != nil {
			t.Fatalf("ConvertToMySQL returned error: %v", err)
		}
		for _, name := range []string{"c1", "c2", "c3"} {
			if !strings.Contains(ddl, "`"+name+"` text") {
				t.Errorf("DDL should contain `%s` text (size trigger), got:\n%s", name, ddl)
			}
		}
	})
}
