# 贡献指南

欢迎提交可复现的缺陷报告、合成测试和小范围改进。请使用中性表名，不提交客户资料、真实 DDL/CSV、口令、日志、运行清单或二进制。安全问题不要发公开 Issue，见 [SECURITY.md](SECURITY.md)。

## 本地验证

Go 1.26.1+：

```bash
go mod download
go test ./... -count=1 -timeout=120s
go vet ./...
bash scripts/backup_restore_test.sh
python3 -m unittest discover -s scripts -p 'test_*.py'
python3 scripts/check_publication.py --history
./scripts/build.sh linux
```

真实数据库测试仅在 `integration` 标签和显式独立 fixture 下运行；默认测试不能依赖环境中的 DSN 或实际数据库服务。

## 变更要求

- 发现缺陷先写离线复现测试，再修复并保留回归。
- Go 文件运行 gofmt，临时输出使用 t.TempDir/t.Chdir；不要依靠真实本地配置让测试通过。
- PR 写明受影响的迁移模式、输入假设、验证命令和未验证能力。
- 普通开发不要递增产品版本或写发布日志；发布是单独授权的工作。
- 新依赖须审核 LICENSE/NOTICE 并更新 [依赖清单](docs/dependencies.md)。
- 提交不代表有权公开他人代码或单位数据；提交者须确认对贡献有授权，并同意贡献按本项目 MIT 许可提供，第三方片段必须保留来源和原许可。

## 必须保持的迁移合同

缺 CSV 仍清空已选中既有表；skip/completed 表不写；已成功子批次不重放；未知提交失败不盲重试；成功输入行数与 COUNT 校验通过才登记完成；CSV 修复有歧义时拒绝；建表失败收集后最终非零退出；标识符引用、名称冲突与每条物理连接的会话参数不可退化。具体指南见 [AGENTS.md](AGENTS.md)。
