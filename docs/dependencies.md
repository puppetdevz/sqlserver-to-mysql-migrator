# 依赖许可清单

以下清单按当前锁定的完整 Go module graph 核对本地模块中的原始许可文件；CLI 列表示实际构建依赖，其余含测试或可选模块。完整 LICENSE/NOTICE 原文见 [THIRD_PARTY_NOTICES.md](../THIRD_PARTY_NOTICES.md)，不能将本项目 MIT 许可覆盖到这些依赖。

| Module | Version | License | CLI |
| --- | --- | --- | --- |
| github.com/Azure/azure-sdk-for-go/sdk/azcore | v1.23.1 | MIT | 否 |
| github.com/Azure/azure-sdk-for-go/sdk/azidentity | v1.14.1 | MIT | 否 |
| github.com/Azure/azure-sdk-for-go/sdk/internal | v1.12.0 | MIT | 否 |
| github.com/Azure/azure-sdk-for-go/sdk/security/keyvault/azkeys | v1.5.0 | MIT | 否 |
| github.com/Azure/azure-sdk-for-go/sdk/security/keyvault/internal | v1.2.0 | MIT | 否 |
| github.com/AzureAD/microsoft-authentication-library-for-go | v1.8.0 | MIT | 否 |
| github.com/DATA-DOG/go-sqlmock | v1.5.2 | BSD-3-Clause | 否 |
| github.com/davecgh/go-spew | v1.1.1 | ISC | 否 |
| github.com/go-sql-driver/mysql | v1.7.1 | MPL-2.0 | 是 |
| github.com/golang-jwt/jwt/v5 | v5.3.1 | MIT | 否 |
| github.com/golang-sql/civil | v0.0.0-20220223132316-b832511892a9 | Apache-2.0 | 是 |
| github.com/golang-sql/sqlexp | v0.1.0 | BSD-3-Clause | 是 |
| github.com/google/uuid | v1.6.0 | BSD-3-Clause | 是 |
| github.com/hashicorp/go-uuid | v1.0.3 | MPL-2.0 | 否 |
| github.com/jcmturner/aescts/v2 | v2.0.0 | Apache-2.0 | 否 |
| github.com/jcmturner/dnsutils/v2 | v2.0.0 | Apache-2.0 | 否 |
| github.com/jcmturner/gofork | v1.7.6 | BSD-3-Clause | 否 |
| github.com/jcmturner/goidentity/v6 | v6.0.1 | Apache-2.0 | 否 |
| github.com/jcmturner/gokrb5/v8 | v8.4.4 | Apache-2.0 | 否 |
| github.com/jcmturner/rpc/v2 | v2.0.3 | Apache-2.0 | 否 |
| github.com/kisielk/sqlstruct | v0.0.0-20201105191214-5f3e10d3ab46 | MIT | 否 |
| github.com/kr/text | v0.2.0 | MIT | 否 |
| github.com/kylelemons/godebug | v1.1.0 | Apache-2.0 | 否 |
| github.com/microsoft/go-mssqldb | v1.11.2 | BSD-3-Clause | 是 |
| github.com/pkg/browser | v0.0.0-20240102092130-5ac0b6a4141c | BSD-2-Clause | 否 |
| github.com/pmezard/go-difflib | v1.0.0 | BSD-2-Clause | 否 |
| github.com/shopspring/decimal | v1.4.0 | MIT | 是 |
| github.com/stretchr/testify | v1.12.1 | MIT | 否 |
| go.uber.org/goleak | v1.2.0 | MIT | 否 |
| go.uber.org/multierr | v1.10.0 | MIT | 是 |
| go.uber.org/zap | v1.26.0 | MIT | 是 |
| go.yaml.in/yaml/v3 | v3.0.5 | MIT AND Apache-2.0 | 否 |
| golang.org/x/crypto | v0.56.0 | BSD-3-Clause | 是 |
| golang.org/x/mod | v0.38.0 | BSD-3-Clause | 否 |
| golang.org/x/net | v0.58.0 | BSD-3-Clause | 否 |
| golang.org/x/sync | v0.22.0 | BSD-3-Clause | 否 |
| golang.org/x/sys | v0.47.0 | BSD-3-Clause | 否 |
| golang.org/x/term | v0.45.0 | BSD-3-Clause | 否 |
| golang.org/x/text | v0.41.0 | BSD-3-Clause | 是 |
| golang.org/x/tools | v0.48.0 | BSD-3-Clause | 否 |
| gopkg.in/check.v1 | v0.0.0-20161208181325-20d25e280405 | BSD-2-Clause | 否 |
| gopkg.in/natefinch/lumberjack.v2 | v2.2.1 | MIT | 是 |
| gopkg.in/yaml.v3 | v3.0.1 | MIT AND Apache-2.0 | 是 |

## 漏洞检查边界

已将 x/crypto 从 v0.55.0 最小升级至 v0.56.0，以修复 GO-2026-6354 / GO-2026-6355（SSH 包；本项目并未导入该包）。govulncheck 的调用链检查通过不等于整个 module 的所有包安全：GO-2026-5932 涉及其中已废弃且本项目未导入的 openpgp 包，无修复版本。不得为新功能引入该包；持续复核公开漏洞数据库。

## 再分发要求

- 保留依赖版权、许可文本和现有 NOTICE；第三方作者署名是许可义务，不是本项目作者身份。
- MySQL 驱动（MPL-2.0）保持原许可；未修改其源文件。再分发 CLI 二进制时，须同时提供上述声明，并说明可通过 `go mod download github.com/go-sql-driver/mysql@v1.7.1` 获取其对应源代码。公开源代码仓库不等于自动满足未来二进制发布的全部义务。
- module graph 中的 `github.com/hashicorp/go-uuid` 也按 MPL-2.0 保留源代码和许可；本次 CLI 未链接该模块。
- Apache-2.0 依赖保留已有 NOTICE，BSD/MIT 依赖保留署名及免责声明。
- Go 标准库与编译器许可证另见 [Go LICENSE](https://go.dev/LICENSE)；分发二进制时一并附带工具链许可。
- 升级依赖后重新审查：`go mod verify`、`go list -m -json all`、`go list -deps -json ./cmd/migrate`。不能仅凭扫描工具零告警断言没有版权风险。
