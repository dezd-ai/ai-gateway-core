# 发布检查清单（Release Checklist）

> 用途：维护者每次发版前逐项核对的流程清单。**v0.1.0 已于 2026-10-14 发布**，本清单作为后续版本（v0.2.0+）的发布模板。
> 关联：[roadmap.md](./roadmap.md) · [SECURITY.md](../SECURITY.md) · [CHANGELOG.md](../CHANGELOG.md) · [CONTRIBUTING.md](../CONTRIBUTING.md)

---

## 1. 仓库与元数据（一次性配置，当前已就绪）

| # | 项 | 状态 | 说明 |
|---|---|---|---|
| 1.1 | 仓库地址与平台 | ✅ 已就绪 | GitHub · `dezd-ai/ai-gateway-core`，默认分支 `main` |
| 1.2 | 模块路径 | ✅ 已就绪 | `go.mod` / `obs/go.mod` 均为 `github.com/dezd-ai/ai-gateway-core`，build/vet/test/gofmt 全绿 |
| 1.3 | LICENSE 署名 | ✅ 已就绪 | Apache-2.0 正文完整，版权行为 `Copyright 2026 dezd-ai` |
| 1.4 | README 徽章 | ✅ 已就绪 | CI / License / Go / 零依赖，中英双语均含 |
| 1.5 | 平台元数据 | ✅ 已就绪 | Description、Topics（10 个）、Discussions、About 均已在 GitHub 配置 |
| 1.6 | Release 流程 | ✅ 已就绪 | 手动发布：打 tag → GitHub Release 引用 CHANGELOG 段落（命令见第 5 节） |

## 2. 发布前：代码与配置卫生

| # | 项 | 状态 | 说明 |
|---|---|---|---|
| 2.1 | `.gitignore` 覆盖 | ✅ 已就绪 | `config.yaml`（可能含密钥）、`logs/`、`bin/`、`obs/obs`、`.vscode/`、内部工作文档均在忽略列表 |
| 2.2 | 无真实密钥入库 | ✅ 已就绪 | `config.example.yaml` 走环境变量；示例 key 均为假值；全历史扫描无 `sk-*` / token / 私钥痕迹 |
| 2.3 | 无 TODO/FIXME 残留 | ✅ 已就绪 | 全局扫描通过 |
| 2.4 | 版本号注入 | ✅ 已就绪 | `main.go` 的 `version` 变量 + `version` 子命令 + `Makefile` 的 `-ldflags -X main.version=` |

## 3. 发布前：质量验证（每次发版必跑）

| # | 项 | 命令 | 状态 |
|---|---|---|---|
| 3.1 | 构建 | `go build ./...` | ✅ 通过 |
| 3.2 | 静态检查 | `go vet ./...` + `cd obs && go vet ./...` | ✅ 通过 |
| 3.3 | 单元/集成测试 | `go test ./...` | ✅ 通过（16 个测试文件） |
| 3.4 | 格式检查 | `gofmt -l .`（CI 强制拦截） | ✅ 通过 |
| 3.5 | 真实压测基线 | `./bin/ai-gateway-core bench -duration 5s -workers 50` | ✅ 数据见 [benchmark.md](./benchmark.md) |
| 3.6 | 端到端冒烟（Mock 闭环） | `serve -mock` + 两条 curl + `/metrics` | ✅ 通过（正常流式 / 403 / 401 / metrics） |
| 3.7 | Docker 闭环 | `docker build -t ai-gateway-core .` + `docker run … serve -mock` | ✅ 通过（镜像约 16MB，流式 chat 正常） |
| 3.8 | 文档命令可执行性 | 逐条跑 README/docs 里的命令 | ✅ 已核对（含 keygen / version / metrics） |

## 4. 文档完备性

| # | 文档 | 状态 |
|---|---|---|
| 4.1 | README（状态矩阵 / 目录结构 / 快速开始 / 已知限制 / Roadmap） | ✅ 与当前实现一致 |
| 4.2 | docs/ 系列（架构 / 插件 / 配置 / API / 审计 / 协议 / 部署 / 基线） | ✅ |
| 4.3 | **中英双语**：README + 核心 4 篇（架构 / 插件 / 配置参考 / API）+ obs 说明 | ✅ 语言切换互链 |
| 4.4 | 观测台（obs）定位说明：**只读、非管理端**（网关无 Web 管理端，配置只走 config.yaml） | ✅ |
| 4.5 | 示例审计数据 `examples/uba.json`（clone 后即可看板，无需运行网关） | ✅ 覆盖正常/拦截/failover/agent |
| 4.6 | obs 配置回退：`config.yaml` 缺失时自动回退 `config.example.yaml` | ✅ |
| 4.7 | SECURITY.md / CONTRIBUTING.md / CODE_OF_CONDUCT.md / MAINTAINERS.md / CHANGELOG.md | ✅ |
| 4.8 | Issue / PR 模板 · Dockerfile / docker-compose / Makefile | ✅ |

## 5. 发布动作

```bash
# 1) 确认 main 干净且 CI 绿
git status
gh run list -L 1

# 2) 打 tag 并推送
git tag -a vX.Y.Z -m "vX.Y.Z: 一句话摘要"
git push origin main --tags

# 3) 创建 GitHub Release（说明引用 CHANGELOG 对应段落）
gh release create vX.Y.Z \
  --title "vX.Y.Z · 摘要" \
  --notes-file <(sed -n '/## \[vX.Y.Z\]/,/^## \[/p' CHANGELOG.md | head -n -1)

# 4) 发布后核对：仓库可见性、Release 状态、私密漏洞报告可用
gh repo view dezd-ai/ai-gateway-core --json visibility
gh api repos/dezd-ai/ai-gateway-core/private-vulnerability-reporting
```

## 6. 发布后：社区运营（规划项，非阻塞）

| # | 项 | 说明 |
|---|---|---|
| 6.1 | 技术拆解文章 | 零拷贝流式审计 / 国产计费精度覆盖 / fallback 设计 |
| 6.2 | SDK 兼容性 CI | ✅ 已落地：`.github/workflows/sdk-compat.yml`（openai-python / openai-node 直连网关，覆盖 /v1/models + 别名 + 流式/非流式） |
| 6.3 | 发布节奏 | 2-4 周一个版本，CHANGELOG 同步 |
| 6.4 | 预置模板 | DeepSeek / Qwen 一键 `docker compose up` |
