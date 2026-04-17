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

	// 记录哪些列会被转为 TEXT（用于后续跳过索引）
	textColumns := make(map[string]bool)

	// 列定义
	for i, column := range tableDDL.Columns {
		columnDef, err := tc.convertColumnWithTextCheck(column, textColumns)
		if err != nil {
			return "", fmt.Errorf("failed to convert column %s: %w", column.Name, err)
		}
		ddl.WriteString("  ")
		ddl.WriteString(columnDef)
		// 如果不是最后一列且（主键不存在 或 主键会被添加），添加逗号
		if i < len(tableDDL.Columns)-1 {
			ddl.WriteString(",\n")
		} else {
			// 最后一列，检查是否需要添加主键
			if tableDDL.PrimaryKey != nil {
				hasTextInPK := false
				for _, col := range tableDDL.PrimaryKey.Columns {
					if textColumns[col] {
						hasTextInPK = true
						break
					}
				}
				if !hasTextInPK {
					ddl.WriteString(",\n")
					ddl.WriteString(fmt.Sprintf("  PRIMARY KEY (`%s`)\n",
						strings.Join(tableDDL.PrimaryKey.Columns, "`, `")))
				} else {
					ddl.WriteString("\n")
				}
			} else {
				ddl.WriteString("\n")
			}
		}
	}

	// 如果主键被跳过（包含 TEXT 列），不添加主键
	// （已经在上面处理了）

	ddl.WriteString(") ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 ROW_FORMAT=COMPRESSED KEY_BLOCK_SIZE=8;\n")

	// 索引 - 跳过包含 TEXT 列的索引
	for _, index := range tableDDL.Indexes {
		indexDDL := tc.convertIndex(tableDDL.TableName, index, textColumns)
		if indexDDL != "" {
			ddl.WriteString("\n")
			ddl.WriteString(indexDDL)
		}
	}

	return ddl.String(), nil
}

// convertColumnWithTextCheck 转换列定义并记录 TEXT 类型
func (tc *TableConverter) convertColumnWithTextCheck(column parser.ColumnDef, textColumns map[string]bool) (string, error) {
	// 移除 COLLATE
	cleanType := tc.typeMapper.CleanCollation(column.Type)

	// 提取类型部分（移除 NULL/NOT NULL）
	typePart := tc.extractType(cleanType)

	// 映射类型
	mysqlType, err := tc.typeMapper.MapType(typePart)
	if err != nil {
		return "", err
	}

	// 记录是否被转为 TEXT 类型
	if mysqlType == "text" || mysqlType == "longtext" {
		textColumns[column.Name] = true
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
func (tc *TableConverter) convertIndex(tableName string, index parser.IndexDef, textColumns map[string]bool) string {
	// 跳过包含 TEXT 列的索引
	for _, col := range index.Columns {
		if textColumns[col] {
			return ""
		}
	}

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

// isTextColumn 检查列是否可能为 TEXT 类型
func (tc *TableConverter) isTextColumn(columnName string) bool {
	textPrefixes := []string{"trigger_name", "trigger_group", "job_name", "job_group",
		"corp_code", "department_path", "department_code"}
	for _, prefix := range textPrefixes {
		if strings.EqualFold(columnName, prefix) {
			return true
		}
	}
	return false
}
