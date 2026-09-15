package converter

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/config"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/matcher"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/parser"
)

// 包级正则和 varchar/nvarchar 阈值常量定义在 type_mapper.go 中
// textInlineOverheadBytes 是 MySQL ROW_FORMAT=DYNAMIC 下 TEXT/BLOB 列的行内前缀大小，
// 列被转为 TEXT 后仅占用此字节数，其余数据存储在溢出页中
const textInlineOverheadBytes = 20

// ConvertMode describes the generated DDL conversion mode.
type ConvertMode string

const (
	ConvertModeNormal ConvertMode = "normal"
)

// ConvertOptions is reserved for future conversion options.
type ConvertOptions struct{}

// ConvertResult is the structured DDL conversion output used by migration code.
type ConvertResult struct {
	SQL               string
	Statements        []string
	Mode              ConvertMode
	EstimatedRowBytes int
}

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
	result, err := tc.ConvertToMySQLResult(tableDDL, ConvertOptions{})
	if err != nil {
		return "", err
	}
	return result.SQL, nil
}

// ConvertToMySQLResult converts SQL Server table DDL to MySQL and returns diagnostics.
func (tc *TableConverter) ConvertToMySQLResult(tableDDL *parser.TableDDL, _ ConvertOptions) (ConvertResult, error) {
	var ddl strings.Builder
	result := ConvertResult{Mode: ConvertModeNormal}

	// CREATE TABLE
	ddl.WriteString("CREATE TABLE ")
	ddl.WriteString(matcher.QuoteIdent(tableDDL.TableName))
	ddl.WriteString(" (\n")

	// 记录哪些列会被转为 TEXT（用于后续跳过索引）
	textColumns := make(map[string]bool)

	// 统计 nvarchar(>192) 和 varchar(>256) 列
	var largeNvarcharCols []string
	var largeVarcharCols []string

	forceTextColumns := make(map[string]bool)

	for _, column := range tableDDL.Columns {
		cleanType := tc.typeMapper.CleanCollation(column.Type)
		typePart := tc.extractType(cleanType)
		sqlType := strings.TrimSpace(strings.ToLower(typePart))

		// NVARCHAR(n)，n > 192
		if matches := reNvarchar.FindStringSubmatch(sqlType); len(matches) > 0 {
			n, _ := strconv.Atoi(matches[1])
			if n >= tc.config.IsEffectiveMaxNvarcharToTextSize() {
				forceTextColumns[column.Name] = true
			}
			if n > 192 {
				largeNvarcharCols = append(largeNvarcharCols, column.Name)
			}
			continue
		}

		// VARCHAR(n)，n > 256
		if matches := reVarchar.FindStringSubmatch(sqlType); len(matches) > 0 {
			n, _ := strconv.Atoi(matches[1])
			if n >= tc.config.IsEffectiveMaxVarcharToTextSize() {
				forceTextColumns[column.Name] = true
			}
			if n > 256 {
				largeVarcharCols = append(largeVarcharCols, column.Name)
			}
			continue
		}
	}

	// 与阈值比较，判断是否触发转换
	shouldConvertNvarchar := len(largeNvarcharCols) > tc.config.IsEffectiveMaxNvarcharToTextColumns()
	shouldConvertVarchar := len(largeVarcharCols) > tc.config.IsEffectiveMaxVarcharToTextColumns()

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

	initialEstimate := tc.estimateRowBytes(tableDDL, forceTextColumns)
	result.EstimatedRowBytes = initialEstimate

	// 列定义
	for i, column := range tableDDL.Columns {
		columnDef, err := tc.convertColumnWithTextCheck(column, textColumns, forceTextColumns)
		if err != nil {
			return ConvertResult{}, fmt.Errorf("failed to convert column %s: %w", column.Name, err)
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
					ddl.WriteString("  PRIMARY KEY (")
					for i, col := range tableDDL.PrimaryKey.Columns {
						if i > 0 {
							ddl.WriteString(", ")
						}
						ddl.WriteString(matcher.QuoteIdent(col))
					}
					ddl.WriteString(")\n")
				} else {
					ddl.WriteString("\n")
				}
			} else {
				ddl.WriteString("\n")
			}
		}
	}

	ddl.WriteString(") ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 ROW_FORMAT=DYNAMIC")
	createSQL := ddl.String()
	result.Statements = append(result.Statements, createSQL)

	// 索引 - 跳过包含 TEXT 列的索引
	for _, index := range tableDDL.Indexes {
		indexDDL := tc.convertIndex(tableDDL.TableName, index, textColumns)
		if indexDDL != "" {
			result.Statements = append(result.Statements, strings.TrimSuffix(indexDDL, ";"))
		}
	}

	result.SQL = strings.Join(result.Statements, ";\n") + ";\n"
	return result, nil
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
		def.WriteString(matcher.QuoteIdent(column.Name))
		def.WriteString(" text")
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
	def.WriteString(matcher.QuoteIdent(column.Name))
	def.WriteString(" ")
	def.WriteString(mysqlType)

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

func (tc *TableConverter) estimatedInlineBytes(column parser.ColumnDef) (int, bool) {
	cleanType := tc.typeMapper.CleanCollation(column.Type)
	typePart := tc.extractType(cleanType)
	sqlType := strings.TrimSpace(strings.ToLower(typePart))

	if matches := reNvarchar.FindStringSubmatch(sqlType); len(matches) > 0 {
		n, _ := strconv.Atoi(matches[1])
		return n * 4, true
	}
	if matches := reVarchar.FindStringSubmatch(sqlType); len(matches) > 0 {
		if matches[1] == "max" {
			return textInlineOverheadBytes, false
		}
		n, _ := strconv.Atoi(matches[1])
		return n * 4, true
	}
	if matches := reNchar.FindStringSubmatch(sqlType); len(matches) > 0 {
		n, _ := strconv.Atoi(matches[1])
		return n * 4, true
	}
	if matches := reChar.FindStringSubmatch(sqlType); len(matches) > 0 {
		n, _ := strconv.Atoi(matches[1])
		return n * 4, true
	}

	switch {
	case strings.HasPrefix(sqlType, "bigint"):
		return 8, false
	case strings.HasPrefix(sqlType, "int"):
		return 4, false
	case strings.HasPrefix(sqlType, "smallint"):
		return 2, false
	case strings.HasPrefix(sqlType, "tinyint"), strings.HasPrefix(sqlType, "bit"):
		return 1, false
	case strings.HasPrefix(sqlType, "datetime"):
		return 8, false
	case strings.HasPrefix(sqlType, "date"), strings.HasPrefix(sqlType, "time"):
		return 3, false
	case strings.HasPrefix(sqlType, "numeric"), strings.HasPrefix(sqlType, "decimal"), strings.HasPrefix(sqlType, "money"):
		return 16, false
	case strings.HasPrefix(sqlType, "float"):
		return 8, false
	case strings.HasPrefix(sqlType, "real"):
		return 4, false
	case strings.Contains(sqlType, "text"), strings.Contains(sqlType, "image"), strings.Contains(sqlType, "binary"):
		return textInlineOverheadBytes, false
	default:
		return 16, false
	}
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
		columns[i] = matcher.QuoteIdent(col)
	}

	return fmt.Sprintf("CREATE %s %s ON %s (%s);",
		indexType, matcher.QuoteIdent(index.Name), matcher.QuoteIdent(tableName), strings.Join(columns, ", "))
}
