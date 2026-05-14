package matcher

import "testing"

func TestTableNameToCSVFileName(t *testing.T) {
	tests := []struct {
		name      string
		tableName string
		want      string
	}{
		{"dollar table", "TABLE$", "TABLE"},
		{"dollar table mixed case", "FORMMAIN$", "FORMMAIN"},
		{"ordinary table unchanged", "SAMPLE_MAIN_102", "SAMPLE_MAIN_102"},
		{"ordinary table simple", "AGENT", "AGENT"},
		{"already no dollar", "CUSTOM_TABLE_NAME", "CUSTOM_TABLE_NAME"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TableNameToCSVFileName(tt.tableName)
			if got != tt.want {
				t.Errorf("TableNameToCSVFileName(%q) = %q, want %q", tt.tableName, got, tt.want)
			}
		})
	}
}

func TestCSVFileNameToTableName(t *testing.T) {
	tests := []struct {
		name      string
		fileName  string
		timestamp string
		want      string
	}{
		// timestamp 非空，$ 表
		{"dollar table with timestamp", "TABLE__20000101000000.csv", "20000101000000", "TABLE$"},
		// timestamp 非空，普通表
		{"ordinary table with timestamp", "SAMPLE_MAIN_102_20000101000000.csv", "20000101000000", "SAMPLE_MAIN_102"},
		{"simple table with timestamp", "AGENT_20000101000000.csv", "20000101000000", "AGENT"},
		// timestamp 非空，无匹配后缀
		{"no matching suffix", "TABLE_99999999999999.csv", "20000101000000", "TABLE_99999999999999"},
		// timestamp 非空，文件名无 .csv
		{"no csv suffix", "TABLE__20000101000000", "20000101000000", "TABLE$"},
		// timestamp 非空，无 timestamp 部分
		{"no timestamp in name", "TABLE.csv", "20000101000000", "TABLE"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CSVFileNameToTableName(tt.fileName, tt.timestamp)
			if got != tt.want {
				t.Errorf("CSVFileNameToTableName(%q, %q) = %q, want %q", tt.fileName, tt.timestamp, got, tt.want)
			}
		})
	}
}