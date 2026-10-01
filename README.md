# sqlserver-to-mysql-migrator

SQL Server → MySQL **协议兼容**数据库的迁移工具，提供两条独立路径：

| 路径 | 输入 / 输出 | 边界 |
| --- | --- | --- |
| 旧 CSV 离线导入 | SQL Server DDL + 外部 CSV → 目标库 | 会创建表并清空本轮选中的已有表 |
| bundle v2 | SQL Server SNAPSHOT → 封存 bundle → 未发布 staging | 严格校验；不发布、不替换业务表、不承诺原子切换 |

GoldenDB **精确版本兼容、真实源端跨表快照与全量性能尚未现场验收**。MySQL 协议兼容不等于所有兼容数据库都已验证。构建或单测通过不能替代生产验收。

## 先读安全约定

> **旧模式是破坏性工具：本轮选中的已有表，即使 CSV 缺失，也仍先 TRUNCATE，再正常跳过。** 未选中、命中 `skip_tables` 或 `completed_tables.txt` 的表不清空。只有明确授权、核对目标并完成可恢复备份后才运行写入命令。
>
> `--dry-run` 不写库，但旧 CSV 模式仍会连接数据库读取元数据；不是完全离线运行，也不是写入授权。`--create-tables-only` 会写 DDL，但不清空或导入数据。日志、错误行、bundle、报告和 profile 可能包含业务信息，禁止提交或直接公开。

仓库只提供合成示例，不附带业务数据、实际口令或数据库 dump。默认 `go test ./...` 不启用真实数据库集成测试。

## 前置条件与构建

- Go **1.26.1 或更高版本**（语言最低要求以 `go.mod` 为准）；实际构建使用受维护分支的最新安全补丁，CI 使用最新 1.26.x。
- 构建脚本需要 Bash、`file` 和 `sha256sum` 或 `shasum`。
- 目标为 MySQL 5.7.44 协议或兼容实现；准确能力须在独立隔离环境验证。

```bash
go mod download
go test ./... -count=1 -timeout=120s
go vet ./...
./scripts/build.sh darwin       # macOS ARM64
./scripts/build.sh linux        # Linux AMD64 + ARM64
```

产物仅写到 `dist/`：

- `sqlserver-to-mysql-migrator-darwin-arm64`
- `sqlserver-to-mysql-migrator-linux-amd64`
- `sqlserver-to-mysql-migrator-linux-arm64`

`BUILD_VARIANT=p0-baseline|p1` 可将产物写入 `dist/<variant>/`；baseline 使用 `migration_baseline` 编译标签。不要在根目录生成迁移二进制。旧 `migrate-*` 产物名不再生成；CLI 参数及迁移合同保留。

## 无数据库的示例

从仓库根目录执行：

```bash
./dist/sqlserver-to-mysql-migrator-darwin-arm64 --version
./dist/sqlserver-to-mysql-migrator-darwin-arm64 --help

# 仅读取合成 CSV 的文件元数据，输出文件必须不存在，不连数据库
./dist/sqlserver-to-mysql-migrator-darwin-arm64 \
  --config examples/config.yaml --csv-inventory /tmp/migrator-example-inventory.jsonl

# 按逻辑 CSV 记录截取，输出文件必须不存在；不加载数据库配置、不连库
./dist/sqlserver-to-mysql-migrator-darwin-arm64 \
  --extract-csv examples/csv/sample_items.csv \
  --extract-output /tmp/migrator-example-sample.csv --extract-records 1
```

`examples/schema.sql`、`examples/csv/` 均为重新创作的极小合成样例。旧 DDL 解析器依赖 `-- V80.dbo.<table> definition` 标记（或现有 `CREATE TABLE V80.dbo.<table>` 格式）；这不是通用 SQL 语法解析器。示例用兼容标记，不改变原解析算法。

## 配置与旧模式迁移

`--config` 默认 `config.yaml`，配置加载器自动合并**同目录**的 `.local.yaml`。例如根 `config.local.yaml` 覆盖根模板，`examples/config.local.yaml` 只覆盖示例模板。后者不会读取根本地配置。**已有本地覆盖可能改变有效目标，运行前必须核对。**

`config.local.example.yaml` 只含空口令，可作为私有本地配置的起点。实际密码只写入受控本地配置，不提交、不打印、不放入 shell 历史。使用最小权限账户。

主要选项：

- `source.ddl_file`、`source.csv_directory`、`csv_has_header`：DDL、UTF-8 CSV 与表头模式。
- 文件名约定 `{table}.csv`；`csv_timestamp` 可用于已有时间戳输入。
- `table_name_case_sensitive`：同时影响建表和导入匹配；冲突会在写入前拒绝。
- `batch_size`、`max_batch_bytes`、`max_workers`、`adaptive_import`：批次和压力预算。
- `row_count_validation`：成功处理的输入行数与目标 COUNT 对比；失败不登记完成。
- `on_duplicate`：`ignore` 或 `replace`。公开模板显式选 `ignore`，应按业务要求确认。
- `skip_tables`：显式跳过的表；模板为空，不携带业务表清单。

**以下命令会连接数据库；非预览命令可能写库。仅在已授权、已备份的隔离目标使用：**

```bash
# 预览：不写库，但会连接目标
./dist/sqlserver-to-mysql-migrator-darwin-arm64 --config examples/config.yaml --dry-run

# 仅创建缺失表
./dist/sqlserver-to-mysql-migrator-darwin-arm64 --config examples/config.yaml --create-tables-only

# 导入明确选中的合成表；已有表会清空
./dist/sqlserver-to-mysql-migrator-darwin-arm64 --config examples/config.yaml --tables sample_items

# 从你自己的私有 TXT 清单重导已有表
./dist/sqlserver-to-mysql-migrator-darwin-arm64 --config config.yaml \
  --reimport-tables --reimport-table-file /private/selected-tables.txt
```

不提供自动生产运行、自动调高数据库全局参数或内置数据库清空脚本。

## bundle v2 模式

仅用于单独授权的只读源和未发布 staging 目标。连接口令通过安全环境注入 `SQLSERVER_SOURCE_URL` 与 `GOLDENDB_TARGET_DSN`，两端都要求验证证书的 TLS；不要在命令示例中填写真实连接串。

```bash
./dist/sqlserver-to-mysql-migrator-darwin-arm64 export-sqlserver \
  --tables dbo.sample_items --bundle /private/new-run --max-bundle-bytes 10737418240
./dist/sqlserver-to-mysql-migrator-darwin-arm64 import-bundle \
  --bundle /private/new-run --dry-run
# 单独审核计划及目标后，才允许写入新 staging：
./dist/sqlserver-to-mysql-migrator-darwin-arm64 import-bundle \
  --bundle /private/new-run --confirm '<预览计划摘要>' --report /private/new-run-report.json
```

bundle 预览验证产物、连接串语法与计划摘要，但**不连接目标**；不能证明目标不存在同名表或验证实际 TLS 握手。实际导入再做目标预检与 staging 对账。失败不自动重放；按报告核对并整表重建。更多边界见 [bundle 合同](docs/bundle-v2.md)。

## 备份与恢复

`scripts/backup.sh`、`scripts/restore.sh` 使用已安全注入的 `DB_HOST/DB_PORT/DB_NAME/DB_USER/DB_PASS`；不会自动读取 `scripts/local-db.env.example`。

- 备份生成 SQL 和同名 SHA-256 文件；备份默认在 scripts/，建议通过 `BACKUP_DIR` 指向仓库外受控目录。
- 恢复在数据库操作前验证备份非空、可读及 SHA-256 一致；随后会**删除目标库的表、视图、例程和事件**。
- 恢复 `--dry-run` 仍会连接数据库查询对象，但不删除/恢复。
- 缺少校验文件的旧备份默认拒绝。SHA-256 只能证明内容未被意外改动，不证明 SQL 可完整恢复；先在独立隔离库演练。

`bash scripts/backup_restore_test.sh` 使用假客户端，不连接数据库。

## 测试、贡献和安全

```bash
go test ./... -count=1 -timeout=120s
go vet ./...
bash scripts/backup_restore_test.sh
python3 -m unittest discover -s scripts -p 'test_*.py'
python3 scripts/check_publication.py --history
```

真实数据库测试需要 `-tags integration` **和**显式隔离 fixture 环境；不允许回退本地配置。见 [测试指南](docs/testing.md)、[贡献指南](CONTRIBUTING.md)、[安全报告](SECURITY.md)、[发布前检查](docs/publication-checklist.md)。

## 许可与作者

项目代码使用 [MIT](LICENSE)。外部依赖保留原许可，见 [依赖清单](docs/dependencies.md)、[第三方声明](THIRD_PARTY_NOTICES.md)及[补充声明](THIRD_PARTY_ADDITIONAL_NOTICES.md)。

作者：**zhongyuming** · **puppetdevz@gmail.com**
