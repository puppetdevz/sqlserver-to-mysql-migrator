//go:build integration

package importer

import (
	"database/sql"
	"os"
	"strings"
	"testing"

	_ "github.com/go-sql-driver/mysql"
)

// GoldenDB requires a separately authorized isolated fixture, verified TLS,
// explicit environment opt-in and the integration build tag. It never reads
// local configuration. This smoke test is not production compatibility proof.
func TestGoldenDBOptionalIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("GOLDENDB_TEST_DSN"))
	if dsn == "" || os.Getenv("GOLDENDB_TEST_ISOLATED") != "1" {
		t.Skip("GOLDENDB_TEST_DSN is not set; GoldenDB integration requires a separate authorized isolated target")
	}
	if err := validateGoldenDBFixtureDSN(dsn); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal("cannot open isolated GoldenDB connection")
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		t.Fatal("cannot ping isolated GoldenDB fixture")
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
