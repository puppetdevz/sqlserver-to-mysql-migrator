package converter

import (
	"fmt"
	"regexp"
	"strconv"
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

	// nvarchar(n) -> varchar(n)
	if strings.HasPrefix(sqlServerType, "nvarchar") {
		re := regexp.MustCompile(`nvarchar\((\d+|max)\)`)
		if re.MatchString(sqlServerType) {
			size := re.FindStringSubmatch(sqlServerType)[1]
			if size == "max" {
				// nvarchar(MAX) 转为 longtext，不计入行大小限制
				return "longtext", nil
			}
			// nvarchar(n) 如果 n >= 1000，转为 text 以避免行大小超限
			// (utf8mb4 编码下 varchar(1000) = 4000+ 字节，多列即超限)
			if sizeVal, err := strconv.Atoi(size); err == nil && sizeVal >= 1000 {
				return "text", nil
			}
			return fmt.Sprintf("varchar(%s)", size), nil
		}
	}

	// varchar(n) -> varchar(n)
	if strings.HasPrefix(sqlServerType, "varchar") {
		re := regexp.MustCompile(`varchar\((\d+|max)\)`)
		if re.MatchString(sqlServerType) {
			size := re.FindStringSubmatch(sqlServerType)[1]
			if size == "max" {
				// varchar(MAX) 转为 longtext，不计入行大小限制
				return "longtext", nil
			}
			// varchar(n) 如果 n >= 1000，转为 text 以避免行大小超限
			if sizeVal, err := strconv.Atoi(size); err == nil && sizeVal >= 1000 {
				return "text", nil
			}
			return fmt.Sprintf("varchar(%s)", size), nil
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

// CleanCollation 移除 COLLATE 子句
func (tm *TypeMapper) CleanCollation(columnDef string) string {
	// 移除 COLLATE Chinese_PRC_90_CI_AI 等
	re := regexp.MustCompile(`\s+COLLATE\s+\w+`)
	return re.ReplaceAllString(columnDef, "")
}
