package csvsample

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractKeepsQuotedNewlinesAndLeavesSourceUnchanged(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "source.csv")
	original := "id,value\n1,\"line1\nline2\"\n2,plain\n3,tail\n"
	if err := os.WriteFile(src, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "sample.csv")
	n, err := Extract(src, dest, 2, true)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("copied %d records, want 2", n)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "line1\nline2") || strings.Contains(string(got), "tail") {
		t.Fatalf("logical records not preserved: %q", got)
	}
	srcGot, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if string(srcGot) != original {
		t.Fatal("source file was modified")
	}
}

func TestExtractRefusesOverwriteAndPhysicalSplitAssumptions(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "source.csv")
	if err := os.WriteFile(src, []byte("a,b\n1,2\n"), 0600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "out.csv")
	if _, err := Extract(src, dest, 1, true); err != nil {
		t.Fatal(err)
	}
	if _, err := Extract(src, dest, 1, true); err == nil {
		t.Fatal("overwrite accepted")
	}
	if _, err := Extract(src, src, 1, true); err == nil {
		t.Fatal("in-place extract accepted")
	}
}
