package target

import (
	"context"
	"database/sql"
	"encoding/base64"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/bundle"
)

func fixtureMatchesPort(addr, binding string) bool { return addr == strings.TrimSpace(binding) }

// Optional disposable Docker MySQL protocol smoke test; NOT GoldenDB G1.
func TestMySQLDockerOptionalBundleRoundTrip(t *testing.T) {
	dsn, id := os.Getenv("MYSQL_BUNDLE_FIXTURE_DSN"), os.Getenv("MYSQL_BUNDLE_FIXTURE_DOCKER_ID")
	if dsn == "" || id == "" {
		t.Skip("explicit disposable Docker MySQL fixture only")
	}
	cfg, e := mysql.ParseDSN(dsn)
	if e != nil || cfg.DBName != "pi_bundle_fixture" || cfg.Net != "tcp" {
		t.Fatal("refusing non-fixture DSN")
	}
	host, _, e := net.SplitHostPort(cfg.Addr)
	if e != nil || host != "127.0.0.1" {
		t.Fatal("fixture must bind loopback")
	}
	out, e := exec.Command("docker", "inspect", "--format", "{{.State.Running}}", id).Output()
	if e != nil || strings.TrimSpace(string(out)) != "true" {
		t.Fatal("disposable fixture not running")
	}
	portBinding, e := exec.Command("docker", "port", id, "3306/tcp").Output()
	if e != nil || !fixtureMatchesPort(cfg.Addr, string(portBinding)) {
		t.Fatal("DSN does not match disposable container port")
	}
	db, e := sql.Open("mysql", dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	dir := filepath.Join(t.TempDir(), "b")
	m := bundle.Manifest{Version: 2, RunID: uuid.NewString(), Source: "fixture/db", Snapshot: "SNAPSHOT", TransactionID: 17, Started: "2026-01-01T00:00:00Z", Ended: "2026-01-01T00:00:01Z", Tables: []bundle.Table{{Schema: "dbo", Name: "fixture_" + strings.ReplaceAll(uuid.NewString()[:8], "-", ""), File: "000001.rows", Columns: []bundle.Column{{Name: "id", Type: "int"}, {Name: "txt", Type: "nvarchar(20)", Nullable: true}, {Name: "bin", Type: "varbinary(10)", Nullable: true}, {Name: "amount", Type: "decimal(8,2)", Nullable: true}, {Name: "stamp", Type: "datetime2(6)", Nullable: true}}}}}
	w, e := bundle.Begin(dir, m)
	if e != nil {
		t.Fatal(e)
	}
	for _, r := range []bundle.Row{{{Text: "1"}, {Text: ""}, {Text: base64.StdEncoding.EncodeToString([]byte{0, 1, 2})}, {Text: "12.30"}, {Text: "2025-01-02T03:04:05.123456"}}, {{Text: "2"}, {Text: "  "}, {Text: ""}, nil, nil}, {{Text: "3"}, nil, nil, nil, nil}} {
		if e = w.Row(0, r); e != nil {
			t.Fatal(e)
		}
	}
	if e = w.Seal(); e != nil {
		t.Fatal(e)
	}
	if e = Import(ctx, db, dir, "pi_bundle_fixture", false); e != nil {
		t.Fatal(e)
	}
	var count int
	if e = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM `"+stage(m.RunID, 0)+"`").Scan(&count); e != nil || count != 3 {
		t.Fatalf("staging count=%d err=%v", count, e)
	}
	// Disposable fixture only: stage-scoped backup, corruption, restore and digest recheck.
	name := stage(m.RunID, 0)
	backup := name + "_backup"
	for _, q := range []string{fmt.Sprintf("CREATE TABLE `%s` LIKE `%s`", backup, name), fmt.Sprintf("INSERT INTO `%s` SELECT * FROM `%s`", backup, name), fmt.Sprintf("DELETE FROM `%s` WHERE id=1", name), fmt.Sprintf("INSERT INTO `%s` SELECT * FROM `%s` WHERE id=1", name, backup)} {
		if _, e = db.ExecContext(ctx, q); e != nil {
			t.Fatal(e)
		}
	}
	conn, e := db.Conn(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	actual, blocks, e := readBack(ctx, conn, name, w.M.Tables[0])
	if e != nil {
		t.Fatal(e)
	}
	if actual != w.M.Tables[0].Data || !bundle.EqualBlocks(blocks, w.M.Tables[0].Blocks) {
		t.Fatal("backup/restore digest mismatch")
	}
}
