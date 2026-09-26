package bundle

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestDecimalExactZeroWithFullScale(t *testing.T) {
	if e := CheckRow(Row{{Text: "0.00"}}, Table{Columns: []Column{{Name: "d", Type: "decimal(2,2)"}}}); e != nil {
		t.Fatal(e)
	}
}
func TestRejectInvalidValueBeforeSealing(t *testing.T) {
	cases := []struct{ typ, value string }{{"int", "2147483648"}, {"tinyint", "-1"}, {"decimal(4,2)", "123.45"}, {"date", "2025-02-30"}, {"datetime2(6)", "2025-01-01T00:00:00.1234567"}, {"nvarchar(2)", "xyz"}, {"varbinary(1)", "AAE="}}
	for _, tc := range cases {
		if e := CheckRow(Row{{Text: tc.value}}, Table{Columns: []Column{{Name: "x", Type: tc.typ}}}); e == nil {
			t.Errorf("accepted %s: %s", tc.typ, tc.value)
		}
	}
}
func TestPrimaryKeyBlocksLocateDifferences(t *testing.T) {
	tab := Table{Columns: []Column{{Name: "id", Type: "int"}, {Name: "v", Type: "nvarchar(10)"}}, Indexes: []Index{{Name: "PK", Primary: true, Unique: true, Columns: []string{"id"}}}}
	var a, b Digest
	var blocksA, blocksB []Digest
	tab.AddRow(&a, &blocksA, Row{{Text: "1"}, {Text: "good"}})
	tab.AddRow(&b, &blocksB, Row{{Text: "1"}, {Text: "bad"}})
	if a == b || EqualBlocks(blocksA, blocksB) || len(blocksA) != 16 {
		t.Fatal("PK block missed value change")
	}
}
func TestCellsAndMultiset(t *testing.T) {
	a := Row{nil, {Text: ""}, {Text: "  "}, {Text: "AA=="}}
	b := Row{nil, {Text: ""}, {Text: " "}, {Text: "AA=="}}
	if bytes.Equal(Encode(a), Encode(b)) {
		t.Fatal("spaces lost")
	}
	var x, y Digest
	x.Add(a)
	x.Add(a)
	x.Add(b)
	y.Add(b)
	y.Add(a)
	y.Add(a)
	if x != y {
		t.Fatal("ordering changed digest")
	}
	var z Digest
	z.Add(a)
	z.Add(b)
	z.Add(b)
	if x == z {
		t.Fatal("multiplicity lost")
	}
}

func TestSealedBundleRejectsTampering(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "bundle")
	m := Manifest{Version: 2, RunID: "run-1", Source: "server/db", Snapshot: "SNAPSHOT", TransactionID: 1, Started: "2026-01-01T00:00:00Z", Ended: "2026-01-01T00:00:01Z", Tables: []Table{{Schema: "dbo", Name: "T", Columns: []Column{{Name: "id", Type: "int", Nullable: false}}, File: "000001.rows"}}}
	w, err := Begin(dir, m)
	if err != nil {
		t.Fatal(err)
	}
	if err = w.Row(0, Row{{Text: "1"}}); err != nil {
		t.Fatal(err)
	}
	if err = w.Seal(); err != nil {
		t.Fatal(err)
	}
	if _, err = Verify(dir); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "000001.rows"), []byte("bad"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = Verify(dir); err == nil {
		t.Fatal("accepted corrupted data")
	}
}

func TestInvalidUnicodeEscapeRejected(t *testing.T) {
	tab := Table{Columns: []Column{{Name: "x", Type: "nvarchar(max)"}}}
	if e := Stream(bytes.NewBufferString(`[{"v":"\ud800"}]`+"\n"), tab, func(Row) error { return nil }); e == nil {
		t.Fatal("unpaired surrogate silently replaced")
	}
	if e := Stream(bytes.NewBufferString(`[{"v":"\\uD800"}]`+"\n"), tab, func(Row) error { return nil }); e != nil {
		t.Fatal(e)
	}
}
func TestDuplicateJSONKeysRejected(t *testing.T) {
	if e := uniqueJSON([]byte(`{"run_id":"x","run_id":"y"}`)); e == nil {
		t.Fatal("manifest key ambiguity")
	}
	tab := Table{Columns: []Column{{Name: "x", Type: "nvarchar(max)"}}}
	if e := Stream(bytes.NewBufferString(`[{"v":"a","v":"b"}]`+"\n"), tab, func(Row) error { return nil }); e == nil {
		t.Fatal("row key ambiguity")
	}
}
func TestBudgetFailureNotSealed(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "b")
	m := Manifest{Version: 2, RunID: "r", Source: "s", Snapshot: "SNAPSHOT", Tables: []Table{{Schema: "dbo", Name: "T", File: "000001.rows", Columns: []Column{{Name: "x", Type: "int"}}}}}
	w, e := Begin(dir, m)
	if e != nil {
		t.Fatal(e)
	}
	defer w.Close()
	if e = w.SetLimit(1); e != nil {
		t.Fatal(e)
	}
	if e = w.Row(0, Row{{Text: "1"}}); e == nil {
		t.Fatal("over budget accepted")
	}
	if _, e = Verify(dir); e == nil {
		t.Fatal("over-budget output sealed")
	}
}
func TestInvalidManifestAndTextRejected(t *testing.T) {
	base := Manifest{Version: 2, RunID: "r", Source: "server/db", Snapshot: "SNAPSHOT", Tables: []Table{{Schema: "dbo", Name: "T", File: "../escape.rows", Columns: []Column{{Name: "id", Type: "int"}}}}}
	if Valid(base) == nil {
		t.Fatal("path traversal")
	}
	base.Tables[0].File = "000001.rows"
	base.Tables[0].Columns = append(base.Tables[0].Columns, Column{Name: "ID", Type: "int"})
	if Valid(base) == nil {
		t.Fatal("duplicate column")
	}
	if CheckRow(Row{{Text: string([]byte{0xff})}}, Table{Columns: []Column{{Name: "x", Type: "nvarchar(max)"}}}) == nil {
		t.Fatal("invalid utf8")
	}
}
func TestCanonicalPadsDriverTextWithoutRounding(t *testing.T) {
	if _, e := Canonical("datetime2(6)", "2025-01-02T03:04:05.123456Z"); e == nil {
		t.Fatal("accepted unapproved timezone conversion")
	}
	d, e := Canonical("decimal(8,2)", "12.3")
	if e != nil || d != "12.30" {
		t.Fatalf("decimal %q %v", d, e)
	}
	ts, e := Canonical("datetime2(6)", "2025-01-02 03:04:05.123")
	if e != nil || ts != "2025-01-02T03:04:05.123000" {
		t.Fatalf("datetime %q %v", ts, e)
	}
	if _, e = Canonical("decimal(4,2)", "12.345"); e == nil {
		t.Fatal("rounded extra scale")
	}
}
func TestQuotedCommaNewlineRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "q")
	w, e := Begin(dir, Manifest{Version: 2, RunID: "r", Source: "s", Snapshot: "SNAPSHOT", TransactionID: 1, Started: "2026-01-01T00:00:00Z", Ended: "2026-01-01T00:00:01Z", Tables: []Table{{Schema: "dbo", Name: "T", File: "000001.rows", Columns: []Column{{Name: "v", Type: "nvarchar(50)"}}}}})
	if e != nil {
		t.Fatal(e)
	}
	row := Row{{Text: "a,b\n\"c\""}}
	if e = w.Row(0, row); e != nil {
		t.Fatal(e)
	}
	if e = w.Seal(); e != nil {
		t.Fatal(e)
	}
	m, e := Verify(dir)
	if e != nil {
		t.Fatal(e)
	}
	f, e := os.Open(filepath.Join(dir, m.Tables[0].File))
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	var got string
	if e = Stream(f, m.Tables[0], func(r Row) error { got = r[0].Text; return nil }); e != nil || got != "a,b\n\"c\"" {
		t.Fatalf("got %q %v", got, e)
	}
}
func TestNoPKDuplicateRowsAreAMultiset(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "dup")
	w, e := Begin(dir, Manifest{Version: 2, RunID: "r", Source: "s", Snapshot: "SNAPSHOT", TransactionID: 1, Started: "2026-01-01T00:00:00Z", Ended: "2026-01-01T00:00:01Z", Tables: []Table{{Schema: "dbo", Name: "T", File: "000001.rows", Columns: []Column{{Name: "v", Type: "int"}}}}})
	if e != nil {
		t.Fatal(e)
	}
	if e = w.Row(0, Row{{Text: "1"}}); e != nil {
		t.Fatal(e)
	}
	if e = w.Row(0, Row{{Text: "1"}}); e != nil {
		t.Fatal(e)
	}
	if e = w.Seal(); e != nil {
		t.Fatal(e)
	}
	m, e := Verify(dir)
	if e != nil || m.Tables[0].Data.Count != 2 {
		t.Fatal(m.Tables[0].Data, e)
	}
}
func TestMissingOrExtraBundleFilesRejected(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "files")
	w, e := Begin(dir, Manifest{Version: 2, RunID: "r", Source: "s", Snapshot: "SNAPSHOT", TransactionID: 1, Started: "2026-01-01T00:00:00Z", Ended: "2026-01-01T00:00:01Z", Tables: []Table{{Schema: "dbo", Name: "T", File: "000001.rows", Columns: []Column{{Name: "id", Type: "int"}}}}})
	if e != nil {
		t.Fatal(e)
	}
	if e = w.Row(0, Row{{Text: "1"}}); e != nil {
		t.Fatal(e)
	}
	if e = w.Seal(); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(dir, "T.csv"), []byte("1\n"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = Verify(dir); e == nil {
		t.Fatal("accepted extra CSV")
	}
	if e = os.Remove(filepath.Join(dir, "T.csv")); e != nil {
		t.Fatal(e)
	}
	if e = os.Remove(filepath.Join(dir, "000001.rows")); e != nil {
		t.Fatal(e)
	}
	if _, e = Verify(dir); e == nil {
		t.Fatal("accepted missing data file")
	}
}
func TestColumnCountMismatchRejected(t *testing.T) {
	tab := Table{Columns: []Column{{Name: "a", Type: "int"}, {Name: "b", Type: "int"}}}
	if CheckRow(Row{{Text: "1"}}, tab) == nil {
		t.Fatal("short row")
	}
	if CheckRow(Row{{Text: "1"}, {Text: "2"}, {Text: "3"}}, tab) == nil {
		t.Fatal("long row")
	}
}
func TestDifferentSnapshotsDoNotShareIdentity(t *testing.T) {
	a := Manifest{Version: 2, RunID: "run-a", Source: "s", Snapshot: "SNAPSHOT", TransactionID: 1, Started: "2026-01-01T00:00:00Z", Ended: "2026-01-01T00:00:01Z", Tables: []Table{{Schema: "dbo", Name: "T", File: "000001.rows", Columns: []Column{{Name: "id", Type: "int"}}}}}
	b := a
	b.RunID = "run-b"
	b.TransactionID = 2
	if fmtIdentity(a) == fmtIdentity(b) {
		t.Fatal("snapshots collided")
	}
}
func fmtIdentity(m Manifest) string {
	return m.RunID + "/" + m.Snapshot + "/" + itoa64(m.TransactionID)
}
func itoa64(n int64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
func TestUnsealedCannotImport(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "bundle")
	w, err := Begin(dir, Manifest{Version: 2, RunID: "r", Source: "s", Snapshot: "SNAPSHOT", Tables: []Table{{Schema: "dbo", Name: "T", Columns: []Column{{Name: "x", Type: "int"}}, File: "000001.rows"}}})
	if err != nil {
		t.Fatal(err)
	}
	_ = w.Row(0, Row{{Text: "1"}})
	if _, err = Verify(dir); err == nil {
		t.Fatal("accepted incomplete export")
	}
}
