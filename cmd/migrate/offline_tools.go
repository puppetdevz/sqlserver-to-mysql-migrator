package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"sync"
	"time"

	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/config"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/csvsample"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/importer"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/logger"
)

var (
	csvInventory     = flag.String("csv-inventory", "", "离线写出 CSV 文件元数据 JSONL（不扫描内容、不连接数据库）")
	extractCSV       = flag.String("extract-csv", "", "用兼容 CSV 解析器截取逻辑记录到新文件，不修改原文件")
	extractOutput    = flag.String("extract-output", "", "截取输出文件（禁止覆盖）")
	extractRecords   = flag.Int("extract-records", 0, "截取的逻辑数据行数（不含表头）")
	extractHasHeader = flag.Bool("extract-has-header", true, "截取时是否保留表头")
	cpuProfile       = flag.String("cpu-profile", "", "可选本地 CPU profile 文件，默认不启用，不进入回传报告")
	heapProfile      = flag.String("heap-profile", "", "可选本地 heap profile 文件，默认不启用，不进入回传报告")
	profileDuration  = flag.Duration("profile-duration", 30*time.Second, "本地 profile 时长，1s-5m")
)

type csvInventoryRow struct {
	TableName string `json:"table_name"`
	FileName  string `json:"file_name"`
	Bytes     int64  `json:"bytes"`
	ModUnix   int64  `json:"mtime_unix"`
}

func runOfflineTools() (bool, int) {
	if *extractCSV == "" && *extractOutput == "" && *extractRecords == 0 {
		return false, 0
	}
	if *extractCSV == "" || *extractOutput == "" || *extractRecords < 1 {
		fmt.Fprintln(os.Stderr, "use --extract-csv FILE --extract-output FILE --extract-records N")
		return true, 1
	}
	n, err := csvsample.Extract(*extractCSV, *extractOutput, *extractRecords, *extractHasHeader)
	if err != nil {
		fmt.Fprintf(os.Stderr, "CSV extract failed: %v\n", err)
		return true, 1
	}
	fmt.Printf("extracted %d logical records to %s\n", n, *extractOutput)
	return true, 0
}

func writeCSVInventory(cfg *config.Config, dest string) error {
	if dest == "" {
		return nil
	}
	files, err := scanCSVFiles(cfg.Source.CSVDirectory)
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer out.Close()
	enc := json.NewEncoder(out)
	for _, path := range files {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		name := extractTableNameFromFile(filepath.Base(path), cfg.Source.CSVTimestamp)
		row := csvInventoryRow{TableName: name, FileName: filepath.Base(path), Bytes: info.Size(), ModUnix: info.ModTime().Unix()}
		if err := enc.Encode(row); err != nil {
			return err
		}
	}
	return nil
}

func startLocalProfiles() (func(), error) {
	if *cpuProfile == "" && *heapProfile == "" {
		return func() {}, nil
	}
	if *profileDuration < time.Second || *profileDuration > 5*time.Minute {
		return nil, fmt.Errorf("--profile-duration must be between 1s and 5m")
	}
	var cpuFile *os.File
	if *cpuProfile != "" {
		f, err := os.OpenFile(*cpuProfile, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return nil, err
		}
		if err := pprof.StartCPUProfile(f); err != nil {
			_ = f.Close()
			_ = os.Remove(*cpuProfile)
			return nil, err
		}
		cpuFile = f
	}
	var once sync.Once
	stop := func() {
		once.Do(func() {
			if cpuFile != nil {
				pprof.StopCPUProfile()
				_ = cpuFile.Close()
			}
			if *heapProfile == "" {
				return
			}
			f, err := os.OpenFile(*heapProfile, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				fmt.Fprintf(os.Stderr, "heap profile failed: %v\n", err)
				return
			}
			defer f.Close()
			runtime.GC()
			_ = pprof.WriteHeapProfile(f)
		})
	}
	timer := time.AfterFunc(*profileDuration, stop)
	return func() { timer.Stop(); stop() }, nil
}

func logWritePreview(cfg *config.Config, create, truncate []string) {
	limits := cfg.Migration.Resources.Effective()
	reportDir := "-"
	targetID := "unavailable-without-diagnostics"
	if cfg.Diagnostics != nil {
		reportDir = cfg.Diagnostics.Dir()
		targetID = cfg.Diagnostics.Pseudonym("target:" + fmt.Sprintf("%s:%d/%s", cfg.Target.Host, cfg.Target.Port, cfg.Target.Database))
	}
	algorithm := "prepared-p1"
	if importer.BaselineAlgorithms {
		algorithm = "prepared-p0-baseline"
	}
	logger.Info("WRITE PREVIEW ONLY. This is not write authorization. Re-run without --dry-run after human approval of an isolated target.")
	logger.Infof("Preview identity: version=%s-%s algorithm=%s report_dir=%s target_id=%s", Version, BuildLabel, algorithm, reportDir, targetID)
	logger.Infof("Preview limits: max_rows_per_table=%d pre_count=%t validate_count=%t adaptive=%t workers=%d tokens=%d inflight_bytes=%d queue_bytes=%d",
		cfg.Migration.MaxRowsPerTable, cfg.Migration.ShouldCountCSVRowsBeforeImport(), cfg.Migration.ShouldValidateRowCount(),
		cfg.Migration.ShouldUseAdaptiveImport(), cfg.Migration.MaxWorkers, cfg.Migration.EffectiveImportTokens(),
		limits.MaxInflightBytes, limits.QueueBytes)
	logger.Infof("Preview objects: would_create=%d would_truncate=%d selected_tables=%d", len(create), len(truncate), len(create)+len(truncate))
	for _, name := range create {
		logger.Infof("[DRY RUN] would create table %s", name)
	}
	for _, name := range truncate {
		logger.Infof("[DRY RUN] would truncate table %s", name)
	}
}
