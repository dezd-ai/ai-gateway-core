# 开源发布检查清单（Release Checklist）

> 用途：首次开源发布与后续版本发布的**逐项核对表**。每项标注当前状态。
> 关联：[README 已知限制](../README.md#已知限制与待办) · [roadmap.md](./roadmap.md) · [SECURITY.md](../SECURITY.md)

---

## 1. 仓库与元数据（首次发布必做）

| # | 项 | 状态 | 说明 |
|---|---|---|---|
| 1.1 | 确定仓库地址与平台 | ✅ 已完成 | **GitHub · `DezDing/ai-gateway-core`** |
| 1.2 | 替换模块路径 | ✅ 已完成 | `go.mod` + `obs/go.mod` + 31 个 Go/MD 文件共 58 处；替换后 build/vet/test/gofmt 全绿 |
| 1.3 | `git init` + 首次提交 | 🔨 待执行 | 命令见第 5 节 |
| 1.4 | LICENSE 版权署名 | ⏸️ 保持模板占位 | 按决定暂不填（Apache-2.0 正文完整，仅署名待补） |
| 1.5 | README 徽章（CI / License / Go / 零依赖） | ✅ 已完成 | 中英双语 README 均已加 |
| 1.6 | 平台元数据（描述、Topics、About） | 🔨 待执行 | 建议 Topics：`ai-gateway` `llm-gateway` `go` `llm` `reverse-proxy` `zero-dependency` |
| 1.7 | Release 工作流（打 tag 自动出二进制） | ⏸️ 后续 | 首次可手动发布 |

## 2. 发布前代码与配置清理（已完成 ✅）

| # | 项 | 状态 | 说明 |
|---|---|---|---|
| 2.1 | `.gitignore` 补漏 | ✅ 已完成 | 加 `obs/obs`（7.6MB 二进制）、内部文档 |
| 2.2 | 内部工作文档不发布 | ✅ 已完成 | `宇宙终极完全体技术规格书.txt` 已 gitignore（AI 提示词式内部文档） |
| 2.3 | 无真实密钥入库 | ✅ 已完成 | `config.yaml` gitignore；`config.example.yaml` 走环境变量；示例 key 均为假值 |
| 2.4 | 无 TODO/FIXME 残留 | ✅ 已完成 | 全局扫描通过（M2 TODO 已随 fallback 实现移除） |
| 2.5 | 版本号注入 | ✅ 已完成 | `main.go` 的 `version` 变量 + `version` 子命令 + Makefile `-ldflags -X main.version=` |

## 3. 质量验证（每次发布前重跑）

| # | 项 | 命令 | 状态 |
|---|---|---|---|
| 3.1 | 构建 | `go build ./...` | ✅ 通过 |
| 3.2 | 静态检查 | `go vet ./...` + `cd obs && go vet ./...` | ✅ 通过 |
| 3.3 | 单元/集成测试 | `go test ./...` | ✅ 通过（16 个测试文件） |
| 3.4 | 真实压测基线 | `./bin/ai-gateway-core bench -duration 5s -workers 50` | ✅ 数据见 [benchmark.md](./benchmark.md) |
| 3.5 | 端到端冒烟（Mock 闭环） | `serve -mock` + 两条 curl + `/metrics` | ✅ 通过（正常流式 / 403 / 401 / metrics） |
| 3.6 | 文档命令可执行性 | 逐条跑 README/docs 里的命令 | ✅ 已核对（含 keygen / version / metrics） |

## 4. 文档完备性（✅ 已完成）

| # | 文档 | 状态 |
|---|---|---|
| 4.1 | README（状态矩阵 / 目录结构 / 快速开始 / 已知限制 / Roadmap） | ✅ 已更新至当前实现 |
| 4.2 | docs/ 15 篇（架构/插件/配置/API/审计/协议/部署/基线/各设计稿） | ✅ |
| 4.3 | SECURITY.md（含安全限制与报告渠道） | ✅ |
| 4.4 | CONTRIBUTING.md（贡献流程 + 扩展指南） | ✅ |
| 4.5 | CODE_OF_CONDUCT.md / MAINTAINERS.md / CHANGELOG.md | ✅ |
| 4.6 | Issue / PR 模板 | ✅ |
| 4.7 | Dockerfile / docker-compose / Makefile | ✅ |

## 5. 发布动作（地址确定后执行）

```bash
# 1) 替换模块路径（先确认真实地址）
#    go.mod + 所有 import + 文档引用
# 2) 初始化仓库
git init && git add -A && git commit -m "feat: AI-Gateway-Core v0.1.0 首个开源版本"
# 3) 打 tag（触发 release 工作流，若已配置）
git tag -a v0.1.0 -m "v0.1.0: 内核 + 6 插件 + fallback + 治理能力 + 内存防线 + Agent 模式"
git push origin main --tags
# 4) 发布说明：直接引用 CHANGELOG 的 v0.1.0 段
```

## 6. 发布后（社区运营）

| # | 项 | 说明 |
|---|---|---|
| 6.1 | 技术拆解文章 | 零拷贝流式审计 / 国产计费精度覆盖 / fallback 设计（中文技术社区传播力强） |
| 6.2 | SDK 兼容性 CI | 用 openai-python / openai-node 直连网关跑通（最易验证的信任点） |
| 6.3 | 发布节奏 | 2-4 周一个版本，CHANGELOG 同步 |
| 6.4 | 预置模板 | DeepSeek/Qwen 一键 `docker compose up` |

---

## 当前阻塞点

> **唯一阻塞项：仓库地址未定（1.1）**。它连带阻塞 1.2 / 1.3 / 1.5 / 1.6 —— 其余所有发布准备工作均已完成。
