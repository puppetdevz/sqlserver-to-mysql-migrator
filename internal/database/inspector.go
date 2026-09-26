package database

import (
	"fmt"

	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/logger"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/matcher"
)

// Inspector 数据库检查器
type Inspector struct {
	conn         *Connection
	tableMatcher matcher.TableNameMatcher
}

// NewInspector 创建数据库检查器
func NewInspector(conn *Connection, tableMatcher matcher.TableNameMatcher) *Inspector {
	return &Inspector{
		conn:         conn,
		tableMatcher: tableMatcher,
	}
}

// TableClassification 表分类结果
type TableClassification struct {
	ExistingTables    []string        // 已存在的表
	MissingTables     []string        // 缺失的表
	ExistingTablesMap map[string]bool // 已存在表的快速查找映射
}

// ClassifyTables 分类表（已存在 vs 缺失）
func (i *Inspector) ClassifyTables(allTableNames []string) (*TableClassification, error) {
	// 获取目标数据库中已存在的表
	tables, err := i.conn.GetTableNames()
	if err != nil {
		return nil, fmt.Errorf("failed to get existing tables: %w", err)
	}

	classification := classifyTableNames(allTableNames, tables, i.tableMatcher)

	logger.Infof("Table classification: %d existing, %d missing, %d total",
		len(classification.ExistingTables), len(classification.MissingTables), len(allTableNames))

	return classification, nil
}

func classifyTableNames(allTableNames, existingTables []string, tableMatcher matcher.TableNameMatcher) *TableClassification {
	existingTablesMap := make(map[string]bool)
	for _, table := range existingTables {
		key := tableMatcher.Key(table)
		if existingTablesMap[key] {
			logger.Warnf("Table name conflict under current case-sensitivity setting: %s", table)
			continue
		}
		existingTablesMap[key] = true
	}

	var existing []string
	var missing []string
	for _, tableName := range allTableNames {
		if existingTablesMap[tableMatcher.Key(tableName)] {
			existing = append(existing, tableName)
		} else {
			missing = append(missing, tableName)
		}
	}

	return &TableClassification{
		ExistingTables:    existing,
		MissingTables:     missing,
		ExistingTablesMap: existingTablesMap,
	}
}

// GetTableStructure 获取表结构信息
func (i *Inspector) GetTableStructure(tableName string) ([]ColumnInfo, error) {
	query := fmt.Sprintf("DESCRIBE %s", matcher.QuoteIdent(tableName))
	rows, err := i.conn.DB.Query(query)
	if err != nil {
		return nil, fmt.Errorf("failed to describe table %s: %w", tableName, err)
	}
	defer rows.Close()

	var columns []ColumnInfo
	for rows.Next() {
		var col ColumnInfo
		var null, key, extra string
		var defaultVal *string

		err := rows.Scan(&col.Field, &col.Type, &null, &key, &defaultVal, &extra)
		if err != nil {
			return nil, fmt.Errorf("failed to scan column info: %w", err)
		}

		col.Null = (null == "YES")
		col.Key = key
		col.Extra = extra
		if defaultVal != nil {
			col.Default = *defaultVal
		}

		columns = append(columns, col)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating columns: %w", err)
	}

	return columns, nil
}

// ColumnInfo 列信息
type ColumnInfo struct {
	Field   string
	Type    string
	Null    bool
	Key     string
	Default string
	Extra   string
}
