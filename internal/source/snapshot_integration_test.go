package source

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/bundle"
)

func validateSQLServerFixtureDSN(dsn string) error {
	u, e := url.Parse(dsn)
	if e != nil {
		return e
	}
	host, _, e := net.SplitHostPort(u.Host)
	if e != nil {
		return e
	}
	if u.Scheme != "sqlserver" || host != "127.0.0.1" || u.Query().Get("database") != "pi_migration_fixture" {
		return fmt.Errorf("refusing non-disposable fixture DSN")
	}
	return nil
}

func fixturePortMatches(dsn, binding string) bool {
	u, e := url.Parse(dsn)
	return e == nil && u.Host == strings.TrimSpace(binding)
}
func requireFixturePort(t *testing.T, container string, dsns ...string) {
	t.Helper()
	out, e := exec.Command("docker", "port", container, "1433/tcp").Output()
	if e != nil {
		t.Fatal(e)
	}
	for _, dsn := range dsns {
		if !fixturePortMatches(dsn, string(out)) {
			t.Fatal("DSN does not match disposable container port")
		}
	}
}

// TestSQLServerOptionalSnapshotConsistency uses only a caller-provided disposable
// Docker SQL Server fixture. No DSN fallback or fixed business table is allowed.
func TestSQLServerOptionalSnapshotConsistency(t *testing.T) {
	adminURL, readURL, container := os.Getenv("SQLSERVER_FIXTURE_ADMIN_DSN"), os.Getenv("SQLSERVER_FIXTURE_READ_DSN"), os.Getenv("SQLSERVER_FIXTURE_DOCKER_ID")
	if adminURL == "" || readURL == "" || container == "" {
		t.Skip("requires disposable Docker SQL Server fixture and explicit DSNs")
	}
	inspect := exec.Command("docker", "inspect", "--format", "{{.State.Running}}", container)
	out, e := inspect.Output()
	if e != nil || strings.TrimSpace(string(out)) != "true" {
		t.Fatal("fixture container not running")
	}
	for _, dsn := range []string{adminURL, readURL} {
		if e := validateSQLServerFixtureDSN(dsn); e != nil {
			t.Fatal(e)
		}
	}
	requireFixturePort(t, container, adminURL, readURL)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	admin, e := sql.Open("sqlserver", adminURL)
	if e != nil {
		t.Fatal(e)
	}
	defer admin.Close()
	read, e := sql.Open("sqlserver", readURL)
	if e != nil {
		t.Fatal(e)
	}
	defer read.Close()
	suffix := strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	a, b := "SnapshotA_"+suffix, "SnapshotB_"+suffix
	for _, q := range []string{fmt.Sprintf("CREATE TABLE dbo.[%s] (id int NOT NULL PRIMARY KEY, value int NOT NULL)", a), fmt.Sprintf("CREATE TABLE dbo.[%s] (id int NOT NULL PRIMARY KEY, value int NOT NULL)", b), fmt.Sprintf("INSERT INTO dbo.[%s] VALUES (1,1)", a), fmt.Sprintf("INSERT INTO dbo.[%s] VALUES (1,1)", b), fmt.Sprintf("GRANT SELECT ON dbo.[%s] TO pi_read", a), fmt.Sprintf("GRANT SELECT ON dbo.[%s] TO pi_read", b)} {
		if _, e = admin.ExecContext(ctx, q); e != nil {
			t.Fatal(e)
		}
	}
	dir := filepath.Join(t.TempDir(), "bundle")
	changed := false
	e = exportWithObserver(ctx, read, []string{"dbo." + a, "dbo." + b}, dir, 1<<20, func(i int) error {
		if i != 0 {
			return nil
		}
		_, e := admin.ExecContext(ctx, fmt.Sprintf("UPDATE dbo.[%s] SET value=2 WHERE id=1", b))
		changed = e == nil
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	if !changed {
		t.Fatal("writer never committed")
	}
	m, e := bundle.Verify(dir)
	if e != nil {
		t.Fatal(e)
	}
	f, e := os.Open(filepath.Join(dir, m.Tables[1].File))
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	var got string
	e = bundle.Stream(f, m.Tables[1], func(r bundle.Row) error { got = r[1].Text; return nil })
	if e != nil {
		t.Fatal(e)
	}
	if got != "1" {
		t.Fatalf("cross-table mixed snapshots: B=%s", got)
	}
	var now int
	if e = admin.QueryRowContext(ctx, fmt.Sprintf("SELECT value FROM dbo.[%s] WHERE id=1", b)).Scan(&now); e != nil || now != 2 {
		t.Fatalf("concurrent writer not visible outside snapshot: %d %v", now, e)
	}
}

func TestSQLServerOptionalValueRoundTrip(t *testing.T) {
	adminURL, readURL, container := os.Getenv("SQLSERVER_FIXTURE_ADMIN_DSN"), os.Getenv("SQLSERVER_FIXTURE_READ_DSN"), os.Getenv("SQLSERVER_FIXTURE_DOCKER_ID")
	if adminURL == "" || readURL == "" || container == "" {
		t.Skip("requires disposable Docker SQL Server fixture and explicit DSNs")
	}
	for _, dsn := range []string{adminURL, readURL} {
		if e := validateSQLServerFixtureDSN(dsn); e != nil {
			t.Fatal(e)
		}
	}
	out, e := exec.Command("docker", "inspect", "--format", "{{.State.Running}}", container).Output()
	if e != nil || strings.TrimSpace(string(out)) != "true" {
		t.Fatal("fixture container not running")
	}
	requireFixturePort(t, container, adminURL, readURL)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	admin, e := sql.Open("sqlserver", adminURL)
	if e != nil {
		t.Fatal(e)
	}
	defer admin.Close()
	read, e := sql.Open("sqlserver", readURL)
	if e != nil {
		t.Fatal(e)
	}
	defer read.Close()
	name := "Values_" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	for _, q := range []string{
		fmt.Sprintf("CREATE TABLE dbo.[%s] (id int NOT NULL PRIMARY KEY, txt nvarchar(20) NULL, b varbinary(10) NULL, amount decimal(8,2) NULL, moment datetime2(6) NULL)", name),
		fmt.Sprintf("INSERT INTO dbo.[%s](id,txt,b,amount,moment) VALUES (1,N'',0x000102,12.30,'2025-01-02T03:04:05.123456'),(2,N'  ',0x00,NULL,NULL),(3,NULL,NULL,NULL,NULL)", name),
		fmt.Sprintf("GRANT SELECT ON dbo.[%s] TO pi_read", name),
	} {
		if _, e = admin.ExecContext(ctx, q); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = read.ExecContext(ctx, fmt.Sprintf("INSERT INTO dbo.[%s](id,txt) VALUES (99,N'nope')", name)); e == nil {
		t.Fatal("read account wrote source")
	}
	dir := filepath.Join(t.TempDir(), "values")
	if e = Export(ctx, read, []string{"dbo." + name}, dir); e != nil {
		t.Fatal(e)
	}
	m, e := bundle.Verify(dir)
	if e != nil {
		t.Fatal(e)
	}
	f, e := os.Open(filepath.Join(dir, m.Tables[0].File))
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	var rows []bundle.Row
	if e = bundle.Stream(f, m.Tables[0], func(r bundle.Row) error { rows = append(rows, r); return nil }); e != nil {
		t.Fatal(e)
	}
	if len(rows) != 3 || rows[0][1] == nil || rows[0][1].Text != "" || rows[1][1].Text != "  " || rows[2][1] != nil {
		t.Fatalf("null/empty/space lost: %+v", rows)
	}
	if rows[0][2] == nil || rows[0][2].Text != "AAEC" || rows[1][2].Text != "AA==" || rows[2][2] != nil {
		t.Fatalf("binary lost: %+v", rows)
	}
	if rows[0][3].Text != "12.30" || rows[0][4].Text != "2025-01-02T03:04:05.123456" {
		t.Fatalf("decimal/datetime lost: %+v", rows[0])
	}
}
