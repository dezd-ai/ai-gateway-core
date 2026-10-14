# 维护者（MAINTAINERS）

## 维护者列表

| 维护者 | 联系方式 | 职责 |
|---|---|---|
| [@dezd-ai](https://github.com/dezd-ai) | GitHub（见下方渠道） | 内核架构、路线图决策、Release 管理 |

## 联系方式（推荐渠道）

本项目**不公开个人邮箱**（提交记录使用 GitHub noreply 地址）。请通过以下渠道联系：

| 场景 | 渠道 |
|---|---|
| 使用问题、功能建议 | [GitHub Issues](https://github.com/dezd-ai/ai-gateway-core/issues) |
| 社区讨论、经验分享 | [GitHub Discussions](https://github.com/dezd-ai/ai-gateway-core/discussions) |
| **安全漏洞（私密报告）** | 仓库 **Security → Report a vulnerability**（仅维护者可见，无需邮箱）；详见 [SECURITY.md](SECURITY.md) |
| 直接提及维护者 | 在 Issue / PR 中 `@dezd-ai` |

> 若需邮件沟通（如企业合作），请在 Issue 中说明，维护者会通过 GitHub 私信回复。

## 决策流程

- **日常变更**：任何贡献者提交 PR，至少 1 名维护者 review 后合入。
- **架构性变更**（内核接口、插件 API、配置格式）：需维护者讨论达成一致，并在 CHANGELOG 记录。
- **版本发布**：由维护者打 tag，遵循语义化版本（SemVer）。

## 职责分工（建议）

- **内核维护**：`kernel/` 与 `config/` 的稳定性、并发安全、性能。
- **插件维护**：`plugins/` 内置插件的行为与兼容性。
- **生态维护**：`docs/`、`obs/`、Issue 响应、社区答疑。

## 成为维护者

持续贡献者（如定期 review、修复关键缺陷、主导功能模块）可向现有维护者申请，经一致同意后加入本列表。
