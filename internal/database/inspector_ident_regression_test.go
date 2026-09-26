package database

import (
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/matcher"
)

func TestInspectorDescribeQuotesBackticksInTableName(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("DESCRIBE `a``b`").WillReturnRows(sqlmock.NewRows([]string{"Field", "Type", "Null", "Key", "Default", "Extra"}).AddRow("id", "int", "YES", "", nil, ""))
	inspector := NewInspector(&Connection{DB: db}, matcher.DefaultTableNameMatcher())
	if _, err := inspector.GetTableStructure("a`b"); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
