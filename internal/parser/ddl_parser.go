package parser

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
)

// TableDDL 表 DDL 定义
type TableDDL struct {
	TableName  string
	Columns    []ColumnDef
	PrimaryKey *PrimaryKeyDef
	Indexes    []IndexDef
	RawDDL     string
}

// ColumnDef 列定义
type ColumnDef struct {
	Name     string
	Type     string
	Nullable bool
	RawDef   string
}

// PrimaryKeyDef 主键定义
type PrimaryKeyDef struct {
	Name    string
	Columns []string
}

// IndexDef 索引定义
type IndexDef struct {
	Name    string
	Columns []string
	Unique  bool
}

// DDLParser DDL 解析器
type DDLParser struct {
	filePath string
}

// NewDDLParser 创建 DDL 解析器
func NewDDLParser(filePath string) *DDLParser {
	return &DDLParser{
		filePath: filePath,
	}
}

// ParseAll 解析所有表定义
func (p *DDLParser) ParseAll() (map[string]*TableDDL, error) {
	// 读取文件
	content, err := os.ReadFile(p.filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read DDL file: %w", err)
	}

	// 按表分割
	tables := make(map[string]*TableDDL)
	tableBlocks := p.splitByTable(string(content))
	var parseErrs []error

	for _, block := range tableBlocks {
		tableDDL, err := p.parseTableBlock(block)
		if err != nil {
			parseErrs = append(parseErrs, err)
			continue
		}
		if tableDDL != nil {
			if existing, ok := tables[tableDDL.TableName]; ok {
				return nil, fmt.Errorf("duplicate table definition for %q (conflicts with existing %q)", tableDDL.TableName, existing.TableName)
			}
			tables[tableDDL.TableName] = tableDDL
		}
	}
	if len(parseErrs) > 0 {
		return nil, errors.Join(parseErrs...)
	}

	return tables, nil
}

// ParseTable 解析指定表的 DDL
func (p *DDLParser) ParseTable(tableName string) (*TableDDL, error) {
	tables, err := p.ParseAll()
	if err != nil {
		return nil, err
	}

	if table, ok := tables[tableName]; ok {
		return table, nil
	}

	var matches []*TableDDL
	for name, table := range tables {
		if strings.EqualFold(name, tableName) {
			matches = append(matches, table)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		names := make([]string, 0, len(matches))
		for _, table := range matches {
			names = append(names, table.TableName)
		}
		sort.Strings(names)
		return nil, fmt.Errorf("ambiguous table name %q: matches %s", tableName, strings.Join(names, ", "))
	}

	return nil, fmt.Errorf("table not found: %s", tableName)
}

// splitByTable 按表分割 DDL 内容
func (p *DDLParser) splitByTable(content string) []string {
	// 使用注释标记分割：-- V80.dbo.{TABLE_NAME} definition
	// 支持带 $ 后缀的表名（如 sample_main_101$）
	re := regexp.MustCompile(`(?m)^-- V80\.dbo\.[\w$]+ definition`)
	indices := re.FindAllStringIndex(content, -1)

	if len(indices) == 0 {
		return []string{content}
	}

	var blocks []string
	for i := 0; i < len(indices); i++ {
		start := indices[i][0]
		var end int
		if i < len(indices)-1 {
			end = indices[i+1][0]
		} else {
			end = len(content)
		}
		blocks = append(blocks, content[start:end])
	}

	return blocks
}

// parseTableBlock 解析单个表块
func (p *DDLParser) parseTableBlock(block string) (*TableDDL, error) {
	lines := strings.Split(block, "\n")
	if len(lines) == 0 {
		return nil, nil
	}

	// 提取表名
	tableName := p.extractTableName(block)
	if tableName == "" {
		return nil, fmt.Errorf("failed to extract table name")
	}

	// 查找 CREATE TABLE 语句
	createTableStart := -1
	createTableEnd := -1
	for i, line := range lines {
		if strings.Contains(line, "CREATE TABLE") {
			createTableStart = i
		}
		if createTableStart >= 0 && strings.Contains(line, ");") {
			createTableEnd = i
			break
		}
	}

	if createTableStart < 0 || createTableEnd < 0 {
		return nil, fmt.Errorf("failed to find CREATE TABLE statement for %s", tableName)
	}

	// 解析列定义
	columns, primaryKey := p.parseColumns(lines[createTableStart+1 : createTableEnd])

	// 解析索引
	indexes := p.parseIndexes(lines[createTableEnd+1:])

	return &TableDDL{
		TableName:  tableName,
		Columns:    columns,
		PrimaryKey: primaryKey,
		Indexes:    indexes,
		RawDDL:     block,
	}, nil
}

// extractTableName 提取表名
func (p *DDLParser) extractTableName(block string) string {
	// 从注释中提取：-- V80.dbo.ADDRESSBOOK definition
	// 支持带 $ 后缀的表名（如 sample_main_101$）
	re := regexp.MustCompile(`-- V80\.dbo\.([\w$]+) definition`)
	matches := re.FindStringSubmatch(block)
	if len(matches) > 1 {
		return matches[1]
	}

	// 从 CREATE TABLE 语句中提取
	// 支持带 $ 后缀的表名
	re = regexp.MustCompile(`CREATE TABLE V80\.dbo\.([\w$]+)`)
	matches = re.FindStringSubmatch(block)
	if len(matches) > 1 {
		return matches[1]
	}

	return ""
}

// parseColumns 解析列定义
func (p *DDLParser) parseColumns(lines []string) ([]ColumnDef, *PrimaryKeyDef) {
	var columns []ColumnDef
	var primaryKey *PrimaryKeyDef

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "--") {
			continue
		}

		// 检查是否是主键约束
		if strings.Contains(line, "CONSTRAINT") && strings.Contains(line, "PRIMARY KEY") {
			primaryKey = p.parsePrimaryKey(line)
			continue
		}

		// 解析列定义
		column := p.parseColumn(line)
		if column != nil {
			columns = append(columns, *column)
		}
	}

	return columns, primaryKey
}

// parseColumn 解析单个列定义
func (p *DDLParser) parseColumn(line string) *ColumnDef {
	// 移除尾部逗号
	line = strings.TrimSuffix(strings.TrimSpace(line), ",")

	// 跳过 CONSTRAINT 行（非主键的 UNIQUE、FOREIGN KEY 等约束）
	if strings.Contains(strings.ToUpper(line), "CONSTRAINT") && !strings.Contains(strings.ToUpper(line), "PRIMARY KEY") {
		return nil
	}

	// 分割列名和类型
	parts := strings.Fields(line)
	if len(parts) < 2 {
		return nil
	}

	rawColumnName := parts[0]
	columnName := cleanIdentifier(rawColumnName)

	// 提取类型（可能包含括号和参数）
	typeStart := len(rawColumnName) + 1
	typePart := strings.TrimSpace(line[typeStart:])

	// 移除 DEFAULT 值部分（如 "datetime DEFAULT '1753-01-01 00:00:00' NOT NULL" -> "datetime NOT NULL"）
	// 只移除 DEFAULT 及其值，不影响 NOT NULL/NULL 标记
	typePart = regexp.MustCompile(`\s+DEFAULT\s+'[^']*'`).ReplaceAllString(typePart, "")
	typePart = regexp.MustCompile(`\s+DEFAULT\s+\S+`).ReplaceAllString(typePart, "")

	// 检查是否 nullable
	nullable := !strings.Contains(strings.ToUpper(line), "NOT NULL")

	return &ColumnDef{
		Name:     columnName,
		Type:     typePart,
		Nullable: nullable,
		RawDef:   line,
	}
}

// parsePrimaryKey 解析主键定义
func (p *DDLParser) parsePrimaryKey(line string) *PrimaryKeyDef {
	// CONSTRAINT PK_ADDRESSBOOK PRIMARY KEY (ID)
	re := regexp.MustCompile(`CONSTRAINT\s+(\w+)\s+PRIMARY KEY\s+\(([^)]+)\)`)
	matches := re.FindStringSubmatch(line)
	if len(matches) < 3 {
		return nil
	}

	name := matches[1]
	columnsStr := matches[2]
	columns := strings.Split(columnsStr, ",")
	for i := range columns {
		columns[i] = cleanIdentifier(columns[i])
	}

	return &PrimaryKeyDef{
		Name:    name,
		Columns: columns,
	}
}

// parseIndexes 解析索引定义
func (p *DDLParser) parseIndexes(lines []string) []IndexDef {
	var indexes []IndexDef

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if !strings.Contains(line, "CREATE") || !strings.Contains(line, "INDEX") {
			continue
		}

		index := p.parseIndex(line)
		if index != nil {
			indexes = append(indexes, *index)
		}
	}

	return indexes
}

// parseIndex 解析单个索引定义
func (p *DDLParser) parseIndex(line string) *IndexDef {
	// CREATE NONCLUSTERED INDEX IDX_ADDRESSBOOK_ENTITYID ON V80.dbo.ADDRESSBOOK (  MEMBER_ID ASC  )
	unique := strings.Contains(strings.ToUpper(line), "UNIQUE")

	// 提取索引名
	re := regexp.MustCompile(`INDEX\s+(\w+)\s+ON`)
	matches := re.FindStringSubmatch(line)
	if len(matches) < 2 {
		return nil
	}
	indexName := matches[1]

	// 提取列名
	re = regexp.MustCompile(`\(\s*([^)]+)\s*\)`)
	matches = re.FindStringSubmatch(line)
	if len(matches) < 2 {
		return nil
	}
	columnsStr := matches[1]

	columns := strings.Split(columnsStr, ",")
	for i := range columns {
		columns[i] = cleanIndexColumn(columns[i])
	}

	return &IndexDef{
		Name:    indexName,
		Columns: columns,
		Unique:  unique,
	}
}

func cleanIdentifier(identifier string) string {
	identifier = strings.TrimSpace(identifier)
	identifier = strings.Trim(identifier, "[]")
	return strings.TrimSpace(identifier)
}

func cleanIndexColumn(column string) string {
	fields := strings.Fields(strings.TrimSpace(column))
	if len(fields) == 0 {
		return ""
	}
	name := fields[0]
	return cleanIdentifier(name)
}
