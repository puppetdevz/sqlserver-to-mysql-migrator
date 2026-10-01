//go:build integration

package importer

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/database"
)

func newMySQLFixtureConnection(t *testing.T) *database.Connection {
	t.Helper()
	dsn, container := os.Getenv("MIGRATOR_MYSQL_TEST_DSN"), os.Getenv("MIGRATOR_MYSQL_DOCKER_ID")
	if dsn == "" || container == "" {
		t.Skip("requires integration tag and explicit disposable Docker MySQL fixture")
	}
	out, err := exec.Command("docker", "inspect", "--format", "{{.State.Running}}", container).Output()
	if err != nil || strings.TrimSpace(string(out)) != "true" {
		t.Fatal("disposable fixture container is not running")
	}
	binding, err := exec.Command("docker", "port", container, "3306/tcp").Output()
	if err != nil {
		t.Fatal("cannot verify disposable fixture port")
	}
	cfg, err := mysqlFixtureConfig(dsn, string(binding))
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	conn, err := database.NewConnection(cfg)
	if err != nil {
		t.Fatal("cannot connect to explicit disposable fixture")
	}
	return conn
}
