package converter

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/config"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/parser"
)

// 包级别正则已移至 type_mapper.go（reNvarchar, reVarchar, reTrimNullable）
// 阈值常量也在 type_mapper.go（VarcharThreshold=256, NvarcharThreshold=192）

// TableConverter 表结构转换器
type TableConverter struct {
	typeMapper *TypeMapper
	config     config.ConverterConfig
}

// NewTableConverter 创建表转换器
func NewTableConverter(cfg config.ConverterConfig) *TableConverter {
	return &TableConverter{
		typeMapper: NewTypeMapper(),
		config:     cfg,
	}
}

// ConvertToMySQL 将 SQL Server 表定义转换为 MySQL DDL
func (tc *TableConverter) ConvertToMySQL(tableDDL *parser.TableDDL) (string, error) {
	var ddl strings.Builder

	// CREATE TABLE
	ddl.WriteString(fmt.Sprintf("CREATE TABLE `%s` (\n", tableDDL.TableName))

	// 记录哪些列会被转为 TEXT（用于后续跳过索引）
	textColumns := make(map[string]bool)

	// 统计 nvarchar(>192) 和 varchar(>256) 列
	var largeNvarcharCols []string
	var largeVarcharCols []string

	for _, column := range tableDDL.Columns {
		cleanType := tc.typeMapper.CleanCollation(column.Type)
		typePart := tc.extractType(cleanType)
		sqlType := strings.TrimSpace(strings.ToLower(typePart))

		// NVARCHAR(n)，n > 192
		if matches := reNvarchar.FindStringSubmatch(sqlType); len(matches) > 0 {
			if n, _ := strconv.Atoi(matches[1]); n > 192 {
				largeNvarcharCols = append(largeNvarcharCols, column.Name)
			}
			continue
		}

		// VARCHAR(n)，n > 256
		if matches := reVarchar.FindStringSubmatch(sqlType); len(matches) > 0 {
			if n, _ := strconv.Atoi(matches[1]); n > 256 {
				largeVarcharCols = append(largeVarcharCols, column.Name)
			}
			continue
		}
	}

	// 与阈值比较，判断是否触发转换
	shouldConvertNvarchar := len(largeNvarcharCols) > tc.config.IsEffectiveMaxNvarcharToTextColumns()
	shouldConvertVarchar := len(largeVarcharCols) > tc.config.IsEffectiveMaxVarcharToTextColumns()

	// 构建强转列 map（仅在触发转换条件时迭代）
	forceTextColumns := make(map[string]bool)
	if shouldConvertNvarchar {
		for _, col := range largeNvarcharCols {
			forceTextColumns[col] = true
		}
	}
	if shouldConvertVarchar {
		for _, col := range largeVarcharCols {
			forceTextColumns[col] = true
		}
	}

	// 列定义
	for i, column := range tableDDL.Columns {
		columnDef, err := tc.convertColumnWithTextCheck(column, textColumns, forceTextColumns)
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

	ddl.WriteString(") ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 ROW_FORMAT=DYNAMIC;\n")

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
func (tc *TableConverter) convertColumnWithTextCheck(
	column parser.ColumnDef,
	textColumns map[string]bool,
	forceTextColumns map[string]bool,
) (string, error) {
	// 如果列在 forceTextColumns 中，强制转为 TEXT
	if forceTextColumns[column.Name] {
		textColumns[column.Name] = true
		var def strings.Builder
		def.WriteString(fmt.Sprintf("`%s` text", column.Name))
		if !column.Nullable {
			def.WriteString(" NOT NULL")
		} else {
			def.WriteString(" NULL")
		}
		return def.String(), nil
	}

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
	typeDef = reTrimNullable.ReplaceAllString(typeDef, "")
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
