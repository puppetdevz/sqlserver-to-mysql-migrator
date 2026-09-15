package converter

import (
	"strings"
	"testing"

	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/config"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/parser"
)

func TestConvertToMySQLQuotesBacktickInIdentifiers(t *testing.T) {
	tc := NewTableConverter(config.ConverterConfig{
		MaxVarcharToTextColumns:  10,
		MaxNvarcharToTextColumns: 10,
		MaxNvarcharToTextSize:    500,
		MaxVarcharToTextSize:     500,
	})
	tableDDL := &parser.TableDDL{
		TableName: "a`b",
		Columns: []parser.ColumnDef{
			{Name: "c`d", Type: "int", Nullable: false},
			{Name: "name", Type: "varchar(10)", Nullable: true},
		},
		PrimaryKey: &parser.PrimaryKeyDef{Name: "pk`x", Columns: []string{"c`d"}},
		Indexes: []parser.IndexDef{
			{Name: "idx`y", Columns: []string{"name"}, Unique: false},
		},
	}
	ddl, err := tc.ConvertToMySQL(tableDDL)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"CREATE TABLE `a``b`",
		"`c``d` int NOT NULL",
		"PRIMARY KEY (`c``d`)",
		"CREATE INDEX `idx``y` ON `a``b` (`name`)",
	} {
		if !strings.Contains(ddl, want) {
			t.Fatalf("DDL missing %q:\n%s", want, ddl)
		}
	}
	if strings.Contains(ddl, "CREATE TABLE `a`b`") {
		t.Fatalf("unescaped table ident in DDL:\n%s", ddl)
	}
}

func TestConvertResultStatementsDoNotSplitOnSemicolonInDefault(t *testing.T) {
	tc := NewTableConverter(config.ConverterConfig{
		MaxVarcharToTextColumns:  10,
		MaxNvarcharToTextColumns: 10,
		MaxNvarcharToTextSize:    500,
		MaxVarcharToTextSize:     500,
	})
	tableDDL := &parser.TableDDL{
		TableName: "t",
		Columns: []parser.ColumnDef{
			{Name: "note", Type: "varchar(20)", Nullable: true},
		},
	}
	result, err := tc.ConvertToMySQLResult(tableDDL, ConvertOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Statements) != 1 {
		t.Fatalf("statements=%d want 1: %#v", len(result.Statements), result.Statements)
	}
	if strings.Contains(result.Statements[0], ";") {
		t.Fatalf("CREATE statement should not rely on semicolon split: %q", result.Statements[0])
	}
}
