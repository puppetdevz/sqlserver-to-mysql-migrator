package source

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

func validateSQLServerFixtureDSN(dsn string) error {
	u, e := url.Parse(dsn)
	if e != nil {
		return fmt.Errorf("invalid fixture URL (credentials redacted)")
	}
	host, _, e := net.SplitHostPort(u.Host)
	if e != nil {
		return fmt.Errorf("invalid fixture endpoint (credentials redacted)")
	}
	if u.Scheme != "sqlserver" || host != "127.0.0.1" || u.Query().Get("database") != "pi_migration_fixture" {
		return fmt.Errorf("refusing non-disposable fixture DSN")
	}
	return nil
}

func fixturePortMatches(dsn, binding string) bool {
	u, e := url.Parse(dsn)
	return e == nil && u.Host == strings.TrimSpace(binding)
}
