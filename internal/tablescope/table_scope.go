package tablescope

import (
	"fmt"
	"os"
	"strings"

	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/matcher"
)

const (
	ReasonNotFoundInDDL     = "not found in DDL definitions"
	ReasonMatchedSkipTables = "matched skip_tables"
	ReasonMatchedCompleted  = "matched completed_tables.txt"
)

type Scope struct {
	Enabled  bool
	Tables   []string
	Source   string
	Reimport bool
}

type Skip struct {
	Table  string
	Reason string
}

type Result struct {
	Tables  []string
	Skipped []Skip
}

func Resolve(tablesFlag string, reimport bool, reimportTableFile string) (Scope, error) {
	if !reimport {
		if strings.TrimSpace(tablesFlag) == "" {
			return Scope{}, nil
		}
		return Scope{
			Enabled:  true,
			Tables:   splitCommaTableNames(tablesFlag),
			Source:   "--tables",
			Reimport: false,
		}, nil
	}

	if strings.TrimSpace(tablesFlag) != "" {
		return Scope{}, fmt.Errorf("--tables cannot be used together with --reimport-tables; use --reimport-table-file for reimport scope")
	}
	if strings.TrimSpace(reimportTableFile) == "" {
		return Scope{}, fmt.Errorf("--reimport-table-file is required when --reimport-tables is enabled")
	}

	tableNames, err := LoadTXTFile(reimportTableFile)
	if err != nil {
		return Scope{}, err
	}
	return Scope{
		Enabled:  true,
		Tables:   tableNames,
		Source:   fmt.Sprintf("--reimport-table-file %s", reimportTableFile),
		Reimport: true,
	}, nil
}

func LoadTXTFile(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read table list file %s: %w", path, err)
	}

	lines := strings.Split(string(data), "\n")
	tableNames := make([]string, 0, len(lines))
	for _, line := range lines {
		tableName := strings.TrimSpace(line)
		if tableName == "" {
			continue
		}
		tableNames = append(tableNames, tableName)
	}
	if len(tableNames) == 0 {
		return nil, fmt.Errorf("table list file %s does not contain any table names", path)
	}
	return tableNames, nil
}

func Apply(allTables []string, scope Scope, skipTables, completedTables []string, tableMatcher matcher.TableNameMatcher) Result {
	ddlTablesByKey := make(map[string]string, len(allTables))
	for _, tableName := range allTables {
		ddlTablesByKey[tableMatcher.Key(tableName)] = tableName
	}

	requestedSeen := make(map[string]struct{}, len(scope.Tables))
	requestedInDDL := make([]string, 0, len(scope.Tables))
	var skipped []Skip
	for _, requested := range scope.Tables {
		key := tableMatcher.Key(requested)
		if _, ok := requestedSeen[key]; ok {
			continue
		}
		requestedSeen[key] = struct{}{}

		if actual, ok := ddlTablesByKey[key]; ok {
			requestedInDDL = append(requestedInDDL, actual)
		} else {
			skipped = append(skipped, Skip{
				Table:  requested,
				Reason: ReasonNotFoundInDDL,
			})
		}
	}

	kept, skippedByConfig := excludeTablesWithReason(requestedInDDL, skipTables, tableMatcher, ReasonMatchedSkipTables)
	skipped = append(skipped, skippedByConfig...)

	kept, skippedByCompleted := excludeTablesWithReason(kept, completedTables, tableMatcher, ReasonMatchedCompleted)
	skipped = append(skipped, skippedByCompleted...)

	return Result{Tables: kept, Skipped: skipped}
}

func splitCommaTableNames(value string) []string {
	parts := strings.Split(value, ",")
	tableNames := make([]string, 0, len(parts))
	for _, part := range parts {
		tableName := strings.TrimSpace(part)
		if tableName == "" {
			continue
		}
		tableNames = append(tableNames, tableName)
	}
	return tableNames
}

func ExcludeTables(allTables, excludeList []string, tableMatcher matcher.TableNameMatcher) []string {
	kept, _ := excludeTablesWithReason(allTables, excludeList, tableMatcher, "")
	return kept
}

func excludeTablesWithReason(allTables, excludeList []string, tableMatcher matcher.TableNameMatcher, reason string) ([]string, []Skip) {
	excludeSet := tableMatcher.BuildSet(excludeList)
	kept := make([]string, 0, len(allTables))
	var skipped []Skip

	for _, tableName := range allTables {
		if _, ok := excludeSet[tableMatcher.Key(tableName)]; ok {
			skipped = append(skipped, Skip{
				Table:  tableName,
				Reason: reason,
			})
			continue
		}
		kept = append(kept, tableName)
	}

	return kept, skipped
}
