package converter

import (
	"fmt"
	"regexp"
	"strings"
)

// 包级别预编译正则表达式（避免循环内重复编译）
var (
	reNvarchar     = regexp.MustCompile(`nvarchar\((\d+)\)`)
	reVarchar      = regexp.MustCompile(`varchar\((\d+|max)\)`)
	reNchar        = regexp.MustCompile(`nchar\((\d+)\)`)
	reChar         = regexp.MustCompile(`char\((\d+)\)`)
	reNumeric      = regexp.MustCompile(`numeric\((\d+),(\d+)\)`)
	reDecimal      = regexp.MustCompile(`decimal\((\d+),(\d+)\)`)
	reCollate      = regexp.MustCompile(`\s+COLLATE\s+\w+`)
	reDefault      = regexp.MustCompile(`\s+DEFAULT\s+\S+`)
	reTrimNullable = regexp.MustCompile(`\s+(NOT\s+)?NULL$`)
)

// 阈值常量
const (
	VarcharThreshold  = 256
	NvarcharThreshold = 192
)

// TypeMapper SQL Server 到 MySQL 类型映射器
type TypeMapper struct {
	typeMap map[string]string
}

// NewTypeMapper 创建类型映射器
func NewTypeMapper() *TypeMapper {
	return &TypeMapper{
		typeMap: map[string]string{
			"bigint":           "bigint",
			"int":              "int",
			"smallint":         "smallint",
			"tinyint":          "tinyint",
			"bit":              "tinyint(1)",
			"datetime":         "datetime",
			"date":             "date",
			"time":             "time",
			"ntext":            "longtext",
			"text":             "text",
			"image":            "longblob",
			"varbinary":        "varbinary",
			"uniqueidentifier": "char(36)",
			"money":            "decimal(19,4)",
			"smallmoney":       "decimal(10,4)",
		},
	}
}

// MapType 映射 SQL Server 类型到 MySQL 类型
func (tm *TypeMapper) MapType(sqlServerType string) (string, error) {
	sqlServerType = strings.TrimSpace(strings.ToLower(sqlServerType))

	if sqlServerType == "nvarchar(max)" {
		return "longtext", nil
	}

	// nvarchar(n) -> varchar(n)
	if strings.HasPrefix(sqlServerType, "nvarchar") {
		if matches := reNvarchar.FindStringSubmatch(sqlServerType); len(matches) > 0 {
			return fmt.Sprintf("varchar(%s)", matches[1]), nil
		}
	}

	// varchar(n) -> varchar(n)
	if strings.HasPrefix(sqlServerType, "varchar") {
		if reVarchar.MatchString(sqlServerType) {
			return sqlServerType, nil
		}
	}

	// nchar(n) -> char(n)
	if strings.HasPrefix(sqlServerType, "nchar") {
		if matches := reNchar.FindStringSubmatch(sqlServerType); len(matches) > 0 {
			return fmt.Sprintf("char(%s)", matches[1]), nil
		}
	}

	// char(n) -> char(n)
	if strings.HasPrefix(sqlServerType, "char") {
		if reChar.MatchString(sqlServerType) {
			return sqlServerType, nil
		}
	}

	// numeric(p,s) -> decimal(p,s)
	if strings.HasPrefix(sqlServerType, "numeric") {
		if matches := reNumeric.FindStringSubmatch(sqlServerType); len(matches) > 0 {
			return fmt.Sprintf("decimal(%s,%s)", matches[1], matches[2]), nil
		}
		if sqlServerType == "numeric" {
			return "decimal(10,0)", nil
		}
	}

	// decimal(p,s) -> decimal(p,s)
	if strings.HasPrefix(sqlServerType, "decimal") {
		if reDecimal.MatchString(sqlServerType) {
			return sqlServerType, nil
		}
		if sqlServerType == "decimal" {
			return "decimal(10,0)", nil
		}
	}

	if sqlServerType == "float" {
		return "double", nil
	}
	if sqlServerType == "real" {
		return "float", nil
	}

	if mysqlType, ok := tm.typeMap[sqlServerType]; ok {
		return mysqlType, nil
	}

	return "", fmt.Errorf("unsupported SQL Server type: %s", sqlServerType)
}

// CleanCollation 移除 COLLATE 子句和 DEFAULT 值
func (tm *TypeMapper) CleanCollation(columnDef string) string {
	columnDef = reCollate.ReplaceAllString(columnDef, "")
	columnDef = reDefault.ReplaceAllString(columnDef, "")
	return columnDef
}
