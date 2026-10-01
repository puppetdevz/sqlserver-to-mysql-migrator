# bundle v2 的公开能力边界

这是独立的严格迁移路径，不兼容旧 CSV 目录。源用 SQL Server 的同一个 SNAPSHOT 事务导出选定表；未成功结束并封存的产物不能导入。

- 显式 schema.table 范围，不从旧配置推断。
- 对类型与对象采用拒绝矩阵：仅支持代码定义的 int/bigint/smallint/tinyint/bit/date、合规 decimal、datetime2(0..6)、有界 nvarchar/varbinary 等。identity/computed/default/alias 列，外键、触发器、CHECK 及不支持的索引拒绝，不猜测降级。
- manifest 和数据文件校验、规范化值、行数、多重集及主键分桶对账；篡改、缺文件、未封存、超预算拒绝。
- 目标已有业务表拒绝，不覆盖；只建 run-scoped staging，读回校验结果写新报告。
- `import-bundle --dry-run` 不连接目标，仅验证产物和构造计划。实际导入需要相同目标身份的计划摘要与新报告路径，验证 TLS 并再次检查目标。
- 成功仅表示 staging 对账成功，不会发布、更换业务表、自动补偿或保证原子切换。
- 失败不盲目重放；根据报告检查/隔离 run-scoped 对象，再决定整表重建。
- MySQL Docker fixture 的集成烟测不是 GoldenDB 精确版本验收。真实跨表快照、真实取值与全量性能均需现场单独授权和验证。

运行方式见 README，测试门禁见 testing.md。bundle、报告和诊断文件不是默认安全公开材料。
