package parser

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseColumnUnquotesBracketedIdentifier(t *testing.T) {
	parser := NewDDLParser("")

	column := parser.parseColumn("[TYPE] smallint NULL,")
	if column == nil {
		t.Fatal("parseColumn() returned nil")
	}

	if column.Name != "TYPE" {
		t.Fatalf("column.Name = %q, want TYPE", column.Name)
	}
	if column.Type != "smallint NULL" {
		t.Fatalf("column.Type = %q, want smallint NULL", column.Type)
	}
}

func TestParsePrimaryKeyUnquotesBracketedIdentifiers(t *testing.T) {
	parser := NewDDLParser("")

	primaryKey := parser.parsePrimaryKey("CONSTRAINT PK_TEST PRIMARY KEY ([ID], [TYPE])")
	if primaryKey == nil {
		t.Fatal("parsePrimaryKey() returned nil")
	}

	want := []string{"ID", "TYPE"}
	if len(primaryKey.Columns) != len(want) {
		t.Fatalf("primaryKey.Columns = %v, want %v", primaryKey.Columns, want)
	}
	for i := range want {
		if primaryKey.Columns[i] != want[i] {
			t.Fatalf("primaryKey.Columns = %v, want %v", primaryKey.Columns, want)
		}
	}
}

func TestParseIndexUnquotesBracketedIdentifiers(t *testing.T) {
	parser := NewDDLParser("")

	index := parser.parseIndex("CREATE NONCLUSTERED INDEX IDX_TEST ON V80.dbo.TEST ( [SIGN_TIME] ASC, [TYPE] DESC )")
	if index == nil {
		t.Fatal("parseIndex() returned nil")
	}

	want := []string{"SIGN_TIME", "TYPE"}
	if len(index.Columns) != len(want) {
		t.Fatalf("index.Columns = %v, want %v", index.Columns, want)
	}
	for i := range want {
		if index.Columns[i] != want[i] {
			t.Fatalf("index.Columns = %v, want %v", index.Columns, want)
		}
	}
}

func TestParseIndexPreservesColumnNamesContainingSortDirectionText(t *testing.T) {
	parser := NewDDLParser("")

	index := parser.parseIndex("CREATE NONCLUSTERED INDEX IDX_TEST ON V80.dbo.TEST ( CENSOR_DESC ASC, BASIC_ASC_FIELD DESC )")
	if index == nil {
		t.Fatal("parseIndex() returned nil")
	}

	want := []string{"CENSOR_DESC", "BASIC_ASC_FIELD"}
	if len(index.Columns) != len(want) {
		t.Fatalf("index.Columns = %v, want %v", index.Columns, want)
	}
	for i := range want {
		if index.Columns[i] != want[i] {
			t.Fatalf("index.Columns = %v, want %v", index.Columns, want)
		}
	}
}

func writeParserDDL(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ddl.sql")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

func TestParseAllPreservesOriginalCaseDistinctTables(t *testing.T) {
	content := `-- V80.dbo.Foo definition
CREATE TABLE V80.dbo.Foo (
id int NULL
);
-- V80.dbo.foo definition
CREATE TABLE V80.dbo.foo (
name varchar(10) NULL
);
`
	tables, err := NewDDLParser(writeParserDDL(t, content)).ParseAll()
	if err != nil {
		t.Fatalf("ParseAll() error = %v", err)
	}
	if len(tables) != 2 {
		t.Fatalf("len(tables) = %d, want 2", len(tables))
	}
	foo := tables["Foo"]
	lower := tables["foo"]
	if foo == nil || lower == nil {
		t.Fatalf("tables = %v, want Foo and foo", tables)
	}
	if foo.TableName != "Foo" || lower.TableName != "foo" {
		t.Fatalf("TableName Foo=%q foo=%q", foo.TableName, lower.TableName)
	}
	if len(foo.Columns) != 1 || foo.Columns[0].Name != "id" {
		t.Fatalf("Foo columns = %+v, want id", foo.Columns)
	}
	if len(lower.Columns) != 1 || lower.Columns[0].Name != "name" {
		t.Fatalf("foo columns = %+v, want name", lower.Columns)
	}
}

func TestParseAllDuplicateOriginalNameReturnsError(t *testing.T) {
	content := `-- V80.dbo.Foo definition
CREATE TABLE V80.dbo.Foo (
id int NULL
);
-- V80.dbo.Foo definition
CREATE TABLE V80.dbo.Foo (
name varchar(10) NULL
);
`
	_, err := NewDDLParser(writeParserDDL(t, content)).ParseAll()
	if err == nil {
		t.Fatal("ParseAll() error = nil, want duplicate definition error")
	}
	if !strings.Contains(err.Error(), "Foo") {
		t.Fatalf("error = %v, want Foo", err)
	}
}

func TestParseTableExactMatch(t *testing.T) {
	content := `-- V80.dbo.Foo definition
CREATE TABLE V80.dbo.Foo (
id int NULL
);
`
	table, err := NewDDLParser(writeParserDDL(t, content)).ParseTable("Foo")
	if err != nil {
		t.Fatalf("ParseTable(Foo) error = %v", err)
	}
	if table.TableName != "Foo" {
		t.Fatalf("TableName = %q, want Foo", table.TableName)
	}
}

func TestParseTableUniqueCaseInsensitiveFallback(t *testing.T) {
	content := `-- V80.dbo.Foo definition
CREATE TABLE V80.dbo.Foo (
id int NULL
);
`
	table, err := NewDDLParser(writeParserDDL(t, content)).ParseTable("FOO")
	if err != nil {
		t.Fatalf("ParseTable(FOO) error = %v", err)
	}
	if table.TableName != "Foo" {
		t.Fatalf("TableName = %q, want Foo", table.TableName)
	}
}

func TestParseTableAmbiguousCaseInsensitive(t *testing.T) {
	content := `-- V80.dbo.Foo definition
CREATE TABLE V80.dbo.Foo (
id int NULL
);
-- V80.dbo.foo definition
CREATE TABLE V80.dbo.foo (
name varchar(10) NULL
);
`
	_, err := NewDDLParser(writeParserDDL(t, content)).ParseTable("FOO")
	if err == nil {
		t.Fatal("ParseTable(FOO) error = nil, want ambiguous")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "ambiguous") {
		t.Fatalf("error = %v, want ambiguous", err)
	}
}

func TestParseTableNotFound(t *testing.T) {
	content := `-- V80.dbo.Foo definition
CREATE TABLE V80.dbo.Foo (
id int NULL
);
`
	_, err := NewDDLParser(writeParserDDL(t, content)).ParseTable("Bar")
	if err == nil {
		t.Fatal("ParseTable(Bar) error = nil, want not found")
	}
}

func TestParseAllReturnsErrorWhenOneTableBlockFails(t *testing.T) {
	content := `-- V80.dbo.Good definition
CREATE TABLE V80.dbo.Good (
id int NULL
);
-- V80.dbo.Bad definition
-- no create table statement
`
	_, err := NewDDLParser(writeParserDDL(t, content)).ParseAll()
	if err == nil {
		t.Fatal("ParseAll() error = nil, want parse failure")
	}
	if !strings.Contains(err.Error(), "Bad") && !strings.Contains(err.Error(), "CREATE TABLE") {
		t.Fatalf("error = %v, want Bad/CREATE TABLE clue", err)
	}
}

func TestParseAllTwoValidTablesStillSucceeds(t *testing.T) {
	content := `-- V80.dbo.A definition
CREATE TABLE V80.dbo.A (
id int NULL
);
-- V80.dbo.B definition
CREATE TABLE V80.dbo.B (
name varchar(10) NULL
);
`
	tables, err := NewDDLParser(writeParserDDL(t, content)).ParseAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(tables) != 2 {
		t.Fatalf("len=%d", len(tables))
	}
}
