package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/bundle"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/source"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/target"
)

func runBundleCLI(args []string) int {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if len(args) == 0 {
		return 1
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dir := fs.String("bundle", "", "new export directory / sealed import directory")
	scope := fs.String("tables", "", "explicit schema.table list (export only)")
	dry := fs.Bool("dry-run", false, "validate/preview without database writes")
	confirm := fs.String("confirm", "", "plan digest authorization for new staging tables")
	report := fs.String("report", "", "new durable staging report JSON path (import only)")
	maxBytes := fs.Int64("max-bundle-bytes", bundle.DefaultMaxBundleBytes, "hard export byte budget")
	if err := fs.Parse(args[1:]); err != nil {
		return 1
	}
	if fs.NArg() != 0 || *dir == "" {
		fmt.Fprintln(os.Stderr, "--bundle required; legacy flags are not accepted in bundle mode")
		return 1
	}
	switch args[0] {
	case "export-sqlserver":
		if *dry || *confirm != "" || *report != "" || *scope == "" {
			fmt.Fprintln(os.Stderr, "export requires --tables; no --dry-run/--confirm")
			return 1
		}
		url := os.Getenv("SQLSERVER_SOURCE_URL")
		if url == "" {
			fmt.Fprintln(os.Stderr, "SQLSERVER_SOURCE_URL missing")
			return 1
		}
		db, err := source.Open(ctx, url)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		defer db.Close()
		ctx, cancel := context.WithTimeout(ctx, 8*time.Hour)
		defer cancel()
		err = source.ExportWithLimit(ctx, db, strings.Split(*scope, ","), *dir, *maxBytes)
		if err != nil {
			fmt.Fprintln(os.Stderr, "export failed (bundle not sealed):", err)
			return 1
		}
		fmt.Println("sealed bundle:", *dir)
		return 0
	case "import-bundle":
		if *scope != "" || *maxBytes != bundle.DefaultMaxBundleBytes {
			fmt.Fprintln(os.Stderr, "--tables not valid for sealed bundle")
			return 1
		}
		m, err := bundle.Verify(*dir)
		if err != nil {
			fmt.Fprintln(os.Stderr, "bundle rejected:", err)
			return 1
		}
		dsn := os.Getenv("GOLDENDB_TARGET_DSN")
		if dsn == "" {
			fmt.Fprintln(os.Stderr, "GOLDENDB_TARGET_DSN missing")
			return 1
		}
		cfg, err := mysql.ParseDSN(dsn)
		if err != nil || cfg.DBName == "" || cfg.Addr == "" {
			fmt.Fprintln(os.Stderr, "invalid target DSN (redacted)")
			return 1
		}
		identity := cfg.Net + "/" + cfg.Addr + "/" + cfg.DBName + "/" + cfg.User
		plan := target.PlanHash(m, identity)
		fmt.Printf("target=%s/%s run=%s plan=%s; existing tables rejected; %d NEW staging tables, no publication, no DROP/TRUNCATE\n", cfg.Addr, cfg.DBName, m.RunID, plan, len(m.Tables))
		for i, t := range m.Tables {
			fmt.Printf("  %s.%s -> staging #%d (%d rows)\n", t.Schema, t.Name, i+1, t.Data.Count)
		}
		if *dry {
			return 0
		}
		if *confirm != plan || *report == "" {
			fmt.Fprintln(os.Stderr, "explicit --confirm <plan digest> and --report <new path> required (preview with --dry-run)")
			return 1
		}
		if _, e := os.Lstat(*report); e == nil {
			fmt.Fprintln(os.Stderr, "report already exists")
			return 1
		} else if !os.IsNotExist(e) {
			fmt.Fprintln(os.Stderr, e)
			return 1
		}
		db, database, err := target.Open(dsn)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		defer db.Close()
		ctx, cancel := context.WithTimeout(ctx, 8*time.Hour)
		defer cancel()
		if err = target.ImportReported(ctx, db, *dir, database, identity, *confirm, *report); err != nil {
			fmt.Fprintln(os.Stderr, "unpublished staging import failed; inspect/quarantine run-scoped tables, never auto-retry:", err)
			return 1
		}
		fmt.Println("staging validated, NOT published; G1 approval needed before publication")
		return 0
	default:
		fmt.Fprintln(os.Stderr, errors.New("unknown bundle command"))
		return 1
	}
}
