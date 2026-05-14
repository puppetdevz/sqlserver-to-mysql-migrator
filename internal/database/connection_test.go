package database

import (
	"testing"

	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/matcher"
)

func TestConnectionActualTableNameCaseSensitive(t *testing.T) {
	tableMatcher := matcher.NewTableNameMatcher(true)
	conn := &Connection{tableMatcher: &tableMatcher}
	conn.buildTableNameMap([]string{"sample_main_102"})

	if got := conn.GetActualTableName("SAMPLE_MAIN_102"); got != "SAMPLE_MAIN_102" {
		t.Fatalf("GetActualTableName() = %q, want original requested name", got)
	}
	if got := conn.GetActualTableName("sample_main_102"); got != "sample_main_102" {
		t.Fatalf("GetActualTableName() = %q, want actual table name", got)
	}
}

func TestConnectionActualTableNameCaseInsensitive(t *testing.T) {
	tableMatcher := matcher.NewTableNameMatcher(false)
	conn := &Connection{tableMatcher: &tableMatcher}
	conn.buildTableNameMap([]string{"sample_main_102"})

	if got := conn.GetActualTableName("SAMPLE_MAIN_102"); got != "sample_main_102" {
		t.Fatalf("GetActualTableName() = %q, want actual table name", got)
	}
}

func TestConnectionTableExistsUsesMatcher(t *testing.T) {
	tableMatcher := matcher.NewTableNameMatcher(false)
	conn := &Connection{tableMatcher: &tableMatcher}
	conn.buildTableNameMap([]string{"sample_main_102"})

	exists, err := conn.TableExists("SAMPLE_MAIN_102")
	if err != nil {
		t.Fatalf("TableExists() error = %v", err)
	}
	if !exists {
		t.Fatal("TableExists() = false, want true")
	}
}
