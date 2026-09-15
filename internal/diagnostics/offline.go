package diagnostics

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Reports is a bounded reader for completed schema-1 runs. A malformed, partial,
// or unsuccessful run can be inspected locally but cannot become performance evidence.
type Reports struct {
	Manifest Manifest
	Summary  Summary
	Tables   []Table
	Metrics  []Snapshot
}

const maxArtifactBytes = 64 << 20

var hexID = regexp.MustCompile(`^[0-9a-f]{64}$`)
var runIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
var versionPattern = regexp.MustCompile(`^[A-Za-z0-9.+_-]{1,128}$`)

func decodeFile(path string, v any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > maxArtifactBytes {
		return errors.New("artifact size/type invalid")
	}
	d := json.NewDecoder(io.LimitReader(f, maxArtifactBytes+1))
	d.DisallowUnknownFields()
	if err = d.Decode(v); err != nil {
		return err
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return errors.New("trailing report data")
	}
	return nil
}
func decodeLines[T any](path string) ([]T, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxArtifactBytes {
		return nil, errors.New("artifact size/type invalid")
	}
	d := json.NewDecoder(io.LimitReader(f, maxArtifactBytes+1))
	d.DisallowUnknownFields()
	var out []T
	for {
		var item T
		err := d.Decode(&item)
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		out = append(out, item)
		if len(out) > 100000 {
			return nil, errors.New("too many report records")
		}
	}
}
func ReadReport(dir string) (*Reports, error) {
	r := &Reports{}
	if err := decodeFile(filepath.Join(dir, "manifest.json"), &r.Manifest); err != nil {
		return nil, err
	}
	if err := decodeFile(filepath.Join(dir, "summary.json"), &r.Summary); err != nil {
		return nil, err
	}
	m, s := r.Manifest, r.Summary
	if m.Schema != Schema || s.Schema != Schema || !runIDPattern.MatchString(m.RunID) || m.RunID != s.RunID || !s.Complete {
		return nil, errors.New("unsupported, partial or mismatched report")
	}
	if !hexID.MatchString(m.KeyID) || !hexID.MatchString(m.TargetID) || !hexID.MatchString(m.ScopeFingerprint) {
		return nil, errors.New("invalid report identifiers")
	}
	for _, v := range []string{m.Version, m.Commit, m.Go, m.Driver, m.OS, m.Arch} {
		if !versionPattern.MatchString(v) {
			return nil, errors.New("invalid build identity")
		}
	}
	if m.Strategy != "prepared" {
		return nil, errors.New("unsupported strategy")
	}
	var err error
	r.Tables, err = decodeLines[Table](filepath.Join(dir, "tables.jsonl"))
	if err != nil {
		return nil, err
	}
	r.Metrics, err = decodeLines[Snapshot](filepath.Join(dir, "metrics.jsonl"))
	if err != nil {
		return nil, err
	}
	if len(r.Tables) != m.Selected || len(r.Metrics) == 0 {
		return nil, errors.New("missing table or metric records")
	}
	ids := make(map[string]bool)
	var consumed, affected int64
	for _, t := range r.Tables {
		consumed += t.Counters.ConsumedRows
		affected += t.Counters.AffectedRows
		if s.Success && (!oneOf(t.State, "success", "missing", "skipped") || oneOf(t.CountStatus, "mismatch", "error")) {
			return nil, errors.New("success contradicts table states")
		}
		if !hexID.MatchString(t.ID) || ids[t.ID] {
			return nil, errors.New("invalid or duplicate table ID")
		}
		ids[t.ID] = true
		if !oneOf(t.State, "pending", "active", "success", "failed", "missing", "skipped", "cancelled") || !oneOf(t.CountStatus, "not_run", "disabled", "match", "mismatch", "error") || !oneOf(t.ErrorClass, "", "database", "unknown_commit", "cancelled", "structure", "io", "other") {
			return nil, errors.New("invalid table status")
		}
		if t.ErrorStage != "" && !validMetric(t.ErrorStage) {
			return nil, errors.New("invalid error stage")
		}
		if err := validateTimes(t.Times); err != nil {
			return nil, err
		}
	}
	if consumed != s.Counters.ConsumedRows || affected != s.Counters.AffectedRows || (s.Success && s.Cancelled) {
		return nil, errors.New("inconsistent final counters/status")
	}
	if err := validateTimes(s.Times); err != nil {
		return nil, err
	}
	for i := range r.Metrics {
		if err := validateTimes(r.Metrics[i].Times); err != nil {
			return nil, err
		}
		r.Metrics[i].ProcessMetricsStatus = sanitizeProcessStatus(r.Metrics[i].ProcessMetricsStatus)
	}
	r.Summary.DatabaseMetricsStatus = "unavailable: no authorized CN/DN sampler"
	return r, nil
}
func oneOf(v string, allowed ...string) bool {
	for _, a := range allowed {
		if v == a {
			return true
		}
	}
	return false
}
func validMetric(v string) bool {
	for _, name := range metricNames {
		if v == name {
			return true
		}
	}
	return false
}
func validateTimes(times map[string]Distribution) error {
	if len(times) != int(metricCount) {
		return errors.New("missing timing fields")
	}
	for name, d := range times {
		if !validMetric(name) || d.TotalNS < 0 {
			return errors.New("invalid timing")
		}
		var sum uint64
		for _, n := range d.Buckets {
			sum += n
		}
		if sum != d.Samples {
			return errors.New("histogram count mismatch")
		}
	}
	return nil
}
func sanitizeProcessStatus(v string) string {
	if oneOf(v, "available: current RSS via procfs", "available: peak RSS via getrusage (darwin)", "unavailable: unsupported OS", "unavailable: getrusage failed", "cpu available; RSS unavailable: procfs unreadable", "cpu available; RSS unavailable: invalid procfs") {
		return v
	}
	return "unavailable: unsupported OS"
}

// Export regenerates artifacts from typed validated data; never bundles original
// logs, local pseudonym keys/mappings, profiles, arbitrary files or report prose.
func Export(dir, destination string) error {
	r, err := ReadReport(dir)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	write := func(name string, value any, lines bool) error {
		var b strings.Builder
		if lines {
			switch v := value.(type) {
			case []Table:
				for _, x := range v {
					if err := json.NewEncoder(&b).Encode(x); err != nil {
						return err
					}
				}
			case []Snapshot:
				for _, x := range v {
					if err := json.NewEncoder(&b).Encode(x); err != nil {
						return err
					}
				}
			}
		} else if text, ok := value.(string); ok {
			b.WriteString(text)
		} else {
			if err := json.NewEncoder(&b).Encode(value); err != nil {
				return err
			}
		}
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(b.Len())}); err != nil {
			return err
		}
		_, err := io.WriteString(tw, b.String())
		return err
	}
	err = write("manifest.json", r.Manifest, false)
	if err == nil {
		err = write("summary.json", r.Summary, false)
	}
	if err == nil {
		err = write("tables.jsonl", r.Tables, true)
	}
	if err == nil {
		err = write("metrics.jsonl", r.Metrics, true)
	}
	if err == nil {
		err = write("report.md", renderReport(r.Manifest, r.Summary), false)
	}
	err = errors.Join(err, tw.Close(), gz.Close(), f.Close())
	if err != nil {
		_ = os.Remove(destination)
	}
	return err
}
func Compare(aDir, bDir string) (string, error) {
	a, err := ReadReport(aDir)
	if err != nil {
		return "", err
	}
	b, err := ReadReport(bDir)
	if err != nil {
		return "", err
	}
	am, bm := a.Manifest, b.Manifest
	if am.Settings.DryRun || bm.Settings.DryRun || am.Settings.CreateOnly || bm.Settings.CreateOnly {
		return "", errors.New("preview/create-only runs are not import performance evidence")
	}
	if !a.Summary.Success || !b.Summary.Success || a.Summary.Cancelled || b.Summary.Cancelled {
		return "", errors.New("only successful complete runs are comparable")
	}
	if am.Settings != bm.Settings || am.ScopeFingerprint != bm.ScopeFingerprint || am.KeyID != bm.KeyID || am.TargetID != bm.TargetID || am.GOMAXPROCS != bm.GOMAXPROCS || am.GOMEMLIMIT != bm.GOMEMLIMIT || am.OS != bm.OS || am.Arch != bm.Arch || am.Strategy != bm.Strategy {
		return "", errors.New("comparison requires identical settings, resource budget, target and scope identity")
	}
	if a.Summary.Counters.ConsumedRows != b.Summary.Counters.ConsumedRows || a.Summary.WallNS <= 0 || b.Summary.WallNS <= 0 {
		return "", errors.New("invalid duration or unequal consumed rows")
	}
	ratio := float64(a.Summary.WallNS) / float64(b.Summary.WallNS)
	return fmt.Sprintf("# A/B comparison\n\n|Run|Commit|Wall seconds|Consumed rows|\n|---|---|---:|---:|\n|A|%s|%.3f|%d|\n|B|%s|%.3f|%d|\n\nSpeedup: %.4fx; duration reduction: %.2f%%.\n\nSingle pair is a clue, not proof beyond measurement variation. Confirm identical immutable input, indexes, initial target state, no external writes/load and run order manually. Repeat alternating A/B at least three times where feasible. Metadata fingerprints do not prove file-content identity. No GoldenDB or full-run acceptance is inferred.\n", am.Commit, float64(a.Summary.WallNS)/1e9, a.Summary.Counters.ConsumedRows, bm.Commit, float64(b.Summary.WallNS)/1e9, b.Summary.Counters.ConsumedRows, ratio, (1-1/ratio)*100), nil
}
