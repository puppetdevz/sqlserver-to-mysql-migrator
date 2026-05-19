package converter

import (
	"fmt"
	"regexp"
	"strings"
)

// TypeMapper SQL Server 到 MySQL 类型映射器
type TypeMapper struct {
	typeMap map[string]string
}

// NewTypeMapper 创建类型映射器
func NewTypeMapper() *TypeMapper {
	return &TypeMapper{
		typeMap: map[string]string{
			"bigint":    "bigint",
			"int":       "int",
			"smallint":  "smallint",
			"tinyint":   "tinyint",
			"bit":       "tinyint(1)",
			"datetime":  "datetime",
			"date":      "date",
			"time":      "time",
			"ntext":     "longtext",
			"text":      "text",
			"image":     "longblob",
			"varbinary": "varbinary",
		},
	}
}

// MapType 映射 SQL Server 类型到 MySQL 类型
func (tm *TypeMapper) MapType(sqlServerType string) (string, error) {
	// 移除空格并转为小写
	sqlServerType = strings.TrimSpace(strings.ToLower(sqlServerType))

	// nvarchar(max) -> longtext
	if sqlServerType == "nvarchar(max)" {
		return "longtext", nil
	}

	// nvarchar(n) -> varchar(n)
	if strings.HasPrefix(sqlServerType, "nvarchar") {
		re := regexp.MustCompile(`nvarchar\((\d+)\)`)
		if re.MatchString(sqlServerType) {
			size := re.FindStringSubmatch(sqlServerType)[1]
			return fmt.Sprintf("varchar(%s)", size), nil
		}
	}

	// varchar(n) -> varchar(n)（保留原样）
	if strings.HasPrefix(sqlServerType, "varchar") {
		re := regexp.MustCompile(`varchar\((\d+|max)\)`)
		if re.MatchString(sqlServerType) {
			return sqlServerType, nil
		}
	}

	// nchar(n) -> char(n)
	if strings.HasPrefix(sqlServerType, "nchar") {
		re := regexp.MustCompile(`nchar\((\d+)\)`)
		if re.MatchString(sqlServerType) {
			size := re.FindStringSubmatch(sqlServerType)[1]
			return fmt.Sprintf("char(%s)", size), nil
		}
	}

	// char(n) -> char(n)
	if strings.HasPrefix(sqlServerType, "char") {
		re := regexp.MustCompile(`char\((\d+)\)`)
		if re.MatchString(sqlServerType) {
			return sqlServerType, nil
		}
	}

	// numeric(p,s) -> decimal(p,s)
	if strings.HasPrefix(sqlServerType, "numeric") {
		re := regexp.MustCompile(`numeric\((\d+),(\d+)\)`)
		if re.MatchString(sqlServerType) {
			matches := re.FindStringSubmatch(sqlServerType)
			return fmt.Sprintf("decimal(%s,%s)", matches[1], matches[2]), nil
		}
		// numeric without precision -> decimal(10,0)
		if sqlServerType == "numeric" {
			return "decimal(10,0)", nil
		}
	}

	// decimal(p,s) -> decimal(p,s)
	if strings.HasPrefix(sqlServerType, "decimal") {
		re := regexp.MustCompile(`decimal\((\d+),(\d+)\)`)
		if re.MatchString(sqlServerType) {
			return sqlServerType, nil
		}
		// decimal without precision -> decimal(10,0)
		if sqlServerType == "decimal" {
			return "decimal(10,0)", nil
		}
	}

	// float -> double
	if sqlServerType == "float" {
		return "double", nil
	}

	// real -> float
	if sqlServerType == "real" {
		return "float", nil
	}

	// money -> decimal(19,4)
	if sqlServerType == "money" {
		return "decimal(19,4)", nil
	}

	// smallmoney -> decimal(10,4)
	if sqlServerType == "smallmoney" {
		return "decimal(10,4)", nil
	}

	// uniqueidentifier -> char(36)
	if sqlServerType == "uniqueidentifier" {
		return "char(36)", nil
	}

	// 查找直接映射
	if mysqlType, ok := tm.typeMap[sqlServerType]; ok {
		return mysqlType, nil
	}

	// 未知类型，返回错误
	return "", fmt.Errorf("unsupported SQL Server type: %s", sqlServerType)
}

// CleanCollation 移除 COLLATE 子句和 DEFAULT 值
func (tm *TypeMapper) CleanCollation(columnDef string) string {
	// 移除 COLLATE Chinese_PRC_90_CI_AI 等
	re := regexp.MustCompile(`\s+COLLATE\s+\w+`)
	columnDef = re.ReplaceAllString(columnDef, "")

	// 移除 DEFAULT 值（如 DEFAULT 0, DEFAULT 1）
	re = regexp.MustCompile(`\s+DEFAULT\s+\S+`)
	columnDef = re.ReplaceAllString(columnDef, "")

	return columnDef
}
