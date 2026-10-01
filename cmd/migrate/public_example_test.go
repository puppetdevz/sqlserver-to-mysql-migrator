package main

import (
	"encoding/csv"
	"os"
	"strings"
	"testing"

	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/config"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/converter"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/parser"
)

func TestPublicSyntheticExampleParsesAndConverts(t *testing.T) {
	cfg, err := config.Load("../../examples/config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Target.Database != "migration_example" || cfg.Target.Password != "" || len(cfg.ConfigFiles()) != 1 {
		t.Fatal("public example must not load private root overrides")
	}
	tables, err := parser.NewDDLParser("../../examples/schema.sql").ParseAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(tables) != 1 || tables["sample_items"] == nil {
		t.Fatal("synthetic schema must contain exactly sample_items")
	}
	columns := tables["sample_items"].Columns
	if len(columns) != 2 || columns[0].Name != "id" || columns[1].Name != "label" {
		t.Fatal("synthetic column definitions were not parsed")
	}
	ddl, err := converter.NewTableConverter(cfg.Converter).ConvertToMySQL(tables["sample_items"])
	if err != nil || !strings.Contains(ddl, "sample_items") {
		t.Fatalf("synthetic DDL conversion failed: %v", err)
	}
	f, err := os.Open("../../examples/csv/sample_items.csv")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil || len(rows) != 3 || len(rows[0]) != 2 || rows[0][0] != "id" {
		t.Fatalf("synthetic CSV shape invalid: %v", err)
	}
}
