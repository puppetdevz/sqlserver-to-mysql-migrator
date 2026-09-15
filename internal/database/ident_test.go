package database

import (
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/matcher"
)

func TestTruncateAndCountQuoteBacktickIdentifiers(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	m := matcher.NewTableNameMatcher(true)
	conn := &Connection{DB: db, tableMatcher: &m}
	if err := conn.buildTableNameMap([]string{"a`b"}); err != nil {
		t.Fatal(err)
	}
	mock.ExpectExec("TRUNCATE TABLE `a``b`").WillReturnResult(sqlmock.NewResult(0, 0))
	if err := conn.TruncateTable("a`b"); err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery("SELECT COUNT(*) FROM `a``b`").WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(int64(1)))
	n, err := conn.GetRowCount("a`b")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("count=%d", n)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestBuildTableNameMapCaseInsensitiveConflictFails(t *testing.T) {
	m := matcher.NewTableNameMatcher(false)
	conn := &Connection{tableMatcher: &m}
	err := conn.buildTableNameMap([]string{"Foo", "foo"})
	if err == nil {
		t.Fatal("expected table name key conflict")
	}
	if conn.tableNameMap != nil {
		t.Fatalf("conflict must not publish a partial map: %+v", conn.tableNameMap)
	}
}

func TestBuildTableNameMapCaseSensitiveDistinctTables(t *testing.T) {
	m := matcher.NewTableNameMatcher(true)
	conn := &Connection{tableMatcher: &m}
	if err := conn.buildTableNameMap([]string{"Foo", "foo"}); err != nil {
		t.Fatal(err)
	}
	if got := conn.GetActualTableName("Foo"); got != "Foo" {
		t.Fatalf("Foo -> %q", got)
	}
	if got := conn.GetActualTableName("foo"); got != "foo" {
		t.Fatalf("foo -> %q", got)
	}
}
