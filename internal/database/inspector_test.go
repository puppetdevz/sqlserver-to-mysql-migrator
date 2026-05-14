package database

import (
	"reflect"
	"testing"

	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/matcher"
)

func TestClassifyTableNamesCaseSensitive(t *testing.T) {
	classification := classifyTableNames(
		[]string{"SAMPLE_MAIN_102"},
		[]string{"sample_main_102"},
		matcher.NewTableNameMatcher(true),
	)

	if len(classification.ExistingTables) != 0 {
		t.Fatalf("ExistingTables = %v, want empty", classification.ExistingTables)
	}
	if !reflect.DeepEqual(classification.MissingTables, []string{"SAMPLE_MAIN_102"}) {
		t.Fatalf("MissingTables = %v, want [SAMPLE_MAIN_102]", classification.MissingTables)
	}
}

func TestClassifyTableNamesCaseInsensitive(t *testing.T) {
	classification := classifyTableNames(
		[]string{"SAMPLE_MAIN_102"},
		[]string{"sample_main_102"},
		matcher.NewTableNameMatcher(false),
	)

	if !reflect.DeepEqual(classification.ExistingTables, []string{"SAMPLE_MAIN_102"}) {
		t.Fatalf("ExistingTables = %v, want [SAMPLE_MAIN_102]", classification.ExistingTables)
	}
	if len(classification.MissingTables) != 0 {
		t.Fatalf("MissingTables = %v, want empty", classification.MissingTables)
	}
}
