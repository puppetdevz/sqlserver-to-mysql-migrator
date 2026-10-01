package source

import (
	"strings"
	"testing"
)

func TestFixtureParseErrorsNeverEchoCredentialURL(t *testing.T) {
	marker := "synthetic-fixture-value"
	dsn := "sqlserver://fixture:" + marker + "@127.0.0.1:invalid?database=pi_migration_fixture"
	err := validateSQLServerFixtureDSN(dsn)
	if err == nil {
		t.Fatal("accepted malformed fixture URL")
	}
	if strings.Contains(err.Error(), marker) || strings.Contains(err.Error(), dsn) {
		t.Fatal("fixture parse error disclosed credentials")
	}
}
