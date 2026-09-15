package importer

import (
	"database/sql"
	"os"
	"strings"
	"testing"

	_ "github.com/go-sql-driver/mysql"
)

// GoldenDB-specific behaviour is an explicitly gated, optional integration test.
// It never falls back to the local/production config: when
// GOLDENDB_TEST_DSN is unset the test skips, and when it is set the DSN must not
// point at localhost, 127.0.0.1 or ::1. A separate authorized isolated target is
// required. This test is intentionally excluded from the offline regression.
//
// Enable only after G1 authorization, inside the isolated environment:
//
//	GOLDENDB_TEST_DSN='user:pass@tcp(ISOLATED_HOST:3306)/ISOLATED_DB?parseTime=true' \
//	  go test ./internal/importer -run TestGoldenDBOptionalIntegration -v
func TestGoldenDBOptionalIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("GOLDENDB_TEST_DSN"))
	if dsn == "" {
		t.Skip("GOLDENDB_TEST_DSN is not set; GoldenDB integration requires a separate authorized isolated target")
	}
	lower := strings.ToLower(dsn)
	for _, forbidden := range []string{"tcp(localhost", "tcp(127.0.0.1", "tcp(::1", "tcp([::1]"} {
		if strings.Contains(lower, forbidden) {
			t.Fatalf("GOLDENDB_TEST_DSN must not fall back to a local target (%s)", forbidden)
		}
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("open isolated GoldenDB DSN: %v", err)
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		t.Fatalf("ping isolated GoldenDB: %v", err)
	}

	const table = "goldendb_optional_integration"
	if _, err := db.Exec("DROP TABLE IF EXISTS " + table); err != nil {
		t.Fatalf("drop scratch table: %v", err)
	}
	if _, err := db.Exec("CREATE TABLE " + table + " (id INT PRIMARY KEY, v TEXT)"); err != nil {
		t.Fatalf("create scratch table: %v", err)
	}
	defer db.Exec("DROP TABLE " + table)

	bi := NewBatchInserter(db, table, []string{"id", "v"}, 100, "replace")
	bi.SetMaxBatchBytes(1 << 20)
	defer bi.Close()
	if _, err := bi.InsertBatch([][]any{{1, "alpha"}, {2, strings.Repeat("中文", 100)}}); err != nil {
		t.Fatalf("prepared batch insert: %v", err)
	}
	// REPLACE semantics must remain the configured duplicate-key policy.
	if _, err := bi.InsertBatch([][]any{{1, "alpha-replaced"}}); err != nil {
		t.Fatalf("duplicate-key replace: %v", err)
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
		t.Fatalf("count back: %v", err)
	}
	if count != 2 {
		t.Fatalf("count = %d, want 2 after replace", count)
	}
	var value string
	if err := db.QueryRow("SELECT v FROM " + table + " WHERE id = 1").Scan(&value); err != nil {
		t.Fatalf("read replaced row: %v", err)
	}
	if value != "alpha-replaced" {
		t.Fatalf("value = %q, want replaced content", value)
	}
}
