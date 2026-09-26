// Package target imports sealed bundles into new, run-scoped staging tables.
// No existing user table is ever dropped, truncated or modified by this mode.
package target

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/go-sql-driver/mysql"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/bundle"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/matcher"
)

func stage(run string, i int) string {
	h := sha256.Sum256([]byte(run))
	return fmt.Sprintf("_migration_%x_%03d", h[:8], i)
}
func sqlType(s string) (string, error) {
	switch s {
	case "bit":
		return "TINYINT(1)", nil
	case "tinyint":
		return "TINYINT UNSIGNED", nil
	case "smallint":
		return "SMALLINT", nil
	case "int":
		return "INT", nil
	case "bigint":
		return "BIGINT", nil
	case "date":
		return "DATE", nil
	case "datetime2(6)":
		return "DATETIME(6)", nil
	case "nvarchar(max)":
		return "LONGTEXT", nil
	case "varbinary(max)":
		return "LONGBLOB", nil
	}
	for _, p := range []string{"nvarchar(", "varbinary(", "decimal("} {
		if strings.HasPrefix(s, p) && bundle.Supported(s) {
			if p == "varbinary(" {
				return "VARBINARY" + s[len("varbinary"):], nil
			}
			if p == "decimal(" {
				return strings.ToUpper(s), nil
			}
			return "VARCHAR" + s[strings.Index(s, "("):], nil
		}
	}
	return "", fmt.Errorf("unsupported type %s", s)
}
func createSQL(name string, t bundle.Table) (string, error) {
	for _, idx := range t.Indexes {
		for _, key := range idx.Columns {
			for _, c := range t.Columns {
				if !strings.EqualFold(c.Name, key) {
					continue
				}
				if strings.HasPrefix(c.Type, "nvarchar(") {
					return "", fmt.Errorf("index %s on text needs approved collation mapping", idx.Name)
				}
				if strings.HasSuffix(c.Type, "(max)") {
					return "", fmt.Errorf("index %s on unbounded column must be explicitly mapped", idx.Name)
				}
				if x := strings.IndexByte(c.Type, '('); x >= 0 && c.Type != "datetime2(6)" && !strings.HasPrefix(c.Type, "decimal(") {
					var n int
					_, _ = fmt.Sscanf(c.Type[x:], "(%d)", &n)
					if strings.HasPrefix(c.Type, "varbinary(") && n > 768 || n > 191 && !strings.HasPrefix(c.Type, "varbinary(") {
						return "", fmt.Errorf("index %s exceeds conservative key budget", idx.Name)
					}
				}
			}
		}
	}
	parts := make([]string, 0, len(t.Columns)+len(t.Indexes))
	for _, c := range t.Columns {
		typ, e := sqlType(c.Type)
		if e != nil {
			return "", e
		}
		v := matcher.QuoteIdent(c.Name) + " " + typ
		if !c.Nullable {
			v += " NOT NULL"
		}
		parts = append(parts, v)
	}
	for _, idx := range t.Indexes {
		cols := make([]string, len(idx.Columns))
		for i, c := range idx.Columns {
			cols[i] = matcher.QuoteIdent(c)
		}
		kind := "KEY " + matcher.QuoteIdent(idx.Name)
		if idx.Primary {
			kind = "PRIMARY KEY"
		} else if idx.Unique {
			kind = "UNIQUE KEY " + matcher.QuoteIdent(idx.Name)
		}
		parts = append(parts, kind+" ("+strings.Join(cols, ",")+")")
	}
	return "CREATE TABLE " + matcher.QuoteIdent(name) + " (" + strings.Join(parts, ",") + ") ENGINE=InnoDB DEFAULT CHARSET=utf8mb4", nil
}

// verifySchema refuses silent type, nullability, column order or index changes after DDL.
func verifySchema(ctx context.Context, c *sql.Conn, name string, t bundle.Table) error {
	rows, e := c.QueryContext(ctx, `SELECT column_name,data_type,is_nullable,ordinal_position,character_maximum_length,numeric_precision,numeric_scale,datetime_precision,column_type FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name=? ORDER BY ordinal_position`, name)
	if e != nil {
		return e
	}
	i := 0
	for rows.Next() {
		var col, data, nullable, columnType string
		var ordinal int
		var length, precision, scale, dt sql.NullInt64
		if e = rows.Scan(&col, &data, &nullable, &ordinal, &length, &precision, &scale, &dt, &columnType); e != nil {
			break
		}
		if i >= len(t.Columns) {
			e = errors.New("extra target column")
			break
		}
		want := t.Columns[i]
		sqltype, _ := sqlType(want.Type)
		expected := strings.ToLower(strings.Split(sqltype, "(")[0])
		expected = strings.Fields(expected)[0]
		if col != want.Name || ordinal != i+1 || data != expected || (nullable == "YES") != want.Nullable {
			e = fmt.Errorf("target column mismatch %s", col)
			break
		}
		switch {
		case strings.HasPrefix(want.Type, "nvarchar(") && !strings.HasSuffix(want.Type, "max)"):
			var n int
			_, _ = fmt.Sscanf(want.Type, "nvarchar(%d)", &n)
			if !length.Valid || length.Int64 != int64(n) {
				e = fmt.Errorf("target text width mismatch %s", col)
			}
		case strings.HasPrefix(want.Type, "varbinary(") && !strings.HasSuffix(want.Type, "max)"):
			var n int
			_, _ = fmt.Sscanf(want.Type, "varbinary(%d)", &n)
			if !length.Valid || length.Int64 != int64(n) {
				e = fmt.Errorf("target binary width mismatch %s", col)
			}
		case strings.HasPrefix(want.Type, "decimal("):
			var p, s int
			_, _ = fmt.Sscanf(want.Type, "decimal(%d,%d)", &p, &s)
			if !precision.Valid || !scale.Valid || precision.Int64 != int64(p) || scale.Int64 != int64(s) {
				e = fmt.Errorf("target decimal precision mismatch %s", col)
			}
		case want.Type == "datetime2(6)":
			if !dt.Valid || dt.Int64 != 6 {
				e = fmt.Errorf("target datetime precision mismatch %s", col)
			}
		case want.Type == "tinyint":
			if !strings.Contains(columnType, "unsigned") {
				e = fmt.Errorf("target tinyint sign mismatch %s", col)
			}
		}
		if e != nil {
			break
		}
		i++
	}
	if e == nil {
		e = rows.Err()
	}
	_ = rows.Close()
	if e != nil {
		return e
	}
	if i != len(t.Columns) {
		return errors.New("target missing column")
	}
	ixrows, e := c.QueryContext(ctx, `SELECT index_name,non_unique,column_name,seq_in_index FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name=? ORDER BY index_name,seq_in_index`, name)
	if e != nil {
		return e
	}
	actual := map[string][]string{}
	nonunique := map[string]int{}
	for ixrows.Next() {
		var n, col string
		var notUnique, seq int
		if e = ixrows.Scan(&n, &notUnique, &col, &seq); e != nil {
			break
		}
		if seq != len(actual[n])+1 {
			e = errors.New("target index order mismatch")
			break
		}
		actual[n] = append(actual[n], col)
		nonunique[n] = notUnique
	}
	if e == nil {
		e = ixrows.Err()
	}
	_ = ixrows.Close()
	if e != nil {
		return e
	}
	if len(actual) != len(t.Indexes) {
		return errors.New("target indexes missing/extra")
	}
	for _, idx := range t.Indexes {
		n := idx.Name
		if idx.Primary {
			n = "PRIMARY"
		}
		cols, ok := actual[n]
		if !ok || len(cols) != len(idx.Columns) || nonunique[n] != boolToInt(!idx.Unique) {
			return fmt.Errorf("target index mismatch %s", n)
		}
		for i, c := range cols {
			if c != idx.Columns[i] {
				return fmt.Errorf("target index order mismatch %s", n)
			}
		}
	}
	return nil
}
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
func warnings(ctx context.Context, c *sql.Conn) error {
	rows, e := c.QueryContext(ctx, "SHOW WARNINGS")
	if e != nil {
		return e
	}
	defer rows.Close()
	if rows.Next() {
		return errors.New("target emitted SQL warning; staging table must be inspected manually")
	}
	return rows.Err()
}
func values(row bundle.Row, t bundle.Table) ([]any, error) {
	v := make([]any, len(row))
	for i, c := range row {
		if c == nil {
			continue
		}
		if strings.HasPrefix(t.Columns[i].Type, "varbinary(") {
			b, e := base64.StdEncoding.Strict().DecodeString(c.Text)
			if e != nil {
				return nil, e
			}
			v[i] = b
		} else if t.Columns[i].Type == "datetime2(6)" {
			v[i] = strings.Replace(c.Text, "T", " ", 1)
		} else {
			v[i] = c.Text
		}
	}
	return v, nil
}
func statement(name string, t bundle.Table) string {
	cols := make([]string, len(t.Columns))
	marks := make([]string, len(cols))
	for i, c := range t.Columns {
		cols[i] = matcher.QuoteIdent(c.Name)
		marks[i] = "?"
	}
	return "INSERT INTO " + matcher.QuoteIdent(name) + " (" + strings.Join(cols, ",") + ") VALUES (" + strings.Join(marks, ",") + ")"
}
func selectSQL(name string, t bundle.Table) string {
	cols := make([]string, len(t.Columns))
	for i, c := range t.Columns {
		q := matcher.QuoteIdent(c.Name)
		if c.Type == "datetime2(6)" {
			q = "DATE_FORMAT(" + q + ",'%Y-%m-%dT%H:%i:%s.%f')"
		}
		cols[i] = q
	}
	return "SELECT " + strings.Join(cols, ",") + " FROM " + matcher.QuoteIdent(name)
}
func readBack(ctx context.Context, c *sql.Conn, name string, t bundle.Table) (bundle.Digest, []bundle.Digest, error) {
	var d bundle.Digest
	var blocks []bundle.Digest
	rows, e := c.QueryContext(ctx, selectSQL(name, t))
	if e != nil {
		return d, blocks, e
	}
	defer rows.Close()
	for rows.Next() {
		raw := make([]sql.RawBytes, len(t.Columns))
		dest := make([]any, len(raw))
		for i := range dest {
			dest[i] = &raw[i]
		}
		if e = rows.Scan(dest...); e != nil {
			return d, blocks, e
		}
		r := make(bundle.Row, len(raw))
		for i, b := range raw {
			if b == nil {
				continue
			}
			s := string(b)
			if strings.HasPrefix(t.Columns[i].Type, "varbinary(") {
				s = base64.StdEncoding.EncodeToString(b)
			}
			if !utf8.ValidString(s) {
				return d, blocks, errors.New("invalid target text encoding")
			}
			if !strings.HasPrefix(t.Columns[i].Type, "varbinary(") {
				s, e = bundle.Canonical(t.Columns[i].Type, s)
				if e != nil {
					return d, blocks, e
				}
			}
			r[i] = &bundle.Cell{Text: s}
		}
		if e = bundle.CheckRow(r, t); e != nil {
			return d, blocks, e
		}
		t.AddRow(&d, &blocks, r)
	}
	return d, blocks, rows.Err()
}

// Import fully verifies the bundle and table scope before any DDL. Dry run opens no DB connection.
// A failed write never retries an uncertain commit and never marks a table published.
func Import(ctx context.Context, db *sql.DB, dir, database string, dry bool) error {
	return ImportAuthorized(ctx, db, dir, database, dry, "", "")
}

// ImportAuthorized binds authorization to the re-verified manifest and target endpoint.
func ImportAuthorized(ctx context.Context, db *sql.DB, dir, database string, dry bool, identity, confirmation string) error {
	return importRun(ctx, db, dir, database, dry, identity, confirmation, nil)
}

type Report struct {
	RunID         string   `json:"run_id"`
	Plan          string   `json:"plan"`
	Source        string   `json:"source"`
	TransactionID int64    `json:"transaction_id"`
	Target        string   `json:"target"`
	Snapshot      string   `json:"snapshot"`
	Selected      []string `json:"selected"`
	Excluded      []string `json:"excluded"`
	Planned       []string `json:"planned_stages"`
	Staged        []string `json:"verified_stages"`
	Failed        []string `json:"failed"`
	State         string   `json:"state"`
	Published     bool     `json:"published"`
}

// ImportReported writes a durable, run-bound staging journal. Failure leaves stages unpublished.
func ImportReported(ctx context.Context, db *sql.DB, dir, database, identity, confirmation, reportPath string) error {
	if reportPath == "" || identity == "" || confirmation == "" {
		return errors.New("report and bound authorization required")
	}
	root, e := filepath.Abs(dir)
	if e != nil {
		return e
	}
	path, e := filepath.Abs(reportPath)
	if e != nil {
		return e
	}
	if path == root || strings.HasPrefix(path, root+string(os.PathSeparator)) {
		return errors.New("report must be outside sealed bundle")
	}
	if _, e = os.Lstat(path); e == nil {
		return errors.New("report already exists")
	}
	if !os.IsNotExist(e) {
		return e
	}
	return importRun(ctx, db, dir, database, false, identity, confirmation, func(r Report) error { return saveReport(reportPath, r) })
}
func saveReport(path string, r Report) error {
	b, e := json.MarshalIndent(r, "", "  ")
	if e != nil {
		return e
	}
	tmp := path + ".tmp"
	f, e := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return e
	}
	if _, e = f.Write(b); e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e != nil {
		return e
	}
	if ce != nil {
		return ce
	}
	if e = os.Rename(tmp, path); e != nil {
		return e
	}
	d, e := os.Open(filepath.Dir(path))
	if e != nil {
		return e
	}
	e = d.Sync()
	_ = d.Close()
	return e
}
func importRun(ctx context.Context, db *sql.DB, dir, database string, dry bool, identity, confirmation string, record func(Report) error) error {
	m, e := bundle.Verify(dir)
	if e != nil {
		return e
	}
	if database == "" {
		return errors.New("target database required")
	}
	if confirmation != "" && (identity == "" || PlanHash(m, identity) != confirmation) {
		return errors.New("plan changed after authorization")
	}
	if dry {
		return nil
	}
	report := Report{RunID: m.RunID, Plan: PlanHash(m, identity), Source: m.Source, TransactionID: m.TransactionID, Target: identity, Snapshot: m.Snapshot, Excluded: m.Excluded, State: "unpublished"}
	for i, t := range m.Tables {
		report.Selected = append(report.Selected, t.Schema+"."+t.Name)
		report.Planned = append(report.Planned, stage(m.RunID, i))
	}
	fail := func(table string, err error) error {
		if table != "" {
			report.Failed = append(report.Failed, table)
		}
		report.State = "unpublished_failed"
		if record != nil {
			_ = record(report)
		}
		return err
	}
	if record != nil {
		if e = record(report); e != nil {
			return e
		}
	}
	existing, err := db.QueryContext(ctx, "SELECT table_name FROM information_schema.tables WHERE table_schema=DATABASE()")
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for existing.Next() {
		var n string
		if err = existing.Scan(&n); err != nil {
			break
		}
		seen[strings.ToLower(n)] = true
	}
	if err == nil {
		err = existing.Err()
	}
	_ = existing.Close()
	if err != nil {
		return err
	}
	for i, t := range m.Tables {
		if seen[strings.ToLower(t.Name)] || seen[strings.ToLower(stage(m.RunID, i))] {
			return fail(t.Schema+"."+t.Name, fmt.Errorf("target table exists: %s (no overwrite authorization in v2)", t.Name))
		}
		if _, err = createSQL(stage(m.RunID, i), t); err != nil {
			return fail(t.Schema+"."+t.Name, err)
		}
	}
	c, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer c.Close()
	var current, mode string
	var strict int
	if err = c.QueryRowContext(ctx, "SELECT DATABASE(), @@SESSION.sql_mode, @@SESSION.innodb_strict_mode").Scan(&current, &mode, &strict); err != nil {
		return err
	}
	if current != database || strict != 1 || !strings.Contains(mode, "STRICT_ALL_TABLES") && !strings.Contains(mode, "STRICT_TRANS_TABLES") {
		return errors.New("target identity or strict mode mismatch")
	}
	for i, t := range m.Tables {
		name := stage(m.RunID, i)
		ddl, _ := createSQL(name, t)
		if _, err = c.ExecContext(ctx, ddl); err != nil {
			return fail(t.Schema+"."+t.Name, fmt.Errorf("staging DDL failed for %s: %w", t.Name, err))
		}
		if err = warnings(ctx, c); err != nil {
			return fail(t.Schema+"."+t.Name, err)
		}
		if err = verifySchema(ctx, c, name, t); err != nil {
			return fail(t.Schema+"."+t.Name, err)
		}
		f, e := os.Open(filepath.Join(dir, t.File))
		if e != nil {
			return fail(t.Schema+"."+t.Name, e)
		}
		stmt := statement(name, t)
		var consumed bundle.Digest
		var consumedBlocks []bundle.Digest
		err = bundle.Stream(f, t, func(row bundle.Row) error {
			if e := ctx.Err(); e != nil {
				return e
			}
			v, e := values(row, t)
			if e != nil {
				return e
			}
			result, e := c.ExecContext(ctx, stmt, v...)
			if e != nil {
				return fmt.Errorf("staging write uncertain/failed; never replay automatically: %w", e)
			}
			n, e := result.RowsAffected()
			if e != nil || n != 1 {
				return errors.New("insert affected row count ambiguous")
			}
			if e = warnings(ctx, c); e != nil {
				return e
			}
			t.AddRow(&consumed, &consumedBlocks, row)
			return nil
		})
		_ = f.Close()
		if err != nil {
			return fail(t.Schema+"."+t.Name, err)
		}
		if consumed != t.Data || !bundle.EqualBlocks(consumedBlocks, t.Blocks) {
			return fail(t.Schema+"."+t.Name, fmt.Errorf("input digest/count mismatch for %s", t.Name))
		}
		actual, actualBlocks, e := readBack(ctx, c, name, t)
		if e != nil {
			return fail(t.Schema+"."+t.Name, e)
		}
		if actual != t.Data || !bundle.EqualBlocks(actualBlocks, t.Blocks) {
			bucket := "multiset"
			for i := range t.Blocks {
				if i >= len(actualBlocks) || t.Blocks[i] != actualBlocks[i] {
					bucket = fmt.Sprintf("PK hash bucket %d", i)
					break
				}
			}
			return fail(t.Schema+"."+t.Name, fmt.Errorf("target content mismatch for %s (%s): expected %v, got %v", t.Name, bucket, t.Data, actual))
		}
		report.Staged = append(report.Staged, name)
		if record != nil {
			if e = record(report); e != nil {
				return e
			}
		}
	}
	report.State = "staged_verified_not_published"
	if record != nil {
		return record(report)
	}
	return nil
}

// Open requires a verified TLS connection; target DSN must be supplied out-of-band.
func Open(dsn string) (*sql.DB, string, error) {
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		return nil, "", errors.New("invalid target DSN (redacted)")
	}
	if cfg.DBName == "" || cfg.TLSConfig == "" || cfg.TLSConfig == "false" || cfg.TLSConfig == "skip-verify" || cfg.TLSConfig == "preferred" || cfg.Net != "tcp" {
		return nil, "", errors.New("target requires database and TLS over TCP")
	}
	cfg.ParseTime = false // readback always uses canonical textual date/time values
	if cfg.Params == nil {
		cfg.Params = map[string]string{}
	}
	// Set strict session behavior on every physical connection, not just after Ping.
	cfg.Params["charset"] = "utf8mb4"
	cfg.Params["sql_mode"] = "'STRICT_ALL_TABLES,NO_ENGINE_SUBSTITUTION'"
	cfg.Params["innodb_strict_mode"] = "1"
	connector, err := mysql.NewConnector(cfg)
	if err != nil {
		return nil, "", errors.New("invalid target connector (redacted)")
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(1)
	return db, cfg.DBName, nil
}

// PlanHash identifies one run and one target endpoint, without exposing a password.
func PlanHash(m bundle.Manifest, target string) string {
	b, _ := json.Marshal(m)
	h := sha256.Sum256(append(append(b, 0), []byte(target)...))
	return hex.EncodeToString(h[:])
}
