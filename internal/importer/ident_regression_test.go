package importer

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/database"
)

func TestDescribeQuotesBackticksInTableName(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("DESCRIBE `a``b`").WillReturnRows(sqlmock.NewRows([]string{"Field", "Type", "Null", "Key", "Default", "Extra"}).AddRow("id", "int", "YES", "", nil, ""))
	importer := &TableImporter{conn: &database.Connection{DB: db}}
	if _, err := importer.getDBColumnInfos(context.Background(), "a`b"); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
