package database

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/config"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/logger"
)

// Connection 数据库连接封装
type Connection struct {
	DB *sql.DB
	// 表名映射：大写表名 -> 实际表名（解决 MySQL 大小写不敏感问题）
	tableNameMap map[string]string
}

// NewConnection 创建新的数据库连接
func NewConnection(cfg *config.TargetConfig) (*Connection, error) {
	// 打开数据库连接
	db, err := sql.Open("mysql", cfg.GetDSN())
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// 配置连接池
	db.SetMaxOpenConns(cfg.MaxOpenConns)
	db.SetMaxIdleConns(cfg.MaxIdleConns)
	db.SetConnMaxLifetime(cfg.GetConnMaxLifetime())

	// 测试连接
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	logger.Infof("Database connected: %s@%s:%d/%s", cfg.User, cfg.Host, cfg.Port, cfg.Database)

	return &Connection{DB: db}, nil
}

// Close 关闭数据库连接
func (c *Connection) Close() error {
	if c.DB != nil {
		logger.Info("Closing database connection")
		return c.DB.Close()
	}
	return nil
}

// Ping 测试数据库连接
func (c *Connection) Ping() error {
	return c.DB.Ping()
}

// GetTableNames 获取数据库中所有表名
func (c *Connection) GetTableNames() ([]string, error) {
	query := "SHOW TABLES"
	rows, err := c.DB.Query(query)
	if err != nil {
		return nil, fmt.Errorf("failed to query tables: %w", err)
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var tableName string
		if err := rows.Scan(&tableName); err != nil {
			return nil, fmt.Errorf("failed to scan table name: %w", err)
		}
		tables = append(tables, tableName)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating tables: %w", err)
	}

	// 填充表名映射
	c.buildTableNameMap(tables)

	return tables, nil
}

// buildTableNameMap 构建表名映射（大写 -> 实际）
func (c *Connection) buildTableNameMap(tables []string) {
	c.tableNameMap = make(map[string]string)
	for _, t := range tables {
		c.tableNameMap[strings.ToUpper(t)] = t
	}
}

// RefreshTableNameMap 刷新表名映射（在创建新表后调用）
func (c *Connection) RefreshTableNameMap() error {
	tables, err := c.GetTableNames()
	if err != nil {
		return err
	}
	c.buildTableNameMap(tables)
	return nil
}

// GetActualTableName 获取正确大小写的表名
// 如果找不到表，返回原表名（让后续操作报错，而不是静默失败）
func (c *Connection) GetActualTableName(tableName string) string {
	if c.tableNameMap == nil {
		// 如果映射未初始化，先构建
		tables, err := c.GetTableNames()
		if err != nil || len(tables) == 0 {
			return tableName
		}
	}
	if actual, ok := c.tableNameMap[strings.ToUpper(tableName)]; ok {
		return actual
	}
	return tableName
}

// TableExists 检查表是否存在
// 注意：SHOW TABLES LIKE 不支持参数化查询，需要直接拼接表名
func (c *Connection) TableExists(tableName string) (bool, error) {
	// 确保表名映射已初始化
	if c.tableNameMap == nil {
		tables, err := c.GetTableNames()
		if err != nil {
			return false, fmt.Errorf("failed to get table names: %w", err)
		}
		if len(tables) == 0 {
			return false, nil
		}
	}

	// 使用映射检查表是否存在
	_, exists := c.tableNameMap[strings.ToUpper(tableName)]
	return exists, nil
}

// ExecuteDDL 执行 DDL 语句（支持多语句，用分号分隔）
func (c *Connection) ExecuteDDL(ddl string) error {
	// 分割多语句（按分号分隔）
	statements := strings.Split(ddl, ";")

	for _, stmt := range statements {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}

		// 确保语句以分号结尾（MySQL DDL 推荐格式）
		if !strings.HasSuffix(stmt, ";") {
			stmt = stmt + ";"
		}

		_, err := c.DB.Exec(stmt)
		if err != nil {
			return fmt.Errorf("failed to execute DDL [%s]: %w", stmt, err)
		}
	}
	return nil
}

// TruncateTable 清空表数据
func (c *Connection) TruncateTable(tableName string) error {
	actualName := c.GetActualTableName(tableName)
	query := fmt.Sprintf("TRUNCATE TABLE `%s`", actualName)
	_, err := c.DB.Exec(query)
	if err != nil {
		return fmt.Errorf("failed to truncate table %s: %w", actualName, err)
	}
	logger.Infof("Table truncated: %s", actualName)
	return nil
}

// GetRowCount 获取表的行数
func (c *Connection) GetRowCount(tableName string) (int64, error) {
	actualName := c.GetActualTableName(tableName)
	query := fmt.Sprintf("SELECT COUNT(*) FROM `%s`", actualName)
	var count int64
	err := c.DB.QueryRow(query).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("failed to get row count for table %s: %w", actualName, err)
	}
	return count, nil
}

// BeginTx 开始事务
func (c *Connection) BeginTx() (*sql.Tx, error) {
	return c.DB.Begin()
}

// RetryConnect 重试连接数据库
func RetryConnect(cfg *config.TargetConfig, maxRetries int) (*Connection, error) {
	var conn *Connection
	var err error

	for i := 0; i < maxRetries; i++ {
		conn, err = NewConnection(cfg)
		if err == nil {
			return conn, nil
		}

		logger.Warnf("Failed to connect to database (attempt %d/%d): %v", i+1, maxRetries, err)
		if i < maxRetries-1 {
			// 指数退避
			waitTime := time.Duration(1<<uint(i)) * time.Second
			logger.Infof("Retrying in %v...", waitTime)
			time.Sleep(waitTime)
		}
	}

	return nil, fmt.Errorf("failed to connect after %d attempts: %w", maxRetries, err)
}
