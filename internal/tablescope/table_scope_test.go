package tablescope

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/matcher"
)

func TestLoadTXTFileTrimsBlankLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tables.txt")
	if err := os.WriteFile(path, []byte(" WF_CASE_RUN \n\nCAP_FORM_DATA_HISTORY\n"), 0644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	got, err := LoadTXTFile(path)
	if err != nil {
		t.Fatalf("LoadTXTFile() error = %v", err)
	}

	want := []string{"WF_CASE_RUN", "CAP_FORM_DATA_HISTORY"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("LoadTXTFile() = %#v, want %#v", got, want)
	}
}

func TestApplyReportsSkipAndCompletedReasonsForSelectedTables(t *testing.T) {
	allTables := []string{"KEEP_ME", "SKIP_ME", "DONE_ME", "OTHER"}
	scope := Scope{
		Enabled: true,
		Tables:  []string{"keep_me", "skip_me", "done_me", "missing_me"},
		Source:  "--reimport-table-file tables.txt",
	}
	tableMatcher := matcher.NewTableNameMatcher(false)

	got := Apply(
		allTables,
		scope,
		[]string{"skip_me"},
		[]string{"done_me"},
		tableMatcher,
	)

	if want := []string{"KEEP_ME"}; !reflect.DeepEqual(got.Tables, want) {
		t.Fatalf("Tables = %#v, want %#v", got.Tables, want)
	}

	wantSkipped := []Skip{
		{Table: "missing_me", Reason: "not found in DDL definitions"},
		{Table: "SKIP_ME", Reason: "matched skip_tables"},
		{Table: "DONE_ME", Reason: "matched completed_tables.txt"},
	}
	if !reflect.DeepEqual(got.Skipped, wantSkipped) {
		t.Fatalf("Skipped = %#v, want %#v", got.Skipped, wantSkipped)
	}
}

func TestResolveRequiresReimportFileWhenEnabled(t *testing.T) {
	_, err := Resolve("", true, "")
	if err == nil {
		t.Fatal("Resolve() error = nil, want error")
	}
}
