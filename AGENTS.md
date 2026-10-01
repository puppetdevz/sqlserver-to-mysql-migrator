# Repository Guidelines

## 默认验证

使用 Go 1.26.1+：

```bash
go test ./... -count=1 -timeout=120s
go vet ./...
bash scripts/backup_restore_test.sh
python3 -m unittest discover -s scripts -p 'test_*.py'
python3 scripts/check_publication.py --history
```

默认测试必须离线安全。真实数据库测试全部需要 `integration` 标签和显式隔离 fixture，见 docs/testing.md；不允许读取实际 config.local.yaml 回退目标。不要为了测试连接、清空或修改真实数据库。

用户明确要求本轮免重测时，不自行运行被豁免的测试、构建、扫描或触发 CI；保留此前证据及其适用范围，记录“按用户授权未重跑”，不得把未执行写成通过。免测验收不等于真实数据库、生产性能或公开授权。

构建只用 scripts/build.sh，产物只在 dist/，名称 sqlserver-to-mysql-migrator-<os>-<arch>。不要在根目录生成/运行迁移二进制。构建通过不是生产验收授权。普通开发不得修改产品版本号或发布日志；发布须用户单独授权。

## 代码与资料

CLI 在 cmd/migrate，逻辑在 internal/。Go 按 gofmt；缺陷先写复现。临时文件使用 t.TempDir/t.Chdir，不能改私有配置/业务数据/运行清单迁就测试。不要提交实际口令、业务 DDL/CSV、dump、内部交付文档、日志、诊断/profile 或二进制。公开样例须合成。真实配置保持 gitignored；不要读取并输出它的值。

README 是运行合同，SECURITY 是安全联系渠道，docs/publication-checklist.md 是人工公开门禁。未经授权不重写历史、不强推、不公开。提交采用 feat/fix/refactor/docs/test/chore 范围风格。

## 不得退化的合同

- 缺失 CSV：本轮选中的已有表仍先 TRUNCATE，缺 CSV 正常跳过；无其它错误则成功。未选中、skip/completed 表不得清空；dry-run/create-only 不清空。
- 拆批：先规划占位符/字节上限，仅重试未成功区间；按成功消费输入行数推进，不能用 RowsAffected。提交结果未知返回 ErrUnknownCommit，不盲重放。
- 行数：普通 worker/自适应调度共用校验登记；成功输入行数对比 COUNT，失败不得写 completed；缺 CSV 不 COUNT、不判失败、不登记。
- CSV：正常字段数/显式空字段保持；歧义、无候选、短行报结构错误，不猜测。错误通道不能用 select/default 丢弃。
- 建表：继续收集失败表，清单失败或任何建表失败最终非零退出；失败表排除后续数据处理；不自动为 MySQL 1118 降级重建；create-only 参数显式传递。
- DDL：原表名存储；完全同名重复以及不区分大小写的碰撞均写入前拒绝。SHOW TABLES/CSV key 同样不能覆盖。
- 标识符/会话：动态 SQL 标识符用 matcher.QuoteIdent；会话变量通过 mysql.Config 对每条物理连接生效；DSN 使用 FormatDSN，日志不泄漏口令前缀。
