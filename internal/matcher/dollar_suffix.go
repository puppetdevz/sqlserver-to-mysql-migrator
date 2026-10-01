package matcher

import "strings"

// TableNameToCSVFileName 将表名转换为 CSV 文件名基础名（去掉 $ 后缀）
// 例: TABLE$ → TABLE，SAMPLE_ITEMS → SAMPLE_ITEMS
func TableNameToCSVFileName(tableName string) string {
	if strings.HasSuffix(tableName, "$") {
		return strings.TrimSuffix(tableName, "$")
	}
	return tableName
}

// CSVFileNameToTableName 将 CSV 文件名转换为表名（处理 $ 后缀和 timestamp）
// timestamp 为空时：TABLE__.csv → TABLE$
// timestamp 非空时：TABLE__20000101000000.csv → TABLE$
//              SAMPLE_ITEMS_20000101000000.csv → SAMPLE_ITEMS
func CSVFileNameToTableName(fileName, timestamp string) string {
	// 移除 .csv 后缀
	name := strings.TrimSuffix(fileName, ".csv")

	if timestamp == "" {
		// 无 timestamp: TABLE__ → TABLE$
		if strings.HasSuffix(name, "__") {
			return strings.TrimSuffix(name, "__") + "$"
		}
		return name
	}

	// 有 timestamp: 优先匹配 __TIMESTAMP（$ 表）
	dollarSuffix := "__" + timestamp
	if strings.HasSuffix(name, dollarSuffix) {
		return strings.TrimSuffix(name, dollarSuffix) + "$"
	}

	// 普通表: _TIMESTAMP
	suffix := "_" + timestamp
	if strings.HasSuffix(name, suffix) {
		return strings.TrimSuffix(name, suffix)
	}

	// 无 timestamp 匹配，返回原名（去掉 .csv）
	return name
}
