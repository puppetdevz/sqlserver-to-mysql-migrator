package parser

import "testing"

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
