package importer

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/config"
)

func mysqlFixtureConfig(dsn, binding string) (*config.TargetConfig, error) {
	c, err := mysql.ParseDSN(dsn)
	if err != nil || c.Net != "tcp" || c.DBName != "migrator_import_fixture" || c.User == "" {
		return nil, fmt.Errorf("requires an explicit migrator_import_fixture TCP DSN")
	}
	host, port, err := net.SplitHostPort(c.Addr)
	if err != nil || host != "127.0.0.1" || c.Addr != strings.TrimSpace(binding) {
		return nil, fmt.Errorf("DSN must match the disposable Docker loopback port")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return nil, fmt.Errorf("invalid fixture port")
	}
	return &config.TargetConfig{Host: host, Port: n, Database: c.DBName, User: c.User, Password: c.Passwd, Charset: "utf8mb4"}, nil
}

func validateGoldenDBFixtureDSN(dsn string) error {
	c, err := mysql.ParseDSN(dsn)
	if err != nil || c.Net != "tcp" || c.DBName != "migrator_goldendb_fixture" || c.User == "" || c.TLSConfig != "true" {
		return fmt.Errorf("requires explicit migrator_goldendb_fixture TCP DSN with verified TLS")
	}
	host, _, err := net.SplitHostPort(c.Addr)
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	address, _, _ := strings.Cut(host, "%")
	ip := net.ParseIP(address)
	if err != nil || host == "" || host == "localhost" || strings.HasSuffix(host, ".localhost") || (ip != nil && (ip.IsLoopback() || ip.IsUnspecified())) {
		return fmt.Errorf("GoldenDB fixture must not use a local/default target")
	}
	return nil
}

func TestMySQLFixtureRejectsUnsafeTargets(t *testing.T) {
	for _, dsn := range []string{
		"", "fixture@tcp(localhost:3307)/migrator_import_fixture",
		"fixture@tcp(127.0.0.1:3307)/migration_example",
		"fixture@tcp(127.0.0.1:3306)/migrator_import_fixture",
		"fixture@unix(/tmp/mysql.sock)/migrator_import_fixture",
		"fixture@tcp(203.0.113.7:3307)/migrator_import_fixture",
	} {
		if _, err := mysqlFixtureConfig(dsn, "127.0.0.1:3307"); err == nil {
			t.Error("accepted unsafe fixture target")
		}
	}
	if _, err := mysqlFixtureConfig("fixture@tcp(127.0.0.1:3307)/migrator_import_fixture", "127.0.0.1:3307\n"); err != nil {
		t.Fatal(err)
	}
}

func TestGoldenDBFixtureRejectsUnsafeTargets(t *testing.T) {
	for _, dsn := range []string{
		"", "fixture@tcp(localhost:3306)/migrator_goldendb_fixture?tls=true",
		"fixture@tcp(localhost.:3306)/migrator_goldendb_fixture?tls=true",
		"fixture@tcp(:3306)/migrator_goldendb_fixture?tls=true",
		"fixture@tcp([::1%lo0]:3306)/migrator_goldendb_fixture?tls=true",
		"fixture@tcp(127.0.0.2:3306)/migrator_goldendb_fixture?tls=true",
		"fixture@tcp([::1]:3306)/migrator_goldendb_fixture?tls=true",
		"fixture@tcp(203.0.113.7:3306)/migration_example?tls=true",
		"fixture@tcp(203.0.113.7:3306)/migrator_goldendb_fixture?tls=skip-verify",
		"fixture@tcp(203.0.113.7:3306)/migrator_goldendb_fixture",
	} {
		if err := validateGoldenDBFixtureDSN(dsn); err == nil {
			t.Error("accepted unsafe GoldenDB fixture")
		}
	}
	if err := validateGoldenDBFixtureDSN("fixture@tcp(203.0.113.7:3306)/migrator_goldendb_fixture?tls=true"); err != nil {
		t.Fatal(err)
	}
}
