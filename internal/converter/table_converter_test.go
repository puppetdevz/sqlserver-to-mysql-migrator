package converter

import (
	"fmt"
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

	t.Run("6 varchar(300) cols below count threshold and row size guard, keep VARCHAR", func(t *testing.T) {
		cols := make([]parser.ColumnDef, 6)
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
		for i := 0; i < 6; i++ {
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

func TestConvertToMySQL_RowSizeGuardConvertsManyMediumVarchars(t *testing.T) {
	tc := NewTableConverter(config.ConverterConfig{
		MaxVarcharToTextColumns:  10,
		MaxNvarcharToTextColumns: 10,
		MaxNvarcharToTextSize:    500,
		MaxVarcharToTextSize:     500,
	})

	cols := []parser.ColumnDef{
		{Name: "ID", Type: "bigint NOT NULL", Nullable: false},
	}
	for i := 0; i < 180; i++ {
		cols = append(cols, parser.ColumnDef{
			Name:     "field" + strconv.Itoa(i),
			Type:     "nvarchar(100) NULL",
			Nullable: true,
		})
	}
	tableDDL := &parser.TableDDL{
		TableName: "wide_table",
		Columns:   cols,
		PrimaryKey: &parser.PrimaryKeyDef{
			Name:    "PK_wide_table",
			Columns: []string{"ID"},
		},
	}

	ddl, err := tc.ConvertToMySQL(tableDDL)
	if err != nil {
		t.Fatalf("ConvertToMySQL returned error: %v", err)
	}
	textCount := strings.Count(ddl, "` text")
	if textCount < 160 {
		t.Fatalf("DDL should convert most (169/180) nvarchar columns to text, got %d text columns:\n%s", textCount, ddl)
	}
	if !strings.Contains(ddl, "`ID` bigint NOT NULL") || !strings.Contains(ddl, "PRIMARY KEY (`ID`)") {
		t.Fatalf("DDL should keep primary key column inline and indexed, got:\n%s", ddl)
	}
}

func TestConvertToMySQL_RowSizeGuardKeepsIndexedColumnsInline(t *testing.T) {
	tc := NewTableConverter(config.ConverterConfig{
		MaxVarcharToTextColumns:  10,
		MaxNvarcharToTextColumns: 10,
		MaxNvarcharToTextSize:    500,
		MaxVarcharToTextSize:     500,
	})

	cols := []parser.ColumnDef{
		{Name: "ID", Type: "bigint NOT NULL", Nullable: false},
		{Name: "indexed_code", Type: "nvarchar(100) NULL", Nullable: true},
	}
	for i := 0; i < 180; i++ {
		cols = append(cols, parser.ColumnDef{
			Name:     "field" + strconv.Itoa(i),
			Type:     "nvarchar(100) NULL",
			Nullable: true,
		})
	}
	tableDDL := &parser.TableDDL{
		TableName: "wide_table",
		Columns:   cols,
		PrimaryKey: &parser.PrimaryKeyDef{
			Name:    "PK_wide_table",
			Columns: []string{"ID"},
		},
		Indexes: []parser.IndexDef{
			{Name: "IDX_indexed_code", Columns: []string{"indexed_code"}},
		},
	}

	ddl, err := tc.ConvertToMySQL(tableDDL)
	if err != nil {
		t.Fatalf("ConvertToMySQL returned error: %v", err)
	}
	if !strings.Contains(ddl, "`indexed_code` varchar(100) NULL") {
		t.Fatalf("DDL should keep indexed string column inline, got:\n%s", ddl)
	}
	if !strings.Contains(ddl, "CREATE INDEX `IDX_indexed_code` ON `wide_table` (`indexed_code`);") {
		t.Fatalf("DDL should keep index on indexed_code, got:\n%s", ddl)
	}
}

func TestConvertToMySQL_RowSizeGuardWideTableLikeFormmain1980(t *testing.T) {
	tc := NewTableConverter(config.ConverterConfig{
		MaxVarcharToTextColumns:  10,
		MaxNvarcharToTextColumns: 10,
		MaxNvarcharToTextSize:    500,
		MaxVarcharToTextSize:     500,
	})

	cols := []parser.ColumnDef{
		{Name: "ID", Type: "bigint NOT NULL", Nullable: false},
		{Name: "state", Type: "int NULL", Nullable: true},
		{Name: "start_date", Type: "datetime NULL", Nullable: true},
	}
	// 132 nvarchar(100) like sample_main_103
	for i := 1; i <= 132; i++ {
		cols = append(cols, parser.ColumnDef{
			Name:     fmt.Sprintf("field%04d", i),
			Type:     "nvarchar(100) NULL",
			Nullable: true,
		})
	}
	// 15 nvarchar(20) like sample_main_103
	for i := 201; i <= 215; i++ {
		cols = append(cols, parser.ColumnDef{
			Name:     fmt.Sprintf("field%04d", i),
			Type:     "nvarchar(20) NULL",
			Nullable: true,
		})
	}
	// 128 numeric columns
	for i := 301; i <= 428; i++ {
		cols = append(cols, parser.ColumnDef{
			Name:     fmt.Sprintf("field%04d", i),
			Type:     "numeric(20,2) NULL",
			Nullable: true,
		})
	}
	// 98 large nvarchar (stage 1 converts to TEXT)
	for i := 501; i <= 598; i++ {
		cols = append(cols, parser.ColumnDef{
			Name:     fmt.Sprintf("field%04d", i),
			Type:     "nvarchar(1000) NULL",
			Nullable: true,
		})
	}

	tableDDL := &parser.TableDDL{
		TableName: "wide_sample",
		Columns:   cols,
		PrimaryKey: &parser.PrimaryKeyDef{
			Name:    "PK_wide",
			Columns: []string{"ID"},
		},
	}

	ddl, err := tc.ConvertToMySQL(tableDDL)
	if err != nil {
		t.Fatalf("ConvertToMySQL returned error: %v", err)
	}
	// 98 large nvarchar (stage 1 size threshold) + 132 nvarchar(100) + 15 nvarchar(20)
	// all converted to TEXT by row size guard = 245 total
	textCount := strings.Count(ddl, "` text")
	if textCount < 220 {
		t.Fatalf("DDL should convert ~245 nvarchar columns to text, got %d text columns:\n%s", textCount, ddl)
	}
	// Primary key should be preserved
	if !strings.Contains(ddl, "`ID` bigint NOT NULL") {
		t.Fatalf("DDL should keep ID bigint, got:\n%s", ddl)
	}
	if !strings.Contains(ddl, "PRIMARY KEY (`ID`)") {
		t.Fatalf("DDL should include PRIMARY KEY, got:\n%s", ddl)
	}
}

func TestConvertToMySQLResultNormalModeReportsEstimate(t *testing.T) {
	tc := NewTableConverter(config.ConverterConfig{
		MaxVarcharToTextColumns:  10,
		MaxNvarcharToTextColumns: 10,
		MaxNvarcharToTextSize:    500,
		MaxVarcharToTextSize:     500,
	})

	tableDDL := &parser.TableDDL{
		TableName: "narrow_table",
		Columns: []parser.ColumnDef{
			{Name: "ID", Type: "bigint NOT NULL", Nullable: false},
			{Name: "name", Type: "nvarchar(100) NULL", Nullable: true},
		},
		PrimaryKey: &parser.PrimaryKeyDef{Name: "PK_narrow", Columns: []string{"ID"}},
	}

	result, err := tc.ConvertToMySQLResult(tableDDL, ConvertOptions{})
	if err != nil {
		t.Fatalf("ConvertToMySQLResult returned error: %v", err)
	}
	if result.SQL == "" {
		t.Fatal("ConvertToMySQLResult returned empty SQL")
	}
	if result.Mode != ConvertModeNormal {
		t.Fatalf("Mode = %q, want %q", result.Mode, ConvertModeNormal)
	}
	if result.EstimatedRowBytes <= 0 {
		t.Fatalf("EstimatedRowBytes = %d, want > 0", result.EstimatedRowBytes)
	}
	if len(result.Degradations) != 0 {
		t.Fatalf("Degradations = %+v, want empty", result.Degradations)
	}
}

func TestConvertToMySQLCompatibilityWrapperReturnsSQL(t *testing.T) {
	tc := NewTableConverter(config.ConverterConfig{})
	tableDDL := &parser.TableDDL{
		TableName: "compat_table",
		Columns: []parser.ColumnDef{
			{Name: "ID", Type: "bigint NOT NULL", Nullable: false},
		},
	}

	ddl, err := tc.ConvertToMySQL(tableDDL)
	if err != nil {
		t.Fatalf("ConvertToMySQL returned error: %v", err)
	}
	if !strings.Contains(ddl, "CREATE TABLE `compat_table`") {
		t.Fatalf("DDL = %q, want CREATE TABLE for compat_table", ddl)
	}
}
