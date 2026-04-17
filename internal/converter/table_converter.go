package converter

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/parser"
)

// TableConverter 表结构转换器
type TableConverter struct {
	typeMapper *TypeMapper
}

// NewTableConverter 创建表转换器
func NewTableConverter() *TableConverter {
	return &TableConverter{
		typeMapper: NewTypeMapper(),
	}
}

// ConvertToMySQL 将 SQL Server 表定义转换为 MySQL DDL
func (tc *TableConverter) ConvertToMySQL(tableDDL *parser.TableDDL) (string, error) {
	var ddl strings.Builder

	// CREATE TABLE
	ddl.WriteString(fmt.Sprintf("CREATE TABLE `%s` (\n", tableDDL.TableName))

	// 列定义
	for i, column := range tableDDL.Columns {
		columnDef, err := tc.convertColumn(column)
		if err != nil {
			return "", fmt.Errorf("failed to convert column %s: %w", column.Name, err)
		}
		ddl.WriteString("  ")
		ddl.WriteString(columnDef)
		if i < len(tableDDL.Columns)-1 || tableDDL.PrimaryKey != nil {
			ddl.WriteString(",")
		}
		ddl.WriteString("\n")
	}

	// 主键
	if tableDDL.PrimaryKey != nil {
		ddl.WriteString(fmt.Sprintf("  PRIMARY KEY (`%s`)\n",
			strings.Join(tableDDL.PrimaryKey.Columns, "`, `")))
	}

	ddl.WriteString(") ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;\n")

	// 索引
	for _, index := range tableDDL.Indexes {
		indexDDL := tc.convertIndex(tableDDL.TableName, index)
		ddl.WriteString("\n")
		ddl.WriteString(indexDDL)
	}

	return ddl.String(), nil
}

// convertColumn 转换列定义
func (tc *TableConverter) convertColumn(column parser.ColumnDef) (string, error) {
	// 移除 COLLATE
	cleanType := tc.typeMapper.CleanCollation(column.Type)

	// 提取类型部分（移除 NULL/NOT NULL）
	typePart := tc.extractType(cleanType)

	// 映射类型
	mysqlType, err := tc.typeMapper.MapType(typePart)
	if err != nil {
		return "", err
	}

	// 构建列定义
	var def strings.Builder
	def.WriteString(fmt.Sprintf("`%s` %s", column.Name, mysqlType))

	// NULL/NOT NULL
	if !column.Nullable {
		def.WriteString(" NOT NULL")
	} else {
		def.WriteString(" NULL")
	}

	return def.String(), nil
}

// extractType 提取类型部分
func (tc *TableConverter) extractType(typeDef string) string {
	// 移除 NULL/NOT NULL
	typeDef = strings.TrimSpace(typeDef)
	typeDef = regexp.MustCompile(`\s+(NOT\s+)?NULL$`).ReplaceAllString(typeDef, "")
	return strings.TrimSpace(typeDef)
}

// convertIndex 转换索引定义
func (tc *TableConverter) convertIndex(tableName string, index parser.IndexDef) string {
	indexType := "INDEX"
	if index.Unique {
		indexType = "UNIQUE INDEX"
	}

	columns := make([]string, len(index.Columns))
	for i, col := range index.Columns {
		columns[i] = fmt.Sprintf("`%s`", col)
	}

	return fmt.Sprintf("CREATE %s `%s` ON `%s` (%s);",
		indexType, index.Name, tableName, strings.Join(columns, ", "))
}
