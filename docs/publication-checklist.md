# 发布前人工门禁

静态检查、单测、构建及秘密扫描通过，不自动授权公开。

- [ ] 确认纳入代码及合成样例的公开权利；内部资料/业务数据未纳入。
- [ ] 确认历史凭据使用范围：仍使用或复用的口令需必要轮换并确认旧口令失效；仅用于本人可销毁本地测试且未复用的，可记录明确确认及豁免理由。不得把脱敏当作轮换或失效证明。
- [ ] 对工作树和全部本地 refs 做检查，人工复核工具的命中与例外。
- [ ] 确认完整 LICENSE、依赖许可及 NOTICE、源代码获取说明随相应分发提供。
- [ ] 核对默认离线测试、vet、脚本回归、安全扫描及构建的已有证据与适用范围；用户明确免重测时记录豁免范围，将未执行项标为“未重跑”，不得改写为“通过”。
- [ ] 核对旧模式破坏性合同、私有配置覆盖和未验证能力的公开说明。
- [ ] 确认远端全部相关 refs、平台缓存和旧 clone 的历史清理方案；本地重写不代表服务器已清理。
- [ ] 最终审核拟提交快照和提交身份；不得把私有回滚备份、日志或扫描原始结果放入公开仓库。
- [ ] 获得创建远端、推送或公开仓库的独立授权。

本地检查（以下是可用命令，不表示本轮已执行；实际验收状态见 [整改状态](remediation-status.md)）：

```bash
python3 scripts/check_publication.py --history
# 首次公开前可核对当前单一作者历史；公开后不以此阻断合法贡献者/平台合并提交：
python3 scripts/check_publication.py --history \
  --expected-author-name zhongyuming --expected-author-email puppetdevzz@gmail.com
# 客户/组织标识清单必须保存在仓库外的私有 JSON 字符串数组中，不写入公开代码或 CI：
python3 scripts/check_publication.py --history --forbidden-patterns /private/organization-policy.json
# 导出时只复制审查范围内、非忽略的文件，不带 .git 或私有本地配置
python3 scripts/check_publication.py --snapshot /tmp/new-public-snapshot
# 使用固定版本 gitleaks，并让报告写到仓库外受控目录：
gitleaks git --log-opts=--all --config .gitleaks.toml --redact=100 --no-banner
gitleaks dir /tmp/new-public-snapshot --config .gitleaks.toml --redact=100 --no-banner
```

`check_publication.py` 默认检查路径、常见私钥/token 和配置口令；提交身份核对是显式选项，组织标识只在显式提供私有策略时检查，也可通过 PUBLICATION_FORBIDDEN_PATTERNS 注入 JSON。公开代码不内置真实客户标识，通用 CI 不加载私有策略。它是启发式保护，不证明没有其它编码的秘密、不证明代码权属，也不替代凭据确认。第三方许可证中的原版权姓名/邮箱须保留，不与本项目作者身份混淆。

缺任一必要外部确认时状态必须是 **BLOCKED_FOR_PUBLICATION**。通过全部门禁后才可标记 **READY_FOR_MANUAL_PUBLICATION**，仍不能据此自动推送或发 Release。
