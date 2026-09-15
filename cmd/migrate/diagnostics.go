package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/config"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/diagnostics"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/importer"
)

var (
	diagnosticsDir      = flag.String("diagnostics-dir", "", "显式诊断运行根目录（不表示已获写入授权）")
	diagnosticsKey      = flag.String("diagnostics-key", "", "本地私密伪名密钥文件，至少32字节；不导出")
	diagnosticsInterval = flag.Duration("diagnostics-interval", 5*time.Second, "低频诊断采样间隔，最小1秒")
	reportExport        = flag.String("report-export", "", "离线导出指定运行目录，不加载迁移配置/连接数据库")
	reportOutput        = flag.String("report-output", "", "脱敏 tar.gz 输出文件（禁止覆盖）")
	reportCompare       = flag.String("report-compare", "", "离线对照的 A 运行目录")
	reportWith          = flag.String("report-with", "", "离线对照的 B 运行目录")
	requireDiagnostics  = flag.Bool("require-diagnostics", false, "G1/基准写入前必须启用 --diagnostics-dir；默认 false")
)

func requireDiagnosticsOrError() error {
	if *requireDiagnostics && *diagnosticsDir == "" {
		return fmt.Errorf("--require-diagnostics needs --diagnostics-dir before any database connection")
	}
	return nil
}

func runOfflineReport() (bool, int) {
	if *reportExport == "" && *reportCompare == "" && *reportOutput == "" && *reportWith == "" {
		return false, 0
	}
	var err error
	switch {
	case *reportExport != "" && *reportOutput != "" && *reportCompare == "" && *reportWith == "":
		err = diagnostics.Export(*reportExport, *reportOutput)
	case *reportCompare != "" && *reportWith != "" && *reportExport == "" && *reportOutput == "":
		var text string
		text, err = diagnostics.Compare(*reportCompare, *reportWith)
		if err == nil {
			fmt.Print(text)
		}
	default:
		err = fmt.Errorf("use --report-export DIR --report-output FILE or --report-compare A --report-with B")
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "Offline report failed: %v\n", err)
		return true, 1
	}
	return true, 0
}
func openDiagnostics(cfg *config.Config, started time.Time) (*diagnostics.Recorder, error) {
	if *diagnosticsDir == "" {
		if *diagnosticsKey != "" {
			return nil, fmt.Errorf("--diagnostics-key requires --diagnostics-dir")
		}
		return nil, nil
	}
	if *diagnosticsKey == "" {
		return nil, fmt.Errorf("--diagnostics-dir requires a private --diagnostics-key file")
	}
	f, err := os.Open(*diagnosticsKey)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() < 32 || info.Size() > 4096 {
		return nil, fmt.Errorf("diagnostic key must be a regular file of 32..4096 bytes")
	}
	key := make([]byte, info.Size())
	if _, err = f.ReadAt(key, 0); err != nil {
		return nil, err
	}
	m, t := cfg.Migration, cfg.Target
	limits := m.Resources.Effective()
	settings := diagnostics.Settings{BatchSize: m.BatchSize, MaxBatchBytes: m.MaxBatchBytes, MaxWorkers: m.MaxWorkers, ImportTokens: m.EffectiveImportTokens(), MaxOpenConns: t.MaxOpenConns, MaxIdleConns: t.MaxIdleConns, MaxRowsPerTable: m.MaxRowsPerTable, MaxInflightBytes: limits.MaxInflightBytes, MaxInflightBatches: limits.MaxInflightBatches, QueueBytes: limits.QueueBytes, QueueBatches: limits.QueueBatches, BatchMemoryBytes: limits.BatchMemoryBytes, SQLCacheBytes: limits.SQLCacheBytes, PreCount: m.ShouldCountCSVRowsBeforeImport(), ValidateCount: m.ShouldValidateRowCount(), Replace: m.OnDuplicate == "replace", Adaptive: m.ShouldUseAdaptiveImport(), CaseSensitive: m.IsTableNameCaseSensitive(), HasHeader: cfg.Source.IsCSVHasHeader(), DryRun: *dryRun, CreateOnly: *createOnly}
	r, err := diagnostics.Open(*diagnosticsDir, key, *diagnosticsInterval, settings, Version+"-"+BuildLabel, fmt.Sprintf("%s:%d/%s", t.Host, t.Port, t.Database), started)
	if err != nil {
		return nil, err
	}
	if importer.BaselineAlgorithms {
		return r, nil
	}
	if err = r.SetResources(diagnostics.ResourceLimits{MaxInflightBytes: limits.MaxInflightBytes, MaxInflightBatches: limits.MaxInflightBatches, QueueBytes: limits.QueueBytes, QueueBatches: limits.QueueBatches, BatchMemoryBytes: limits.BatchMemoryBytes, SQLCacheBytes: limits.SQLCacheBytes}); err != nil {
		_ = r.Close(false, false)
		return nil, err
	}
	return r, nil
}
