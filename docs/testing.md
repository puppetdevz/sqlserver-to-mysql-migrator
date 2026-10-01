# 安全测试合同

## 默认套件

`go test ./...` 只编译离线/mock/纯单测，真实数据库文件使用 `//go:build integration`。环境中即使存在 DSN，默认套件也不加载这些文件。测试输出在临时目录；不读取实际私有配置来连接数据库。

`TestDefaultTestsExcludeDatabaseIntegration` 检查默认测试源码中是否出现已知真实 DB 构造器；它是回归保护，不是任意代码的静态安全证明。

## 显式集成测试

仅在你已获得授权的**可销毁隔离环境**使用，先通过独立安全渠道注入变量，不将 DSN 值写入 shell 历史、报告或提交。

| 套件 | 必需环境 | 隔离约束 |
| --- | --- | --- |
| 旧 CSV importer | MIGRATOR_MYSQL_TEST_DSN、MIGRATOR_MYSQL_DOCKER_ID | TCP、127.0.0.1、migrator_import_fixture；端口必须匹配运行中 Docker 容器的 3306/tcp 绑定 |
| SQL Server snapshot | SQLSERVER_FIXTURE_ADMIN_DSN、SQLSERVER_FIXTURE_READ_DSN、SQLSERVER_FIXTURE_DOCKER_ID | 127.0.0.1、pi_migration_fixture；1433/tcp 绑定必须匹配运行容器，源 SNAPSHOT 及只读权限由使用者设置 |
| bundle MySQL | MYSQL_BUNDLE_FIXTURE_DSN、MYSQL_BUNDLE_FIXTURE_DOCKER_ID | 127.0.0.1、pi_bundle_fixture；3306/tcp 绑定必须匹配运行容器 |
| GoldenDB smoke | GOLDENDB_TEST_DSN、GOLDENDB_TEST_ISOLATED=1 | TCP、migrator_goldendb_fixture、tls=true 验证证书；拒绝 localhost/loopback/unspecified 地址，无本地配置回退 |

启用命令（只在上述隔离门禁已满足时）：

```bash
go test -tags integration ./internal/importer -count=1 -timeout=120s
go test -tags integration ./internal/source ./internal/target -count=1 -timeout=120s
```

变量缺失时 Skip；变量存在但不合规时失败，不擅自猜测目标。Docker fixture 允许经验证的 loopback 绑定，不等于允许复用任意 localhost 数据库。集成测试会写入 fixture 的表；独立容器/测试库由使用者提供和销毁，不自动连接或创建生产数据库。

无变量的 tagged 套件可用于验证编译和 Skip，但**不等于已经完成数据库集成验证**。
