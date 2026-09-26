// Package bundle defines the sealed, versioned, strictly decoded transfer format.
package bundle

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"
)

const MaxRow = 16 << 20
const DefaultMaxBundleBytes int64 = 10 << 30

type Cell struct {
	Text string `json:"v"`
}
type Row []*Cell          // nil is SQL NULL; binary cells use canonical base64 in v.
func Encode(r Row) []byte { b, _ := json.Marshal(r); return b }

type Digest struct {
	Count uint64 `json:"count"`
	Sum   string `json:"sum"`
}

func (d *Digest) Add(r Row) {
	h := sha256.Sum256(Encode(r))
	var sum [32]byte
	if d.Sum != "" {
		raw, _ := hex.DecodeString(d.Sum)
		copy(sum[:], raw)
	}
	carry := 0
	for i := 31; i >= 0; i-- {
		n := int(sum[i]) + int(h[i]) + carry
		sum[i] = byte(n)
		carry = n >> 8
	}
	d.Sum = hex.EncodeToString(sum[:])
	d.Count++
}

// AddRow updates the order-independent table digest and, for PK tables, 16
// deterministic key partitions. A bucket mismatch narrows a manual readback.
func (t Table) AddRow(d *Digest, blocks *[]Digest, r Row) {
	d.Add(r)
	var primary *Index
	for i := range t.Indexes {
		if t.Indexes[i].Primary {
			primary = &t.Indexes[i]
			break
		}
	}
	if primary == nil {
		return
	}
	if *blocks == nil {
		*blocks = make([]Digest, 16)
	}
	key := make(Row, 0, len(primary.Columns))
	for _, name := range primary.Columns {
		for i, c := range t.Columns {
			if strings.EqualFold(c.Name, name) {
				key = append(key, r[i])
				break
			}
		}
	}
	h := sha256.Sum256(Encode(key))
	(*blocks)[int(h[0]>>4)].Add(r)
}
func EqualBlocks(a, b []Digest) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

type Column struct {
	Name       string `json:"name"`
	SourceType string `json:"source_type,omitempty"`
	Collation  string `json:"source_collation,omitempty"`
	Type       string `json:"type"` // normalized transfer type and planned target mapping key
	Nullable   bool   `json:"nullable"`
}
type Index struct {
	Name    string   `json:"name"`
	Unique  bool     `json:"unique"`
	Primary bool     `json:"primary"`
	Columns []string `json:"columns"`
}
type Table struct {
	Schema  string   `json:"schema"`
	Name    string   `json:"name"`
	Columns []Column `json:"columns"`
	Indexes []Index  `json:"indexes"`
	File    string   `json:"file"`
	Bytes   int64    `json:"bytes"`
	SHA256  string   `json:"sha256"`
	Data    Digest   `json:"data"`
	Blocks  []Digest `json:"pk_blocks,omitempty"` // 16 deterministic hash partitions for locating PK mismatches
}
type Manifest struct {
	Version       int      `json:"version"`
	RunID         string   `json:"run_id"`
	Source        string   `json:"source"`
	Snapshot      string   `json:"snapshot"`
	TransactionID int64    `json:"transaction_id"`
	Started       string   `json:"started"`
	Ended         string   `json:"ended"`
	Tables        []Table  `json:"tables"`
	Excluded      []string `json:"excluded"`
	Status        string   `json:"status"`
}

var fileName = regexp.MustCompile(`^[0-9]{6}\.rows$`)
var decimalType = regexp.MustCompile(`^decimal\(([1-9][0-9]?),([0-9]{1,2})\)$`)
var lengthType = regexp.MustCompile(`^(nvarchar|varbinary)\(([1-9][0-9]{0,3})\)$`)
var decimalValue = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?$`)

func Valid(m Manifest) error {
	if m.Version != 2 || m.RunID == "" || m.Source == "" || m.Snapshot != "SNAPSHOT" || len(m.Tables) == 0 {
		return errors.New("invalid bundle identity/version/snapshot or empty scope")
	}
	seen := map[string]bool{}
	targetNames := map[string]bool{}
	files := map[string]bool{}
	for _, t := range m.Tables {
		key := strings.ToLower(t.Schema + "." + t.Name)
		if t.Schema == "" || t.Name == "" || seen[key] || targetNames[strings.ToLower(t.Name)] || !fileName.MatchString(t.File) || files[t.File] || len(t.Columns) == 0 {
			return fmt.Errorf("invalid/duplicate table or file: %s", key)
		}
		seen[key] = true
		targetNames[strings.ToLower(t.Name)] = true
		files[t.File] = true
		cols := map[string]bool{}
		for _, c := range t.Columns {
			k := strings.ToLower(c.Name)
			if k == "" || cols[k] || !Supported(c.Type) {
				return fmt.Errorf("unsupported/duplicate column %s.%s", key, c.Name)
			}
			cols[k] = true
		}
		ix := map[string]bool{}
		pk := 0
		for _, idx := range t.Indexes {
			k := strings.ToLower(idx.Name)
			if k == "" || ix[k] || len(idx.Columns) == 0 {
				return fmt.Errorf("invalid index %s", idx.Name)
			}
			ix[k] = true
			if idx.Primary {
				pk++
				if !idx.Unique {
					return errors.New("non-unique primary key")
				}
			}
			for _, c := range idx.Columns {
				if !cols[strings.ToLower(c)] {
					return fmt.Errorf("unknown index column %s", c)
				}
				for _, column := range t.Columns {
					if strings.EqualFold(column.Name, c) && strings.HasPrefix(column.Type, "nvarchar(") {
						return fmt.Errorf("indexed text requires approved collation mapping: %s", c)
					}
				}
			}
		}
		if pk > 1 {
			return errors.New("multiple primary keys")
		}
	}
	return nil
}

// This deliberately small whitelist rejects lossy implicit type mappings.
func Supported(t string) bool {
	switch t {
	case "int", "bigint", "smallint", "tinyint", "bit", "nvarchar(max)", "varbinary(max)", "date", "datetime2(6)":
		return true
	}
	if x := decimalType.FindStringSubmatch(t); x != nil {
		p, _ := strconv.Atoi(x[1])
		s, _ := strconv.Atoi(x[2])
		return p <= 65 && s <= p
	}
	if x := lengthType.FindStringSubmatch(t); x != nil {
		n, _ := strconv.Atoi(x[2])
		return n <= 4000
	}
	return false
}
func CheckRow(r Row, t Table) error {
	if len(r) != len(t.Columns) {
		return errors.New("column count mismatch")
	}
	for i, c := range r {
		if c == nil {
			if !t.Columns[i].Nullable {
				return fmt.Errorf("null in %s", t.Columns[i].Name)
			}
			continue
		}
		if !utf8.ValidString(c.Text) {
			return errors.New("invalid UTF-8")
		}
		if err := checkValue(t.Columns[i].Type, c.Text); err != nil {
			return fmt.Errorf("%s: %w", t.Columns[i].Name, err)
		}
	}
	return nil
}

func checkValue(typ, s string) error {
	bad := errors.New("non-canonical or out-of-range value for " + typ)
	switch typ {
	case "tinyint":
		n, e := strconv.ParseUint(s, 10, 8)
		if e != nil || strconv.FormatUint(n, 10) != s {
			return bad
		}
		return nil
	case "smallint", "int", "bigint":
		bits := 64
		if typ == "smallint" {
			bits = 16
		}
		if typ == "int" {
			bits = 32
		}
		n, e := strconv.ParseInt(s, 10, bits)
		if e != nil || strconv.FormatInt(n, 10) != s {
			return bad
		}
		return nil
	case "bit":
		if s != "0" && s != "1" {
			return bad
		}
		return nil
	case "date":
		v, e := time.Parse("2006-01-02", s)
		if e != nil || v.Format("2006-01-02") != s {
			return bad
		}
		return nil
	case "datetime2(6)":
		v, e := time.Parse("2006-01-02T15:04:05.000000", s)
		if e != nil || v.Format("2006-01-02T15:04:05.000000") != s {
			return bad
		}
		return nil
	}
	if x := decimalType.FindStringSubmatch(typ); x != nil {
		if !decimalValue.MatchString(s) {
			return bad
		}
		p, _ := strconv.Atoi(x[1])
		scale, _ := strconv.Atoi(x[2])
		parts := strings.Split(s, ".")
		frac := 0
		if len(parts) == 2 {
			frac = len(parts[1])
		}
		intDigits := len(strings.TrimPrefix(parts[0], "-"))
		if p == scale && parts[0] == "0" {
			intDigits = 0
		}
		if frac != scale || intDigits > p-scale {
			return bad
		}
		return nil
	}
	if strings.HasPrefix(typ, "varbinary(") {
		b, e := base64.StdEncoding.Strict().DecodeString(s)
		if e != nil || base64.StdEncoding.EncodeToString(b) != s {
			return bad
		}
		if x := lengthType.FindStringSubmatch(typ); x != nil {
			max, _ := strconv.Atoi(x[2])
			if len(b) > max {
				return bad
			}
		}
		return nil
	}
	if x := lengthType.FindStringSubmatch(typ); x != nil {
		max, _ := strconv.Atoi(x[2])
		if x[1] == "nvarchar" {
			if len(utf16.Encode([]rune(s))) > max {
				return bad
			}
		} else if len([]rune(s)) > max {
			return bad
		}
	}
	return nil
}

// Canonical converts driver/SQL text into the exact transfer encoding.
// Extra fractional digits are rejected rather than rounded.
func Canonical(typ, s string) (string, error) {
	if typ == "datetime2(6)" {
		s = strings.Replace(strings.TrimSpace(s), " ", "T", 1)
		head, frac, ok := strings.Cut(s, ".")
		t, err := time.Parse("2006-01-02T15:04:05", head)
		if err != nil {
			return "", err
		}
		if ok {
			if len(frac) > 6 || strings.IndexFunc(frac, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
				return "", errors.New("non-canonical datetime2(6)")
			}
			frac += strings.Repeat("0", 6-len(frac))
		} else {
			frac = "000000"
		}
		out := t.Format("2006-01-02T15:04:05") + "." + frac
		return out, checkValue(typ, out)
	}
	if x := decimalType.FindStringSubmatch(typ); x != nil {
		raw := strings.TrimSpace(s)
		neg := strings.HasPrefix(raw, "-")
		raw = strings.TrimPrefix(raw, "-")
		if raw == "" || strings.HasPrefix(raw, "+") {
			return "", errors.New("non-canonical decimal")
		}
		if strings.HasPrefix(raw, ".") {
			raw = "0" + raw
		}
		p, _ := strconv.Atoi(x[1])
		scale, _ := strconv.Atoi(x[2])
		parts := strings.Split(raw, ".")
		if len(parts) > 2 || parts[0] == "" || strings.TrimLeft(parts[0], "0123456789") != "" {
			return "", errors.New("non-canonical decimal")
		}
		frac := ""
		if len(parts) == 2 {
			if strings.IndexFunc(parts[1], func(r rune) bool { return r < '0' || r > '9' }) >= 0 || len(parts[1]) > scale {
				return "", errors.New("non-canonical decimal")
			}
			frac = parts[1]
		}
		frac += strings.Repeat("0", scale-len(frac))
		intp := strings.TrimLeft(parts[0], "0")
		if intp == "" {
			intp = "0"
		}
		out := intp
		if scale > 0 {
			out += "." + frac
		}
		if neg && (intp != "0" || strings.Trim(frac, "0") != "") {
			out = "-" + out
		}
		intDigits := len(intp)
		if p == scale && intp == "0" {
			intDigits = 0
		}
		if intDigits > p-scale {
			return "", errors.New("decimal overflow")
		}
		return out, checkValue(typ, out)
	}
	return s, checkValue(typ, s)
}

type Writer struct {
	dir    string
	M      Manifest
	files  []*os.File
	hash   []io.Writer
	sums   []interface{ Sum([]byte) []byte }
	counts []Digest
	blocks [][]Digest
	sizes  []int64
	closed bool
	limit  int64
	total  int64
}

func Begin(dir string, m Manifest) (*Writer, error) {
	if err := Valid(m); err != nil {
		return nil, err
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		return nil, err
	}
	w := &Writer{dir: dir, M: m, limit: DefaultMaxBundleBytes}
	for i, t := range m.Tables {
		f, e := os.OpenFile(filepath.Join(dir, t.File), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			w.Close()
			return nil, e
		}
		w.files = append(w.files, f)
		h := sha256.New()
		w.hash = append(w.hash, h)
		w.sums = append(w.sums, h)
		w.counts = append(w.counts, Digest{})
		w.blocks = append(w.blocks, nil)
		w.sizes = append(w.sizes, 0)
		_ = i
	}
	return w, nil
}

// SetLimit establishes a hard byte stop before writing the next logical row.
func (w *Writer) SetLimit(bytes int64) error {
	if bytes <= 0 {
		return errors.New("bundle byte budget must be positive")
	}
	w.limit = bytes
	return nil
}
func (w *Writer) Row(i int, r Row) error {
	if w.closed || i < 0 || i >= len(w.files) {
		return errors.New("writer closed/index invalid")
	}
	if e := CheckRow(r, w.M.Tables[i]); e != nil {
		return e
	}
	b := append(Encode(r), '\n')
	if len(b) > MaxRow {
		return errors.New("row too large")
	}
	if w.total+int64(len(b)) > w.limit {
		return errors.New("bundle disk budget exceeded; incomplete export is not importable")
	}
	n, e := w.files[i].Write(b)
	if e != nil {
		return e
	}
	if n != len(b) {
		return io.ErrShortWrite
	}
	_, _ = w.hash[i].Write(b)
	w.sizes[i] += int64(n)
	w.total += int64(n)
	w.M.Tables[i].AddRow(&w.counts[i], &w.blocks[i], r)
	return nil
}
func (w *Writer) Count(i int) uint64 { return w.counts[i].Count }
func (w *Writer) Close() {
	if w.closed {
		return
	}
	w.closed = true
	for _, f := range w.files {
		_ = f.Close()
	}
}
func (w *Writer) Seal() error {
	if w.closed {
		return errors.New("writer closed")
	}
	for i, f := range w.files {
		if e := f.Sync(); e != nil {
			w.Close()
			return e
		}
		if e := f.Close(); e != nil {
			w.Close()
			return e
		}
		w.M.Tables[i].Bytes = w.sizes[i]
		w.M.Tables[i].SHA256 = hex.EncodeToString(w.sums[i].Sum(nil))
		w.M.Tables[i].Data = w.counts[i]
		w.M.Tables[i].Blocks = w.blocks[i]
	}
	w.closed = true
	w.M.Status = "sealed"
	b, e := json.MarshalIndent(w.M, "", "  ")
	if e != nil {
		return e
	}
	tmp := filepath.Join(w.dir, "manifest.tmp")
	f, e := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	if _, e = f.Write(b); e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e != nil {
		return e
	}
	if closeErr != nil {
		return closeErr
	}
	if e = os.Rename(tmp, filepath.Join(w.dir, "manifest.json")); e != nil {
		return e
	}
	d, e := os.Open(w.dir)
	if e != nil {
		return e
	}
	de := d.Sync()
	_ = d.Close()
	return de
}
func Verify(dir string) (Manifest, error) {
	var m Manifest
	manifestPath := filepath.Join(dir, "manifest.json")
	info, e := os.Lstat(manifestPath)
	if e != nil {
		return m, e
	}
	if !info.Mode().IsRegular() || info.Size() > 4<<20 {
		return m, errors.New("unsafe manifest")
	}
	entries, e := os.ReadDir(dir)
	if e != nil {
		return m, e
	}
	f, e := os.Open(manifestPath)
	if e != nil {
		return m, e
	}
	defer f.Close()
	manifestBytes, e := io.ReadAll(f)
	if e != nil {
		return m, e
	}
	if !utf8.Valid(manifestBytes) || !validUnicodeEscapes(manifestBytes) {
		return m, errors.New("invalid manifest Unicode")
	}
	if e = uniqueJSON(manifestBytes); e != nil {
		return m, e
	}
	dec := json.NewDecoder(bytes.NewReader(manifestBytes))
	dec.DisallowUnknownFields()
	if e = dec.Decode(&m); e != nil {
		return m, e
	}
	var extra any
	if e = dec.Decode(&extra); e != io.EOF {
		return m, errors.New("trailing manifest data")
	}
	if e = Valid(m); e != nil {
		return m, e
	}
	if m.Status != "sealed" {
		return m, errors.New("unsealed bundle")
	}
	start, es := time.Parse(time.RFC3339Nano, m.Started)
	end, ee := time.Parse(time.RFC3339Nano, m.Ended)
	if m.TransactionID <= 0 || es != nil || ee != nil || end.Before(start) {
		return m, errors.New("missing/invalid snapshot transaction evidence")
	}
	allowed := map[string]bool{"manifest.json": true}
	for _, t := range m.Tables {
		allowed[t.File] = true
	}
	for _, entry := range entries {
		if !allowed[entry.Name()] {
			return m, fmt.Errorf("unlisted bundle entry %s", entry.Name())
		}
	}
	for _, t := range m.Tables {
		path := filepath.Join(dir, t.File)
		info, err := os.Lstat(path)
		if err != nil {
			return m, err
		}
		if !info.Mode().IsRegular() || info.Size() != t.Bytes {
			return m, fmt.Errorf("invalid file %s", t.File)
		}
		f, err := os.Open(path)
		if err != nil {
			return m, err
		}
		h := sha256.New()
		d := Digest{}
		var blocks []Digest
		err = Stream(io.TeeReader(f, h), t, func(r Row) error { t.AddRow(&d, &blocks, r); return nil })
		_ = f.Close()
		if err != nil {
			return m, err
		}
		if hex.EncodeToString(h.Sum(nil)) != t.SHA256 || d != t.Data || !EqualBlocks(blocks, t.Blocks) {
			return m, fmt.Errorf("digest mismatch %s", t.File)
		}
	}
	return m, nil
}

// encoding/json replaces isolated UTF-16 surrogate escapes with U+FFFD.
// Reject them at the wire boundary rather than accepting a changed value.
func validUnicodeEscapes(b []byte) bool {
	inString := false
	for i := 0; i < len(b); i++ {
		switch b[i] {
		case '"':
			inString = !inString
		case '\\':
			if !inString || i+1 >= len(b) {
				continue
			}
			if b[i+1] != 'u' {
				i++
				continue
			}
			if i+6 > len(b) {
				return false
			}
			first, e := strconv.ParseUint(string(b[i+2:i+6]), 16, 16)
			if e != nil {
				return false
			}
			if first >= 0xd800 && first <= 0xdbff {
				if i+12 > len(b) || b[i+6] != '\\' || b[i+7] != 'u' {
					return false
				}
				second, e := strconv.ParseUint(string(b[i+8:i+12]), 16, 16)
				if e != nil || second < 0xdc00 || second > 0xdfff {
					return false
				}
				i += 11
				continue
			}
			if first >= 0xdc00 && first <= 0xdfff {
				return false
			}
			i += 5
		}
	}
	return true
}
func uniqueJSON(b []byte) error {
	if !validUnicodeEscapes(b) {
		return errors.New("invalid Unicode escape")
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	var value func() error
	value = func() error {
		token, e := dec.Token()
		if e != nil {
			return e
		}
		d, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch d {
		case '{':
			seen := map[string]bool{}
			for dec.More() {
				key, e := dec.Token()
				if e != nil {
					return e
				}
				s, ok := key.(string)
				if !ok || seen[s] {
					return errors.New("duplicate/invalid JSON key")
				}
				seen[s] = true
				if e = value(); e != nil {
					return e
				}
			}
			_, e := dec.Token()
			return e
		case '[':
			for dec.More() {
				if e = value(); e != nil {
					return e
				}
			}
			_, e := dec.Token()
			return e
		}
		return errors.New("invalid JSON container")
	}
	if e := value(); e != nil {
		return e
	}
	var extra any
	if e := dec.Decode(&extra); e != io.EOF {
		return errors.New("trailing JSON data")
	}
	return nil
}
func Stream(in io.Reader, t Table, consume func(Row) error) error {
	r := bufio.NewReader(in)
	for {
		line, e := r.ReadSlice('\n')
		if e == bufio.ErrBufferFull {
			var buf bytes.Buffer
			buf.Write(line)
			for e == bufio.ErrBufferFull && buf.Len() <= MaxRow {
				line, e = r.ReadSlice('\n')
				buf.Write(line)
			}
			line = buf.Bytes()
		}
		if len(line) > MaxRow {
			return errors.New("row too large")
		}
		if len(line) > 0 {
			if e != nil || line[len(line)-1] != '\n' {
				return errors.New("truncated row")
			}
			if !utf8.Valid(line) {
				return errors.New("invalid row UTF-8")
			}
			if err := uniqueJSON(line); err != nil {
				return err
			}
			var row Row
			dec := json.NewDecoder(bytes.NewReader(line))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&row); err != nil {
				return err
			}
			var extra any
			if err := dec.Decode(&extra); err != io.EOF {
				return errors.New("trailing row data")
			}
			if err := CheckRow(row, t); err != nil {
				return err
			}
			if err := consume(row); err != nil {
				return err
			}
		}
		if e == io.EOF {
			return nil
		}
		if e != nil {
			return e
		}
	}
}
