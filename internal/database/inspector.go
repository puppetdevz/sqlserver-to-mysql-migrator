package database

import (
	"fmt"
	"strings"

	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/logger"
)

// Inspector 数据库检查器
type Inspector struct {
	conn *Connection
}

// NewInspector 创建数据库检查器
func NewInspector(conn *Connection) *Inspector {
	return &Inspector{
		conn: conn,
	}
}

// TableClassification 表分类结果
type TableClassification struct {
	ExistingTables   []string         // 已存在的表
	MissingTables    []string         // 缺失的表
	ExistingTablesMap map[string]bool // 已存在表的快速查找映射
}

// ClassifyTables 分类表（已存在 vs 缺失）
func (i *Inspector) ClassifyTables(allTableNames []string) (*TableClassification, error) {
	// 获取目标数据库中已存在的表
	existingTablesMap, err := i.getExistingTablesMap()
	if err != nil {
		return nil, fmt.Errorf("failed to get existing tables: %w", err)
	}

	var existing []string
	var missing []string

	for _, tableName := range allTableNames {
		// 转换为大写进行比较（不区分大小写）
		upperTableName := strings.ToUpper(tableName)
		if existingTablesMap[upperTableName] {
			existing = append(existing, tableName)
		} else {
			missing = append(missing, tableName)
		}
	}

	logger.Infof("Table classification: %d existing, %d missing, %d total",
		len(existing), len(missing), len(allTableNames))

	return &TableClassification{
		ExistingTables:    existing,
		MissingTables:     missing,
		ExistingTablesMap: existingTablesMap,
	}, nil
}

// getExistingTablesMap 获取已存在表的映射（大写表名 -> true）
func (i *Inspector) getExistingTablesMap() (map[string]bool, error) {
	tables, err := i.conn.GetTableNames()
	if err != nil {
		return nil, err
	}

	tableMap := make(map[string]bool)
	for _, table := range tables {
		// 使用大写作为 key，实现不区分大小写的查找
		tableMap[strings.ToUpper(table)] = true
	}

	return tableMap, nil
}

// GetTableStructure 获取表结构信息
func (i *Inspector) GetTableStructure(tableName string) ([]ColumnInfo, error) {
	query := fmt.Sprintf("DESCRIBE `%s`", tableName)
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
