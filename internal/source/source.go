// Package source exports only explicitly selected, representable SQL Server tables
// from a single read-only SNAPSHOT transaction. Unsupported schema fails closed.
package source

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	_ "github.com/microsoft/go-mssqldb"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/bundle"
)

var identifier = regexp.MustCompile(`^[\pL_][\pL\pN_@$#]*$`)

func quote(s string) string { return "[" + strings.ReplaceAll(s, "]", "]]") + "]" }
func Type(name string, length, precision, scale int) string {
	switch name {
	case "int", "bigint", "smallint", "tinyint", "bit", "date":
		return name
	case "decimal", "numeric":
		if precision >= 1 && precision <= 65 && scale >= 0 && scale <= precision {
			return fmt.Sprintf("decimal(%d,%d)", precision, scale)
		}
	case "datetime2":
		if scale == 6 {
			return "datetime2(6)"
		}
	case "nvarchar", "varbinary":
		if length == -1 {
			return name + "(max)"
		}
		if name == "nvarchar" {
			if length%2 != 0 {
				return ""
			}
			length /= 2
		}
		if length >= 1 && length <= 4000 {
			return fmt.Sprintf("%s(%d)", name, length)
		}
	}
	return ""
}

const columnsSQL = `SELECT c.name, ty.name, c.max_length, c.precision, c.scale, c.is_nullable, c.is_identity, c.is_computed, CASE WHEN c.default_object_id<>0 THEN 1 ELSE 0 END,ty.is_user_defined,c.collation_name
FROM sys.columns c JOIN sys.types ty ON c.user_type_id=ty.user_type_id WHERE c.object_id=OBJECT_ID(@p1,'U') ORDER BY c.column_id`
const indexesSQL = `SELECT i.name,i.is_unique,i.is_primary_key,i.has_filter,i.type_desc,ic.is_included_column,c.name,ic.key_ordinal,ic.is_descending_key,i.is_disabled FROM sys.indexes i JOIN sys.index_columns ic ON i.object_id=ic.object_id AND i.index_id=ic.index_id JOIN sys.columns c ON c.object_id=ic.object_id AND c.column_id=ic.column_id WHERE i.object_id=OBJECT_ID(@p1,'U') AND i.index_id>0 ORDER BY i.index_id,ic.key_ordinal,ic.index_column_id`

func schema(ctx context.Context, tx *sql.Tx, selected []string) ([]bundle.Table, error) {
	tables := make([]bundle.Table, 0, len(selected))
	seen := map[string]bool{}
	for n, full := range selected {
		parts := strings.Split(full, ".")
		if len(parts) != 2 || !identifier.MatchString(parts[0]) || !identifier.MatchString(parts[1]) || seen[strings.ToLower(full)] {
			return nil, fmt.Errorf("invalid/duplicate selected table %q", full)
		}
		seen[strings.ToLower(full)] = true
		var rejects int
		// Fail on objects whose semantics cannot be represented by this version.
		err := tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM sys.foreign_keys WHERE parent_object_id=OBJECT_ID(@p1,'U') OR referenced_object_id=OBJECT_ID(@p1,'U'))+(SELECT COUNT(*) FROM sys.triggers WHERE parent_id=OBJECT_ID(@p1,'U'))+(SELECT COUNT(*) FROM sys.check_constraints WHERE parent_object_id=OBJECT_ID(@p1,'U'))`, full).Scan(&rejects)
		if err != nil {
			return nil, err
		}
		if rejects != 0 {
			return nil, fmt.Errorf("unsupported constraints/triggers on %s", full)
		}
		t := bundle.Table{Schema: parts[0], Name: parts[1], File: fmt.Sprintf("%06d.rows", n+1)}
		rows, err := tx.QueryContext(ctx, columnsSQL, full)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var name, typ string
			var length, prec, scale int
			var nullable, identity, computed, def, alias bool
			var collation sql.NullString
			if err = rows.Scan(&name, &typ, &length, &prec, &scale, &nullable, &identity, &computed, &def, &alias, &collation); err != nil {
				break
			}
			mapped := Type(typ, length, prec, scale)
			if identity || computed || def || alias || mapped == "" {
				err = fmt.Errorf("unsupported column %s.%s type=%s identity/computed/default", full, name, typ)
				break
			}
			t.Columns = append(t.Columns, bundle.Column{Name: name, SourceType: fmt.Sprintf("%s(length=%d,precision=%d,scale=%d)", typ, length, prec, scale), Collation: collation.String, Type: mapped, Nullable: nullable})
		}
		if err == nil {
			err = rows.Err()
		}
		_ = rows.Close()
		if err != nil {
			return nil, err
		}
		if len(t.Columns) == 0 {
			return nil, fmt.Errorf("missing table/columns %s", full)
		}
		ixrows, err := tx.QueryContext(ctx, indexesSQL, full)
		if err != nil {
			return nil, err
		}
		for ixrows.Next() {
			var name, kind, col string
			var unique, primary, filtered, included, descending, disabled bool
			var ordinal int
			if err = ixrows.Scan(&name, &unique, &primary, &filtered, &kind, &included, &col, &ordinal, &descending, &disabled); err != nil {
				break
			}
			if filtered || included || descending || disabled || kind != "CLUSTERED" && kind != "NONCLUSTERED" || ordinal == 0 {
				err = fmt.Errorf("unsupported index %s on %s", name, full)
				break
			}
			if len(t.Indexes) == 0 || t.Indexes[len(t.Indexes)-1].Name != name {
				t.Indexes = append(t.Indexes, bundle.Index{Name: name, Unique: unique, Primary: primary})
			}
			idx := &t.Indexes[len(t.Indexes)-1]
			idx.Columns = append(idx.Columns, col)
		}
		if err == nil {
			err = ixrows.Err()
		}
		_ = ixrows.Close()
		if err != nil {
			return nil, err
		}
		var indexCount int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sys.indexes WHERE object_id=OBJECT_ID(@p1,'U') AND index_id>0`, full).Scan(&indexCount); err != nil {
			return nil, err
		}
		if indexCount != len(t.Indexes) {
			return nil, fmt.Errorf("index omitted on %s", full)
		}
		tables = append(tables, t)
	}
	return tables, nil
}
func expression(c bundle.Column) string {
	q := quote(c.Name)
	switch {
	case c.Type == "datetime2(6)":
		return "CONVERT(nvarchar(33)," + q + ",126)"
	case strings.HasPrefix(c.Type, "decimal("):
		return "CONVERT(nvarchar(100)," + q + ")"
	default:
		return q
	}
}

// Export requires a fresh, non-existing destination. A failure leaves no sealed manifest.
func Export(ctx context.Context, db *sql.DB, selected []string, dir string) error {
	return ExportWithLimit(ctx, db, selected, dir, bundle.DefaultMaxBundleBytes)
}

// ExportWithLimit applies a hard disk budget while streaming one snapshot transaction.
func ExportWithLimit(ctx context.Context, db *sql.DB, selected []string, dir string, maxBytes int64) error {
	return exportWithObserver(ctx, db, selected, dir, maxBytes, nil)
}

// afterTable is a test-only synchronization point for a concurrent source writer.
func exportWithObserver(ctx context.Context, db *sql.DB, selected []string, dir string, maxBytes int64, afterTable func(int) error) error {
	trimmed := make([]string, 0, len(selected))
	for _, name := range selected {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		trimmed = append(trimmed, name)
	}
	selected = trimmed
	if len(selected) == 0 {
		return errors.New("explicit table scope required")
	}
	var enabled int
	if err := db.QueryRowContext(ctx, `SELECT snapshot_isolation_state FROM sys.databases WHERE name=DB_NAME()`).Scan(&enabled); err != nil {
		return err
	}
	if enabled != 1 {
		return errors.New("ALLOW_SNAPSHOT_ISOLATION must be ON; refusing non-consistent export")
	}
	var viewDefinition int
	if err := db.QueryRowContext(ctx, `SELECT HAS_PERMS_BY_NAME(DB_NAME(),'DATABASE','VIEW DEFINITION')`).Scan(&viewDefinition); err != nil {
		return err
	}
	if viewDefinition != 1 {
		return errors.New("database VIEW DEFINITION permission required for object inventory")
	}
	// The driver rejects TxOptions.ReadOnly; SELECT-only SQL plus a read-only login enforce it.
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSnapshot})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var server, database string
	if err = tx.QueryRowContext(ctx, `SELECT CONVERT(nvarchar(128),SERVERPROPERTY('ServerName')),DB_NAME()`).Scan(&server, &database); err != nil {
		return err
	}
	var transactionID int64
	if err = tx.QueryRowContext(ctx, "SELECT CURRENT_TRANSACTION_ID()").Scan(&transactionID); err != nil {
		return fmt.Errorf("cannot prove snapshot transaction: %w", err)
	}
	tables, err := schema(ctx, tx, selected)
	if err != nil {
		return err
	}
	inventory, err := tx.QueryContext(ctx, `SELECT o.type, SCHEMA_NAME(o.schema_id),o.name,COALESCE(SCHEMA_NAME(parent.schema_id),''),COALESCE(parent.name,'') FROM sys.objects o LEFT JOIN sys.tables parent ON o.parent_object_id=parent.object_id WHERE o.is_ms_shipped=0 ORDER BY o.type,o.schema_id,o.name`)
	if err != nil {
		return err
	}
	var excluded []string
	scope := map[string]bool{}
	for _, name := range selected {
		scope[strings.ToLower(name)] = true
	}
	for inventory.Next() {
		var kind, sch, name, parentSchema, parentName string
		if err = inventory.Scan(&kind, &sch, &name, &parentSchema, &parentName); err != nil {
			break
		}
		if kind == "U" && scope[strings.ToLower(sch+"."+name)] {
			continue
		}
		if (kind == "PK" || kind == "UQ") && scope[strings.ToLower(parentSchema+"."+parentName)] {
			continue
		}
		excluded = append(excluded, kind+":"+sch+"."+name)
	}
	if err == nil {
		err = inventory.Err()
	}
	_ = inventory.Close()
	if err != nil {
		return err
	}
	m := bundle.Manifest{Version: 2, RunID: uuid.NewString(), Source: fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(server+"\x00"+database))), Snapshot: "SNAPSHOT", TransactionID: transactionID, Started: time.Now().UTC().Format(time.RFC3339Nano), Tables: tables, Excluded: excluded}
	w, err := bundle.Begin(dir, m)
	if err != nil {
		return err
	}
	defer w.Close()
	if err = w.SetLimit(maxBytes); err != nil {
		return err
	}
	for i, t := range tables {
		var sourceCount uint64
		if err = tx.QueryRowContext(ctx, "SELECT COUNT_BIG(*) FROM "+quote(t.Schema)+"."+quote(t.Name)).Scan(&sourceCount); err != nil {
			return err
		}
		cols := make([]string, len(t.Columns))
		for n, c := range t.Columns {
			cols[n] = expression(c)
		}
		query := "SELECT " + strings.Join(cols, ",") + " FROM " + quote(t.Schema) + "." + quote(t.Name)
		rows, e := tx.QueryContext(ctx, query)
		if e != nil {
			return e
		}
		for rows.Next() {
			values := make([]any, len(cols))
			dest := make([]any, len(cols))
			for j := range dest {
				dest[j] = &values[j]
			}
			if e = rows.Scan(dest...); e != nil {
				break
			}
			r := make(bundle.Row, len(cols))
			for j, v := range values {
				if v == nil {
					continue
				}
				var s string
				switch x := v.(type) {
				case []byte:
					if strings.HasPrefix(t.Columns[j].Type, "varbinary(") {
						s = base64.StdEncoding.EncodeToString(x)
					} else {
						s = string(x)
					}
				case string:
					s = x
				case bool:
					if x {
						s = "1"
					} else {
						s = "0"
					}
				case time.Time:
					if t.Columns[j].Type != "date" {
						e = fmt.Errorf("unexpected source time type for %s", t.Columns[j].Name)
						break
					}
					s = x.Format("2006-01-02")
				case int64, int32, int16, int8, int, uint64, uint32, uint16, uint8:
					s = fmt.Sprint(x)
				default:
					e = fmt.Errorf("unsupported driver value type %T for %s", v, t.Columns[j].Name)
				}
				if e != nil {
					break
				}
				if !utf8.ValidString(s) {
					e = fmt.Errorf("invalid Unicode in %s.%s", t.Name, t.Columns[j].Name)
					break
				}
				if !strings.HasPrefix(t.Columns[j].Type, "varbinary(") {
					s, e = bundle.Canonical(t.Columns[j].Type, s)
					if e != nil {
						break
					}
				}
				r[j] = &bundle.Cell{Text: s}
			}
			if e != nil {
				break
			}
			if e = w.Row(i, r); e != nil {
				break
			}
		}
		if e == nil {
			e = rows.Err()
		}
		_ = rows.Close()
		if e != nil {
			return e
		}
		if w.Count(i) != sourceCount {
			return fmt.Errorf("snapshot count mismatch for %s", t.Name)
		}
		if afterTable != nil {
			if e := afterTable(i); e != nil {
				return e
			}
		}
	}
	// Repeat metadata query while still in the same transaction; DDL conflicts fail closed.
	after, err := schema(ctx, tx, selected)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(after, tables) {
		return errors.New("schema drift during export")
	}
	var endTransactionID int64
	if err = tx.QueryRowContext(ctx, "SELECT CURRENT_TRANSACTION_ID()").Scan(&endTransactionID); err != nil || endTransactionID != transactionID {
		return errors.New("snapshot transaction identity changed")
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	w.M.Ended = time.Now().UTC().Format(time.RFC3339Nano)
	if err = w.Seal(); err != nil {
		return err
	}
	_, err = bundle.Verify(dir)
	return err
}

// Open validates transport encryption: callers supply the URL only through a secret env variable.
func Open(ctx context.Context, connectionURL string) (*sql.DB, error) {
	u, e := url.Parse(connectionURL)
	if e != nil || u.Scheme != "sqlserver" || u.Host == "" || !strings.EqualFold(u.Query().Get("encrypt"), "true") {
		return nil, errors.New("SQL Server requires sqlserver:// URL and encrypt=true")
	}
	for k, values := range u.Query() {
		if strings.EqualFold(k, "TrustServerCertificate") {
			for _, v := range values {
				if v != "0" && !strings.EqualFold(v, "false") {
					return nil, errors.New("SQL Server requires verified certificate")
				}
			}
		}
	}
	db, err := sql.Open("sqlserver", connectionURL)
	if err != nil {
		return nil, errors.New("invalid source connector (credentials redacted)")
	}
	db.SetMaxOpenConns(1)
	db.SetConnMaxLifetime(30 * time.Minute)
	pctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err = db.PingContext(pctx); err != nil {
		db.Close()
		return nil, errors.New("SQL Server connection failed (credentials redacted)")
	}
	return db, nil
}
