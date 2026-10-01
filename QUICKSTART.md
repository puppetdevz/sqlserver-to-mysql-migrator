# 快速开始

从仓库根目录执行。需要 Go **1.26.1+**；示例没有真实业务数据或口令。

## 1. 构建并运行默认离线回归

```bash
go mod download
go test ./... -count=1 -timeout=120s
./scripts/build.sh darwin
```

Linux 使用 `./scripts/build.sh linux`。产物只在 `dist/`；具体名称见 [README](README.md)。

## 2. 不连接数据库的演示

```bash
./dist/sqlserver-to-mysql-migrator-darwin-arm64 --version
./dist/sqlserver-to-mysql-migrator-darwin-arm64 \
  --config examples/config.yaml --csv-inventory /tmp/migrator-example-inventory.jsonl
./dist/sqlserver-to-mysql-migrator-darwin-arm64 \
  --extract-csv examples/csv/sample_items.csv \
  --extract-output /tmp/migrator-example-sample.csv --extract-records 1
```

两个输出必须是尚不存在的新文件。inventory 只读文件元数据；extract 按逻辑记录读取合成 CSV。示例 DDL 的兼容标记是旧解析器格式要求，见 README。

## 3. 数据库操作前的必要检查

1. 使用自己明确授权的独立测试库，完成备份及恢复演练。
2. 配置最小权限账户与安全保管的口令；不要将口令写进示例、提交或命令历史。
3. 检查 `.local.yaml` 同目录覆盖：根本地配置会覆盖根模板，但不会覆盖 `examples/config.yaml`。
4. 先理解写入合同：**旧模式选中的已有表即使 CSV 缺失也仍先 TRUNCATE**。跳过表/完成表不清空。
5. `--dry-run` 对旧模式不写库，但仍连接目标；不是完全离线演示。`--create-tables-only` 会创建表。

核对后可以在隔离环境使用 README 的明确表范围命令。不要直接运行全量迁移试探环境。

## 4. bundle 模式与测试

bundle 为 SNAPSHOT → 封存产物 → 未发布 staging，不自动切换业务表。需要显式表范围、验证证书的 TLS、目标计划确认和新报告路径。GoldenDB 精确版本/真实跨表快照/全量性能未现场验收。

默认测试不连真实数据库；显式集成测试配置见 [docs/testing.md](docs/testing.md)。安全报告发往 **puppetdevz@gmail.com**，请勿附带实际凭据或业务数据。
