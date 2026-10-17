# 变更日志（Changelog）

本项目遵循 [语义化版本](https://semver.org/lang/zh-CN/)。格式参考 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)。

## [Unreleased]

### Fixed

- **代理阶段错误落审计（backlog #9）**：404 无路由（`no_route`）、502 代理失败（`proxy_error`）、503 全上游熔断（`circuit_open`）此前既不经过 `writeAbort` 也不经过 auditor，审计链路缺一段；现由 `defaultErrorHandler` 复用 `EmitAbortAudit` 落记录。顺带修正：路由层「全部熔断」哨兵 `errAllNodesOpen` 对外统一映射为 503（此前误报 502）。回归测试：`plugins/proxy_audit_test.go`。

## [v0.1.0] - 2025-08-28（首个开源版本）

> 本版本包含内核原型 + 后续全部增强：请求级失败转移、治理能力（vKey 生命周期/限流/配额/拦截审计）、内存防线五层、Agent 宽松模式、内容路由。

### 核心（内核 + 插件 + 工具）

- **内核**：插件管线（`Pipeline`）、多维路由引擎（`Router`）、状态机断路器（`CircuitBreaker`）、反向代理（`ReverseProxy`，含 TCP 背压）、零拷贝字节池（`BufferPool`）、边缘环形落盘（`EdgeStorage`）、看门狗（`WatchdogLoop`）、配置热加载（不可变快照原子交换）。
- **插件**：`auth_vkey`（鉴权 + HAL 指纹预留）、`prompt_injector`（部门模板注入 + 模型白名单）、`security_guard`（敏感词 DLP + 防注入）、`dynamic_router`（Director 阶段路由 + 凭证替换 + 百度换钥）、`auditor`（SSE 流式审计 + 官方计费覆盖 + 截断补齐）、`model_routing`（内容感知定档）。
- **路由策略**：`round_robin` / `least_connection` / `latency`（EMA）/ `cost` / `domain_tag`，含模型感知强约束。
- **工具**：`check`（Doctor 诊断）、`bench`（压测：QPS/成功率/时延分级/内存膨胀/连接残留）、`keygen`（vKey 生成）、`serve -mock`（内置 Mock 闭环）、`version`。
- **观测**：`obs/` 只读观测台（汇总卡 / 成本分布 / 审计表 / 拦截与 fallback 标记 / 调用链时间线）。
- **配置**：零依赖手写 YAML 子集解析 + 类型化加载 + `Validate` 静态审计。

### 请求级失败转移（fallback，M1-M3）

- `config`：路由级 `fallback` 配置（mode / max_attempts / total_timeout_ms / connect_retry / retry_on / transparent_header / model_map）+ 静态校验。
- `kernel`：`RouteCandidates` 候选链、（上游,模型）二元组、`classifyFailure` 失败分类（4xx/429/5xx/timeout/connect）、`fallbackPlan` 循环尝试引擎（状态码分流 / connect 同节点重试 / retry_on 过滤 / max_attempts 截断 / 总超时预算 / 每尝试重放 Rewrite 与 body）。
- **model_map 降档**：降级候选追加链尾，运行期过 vKey 白名单；改写 body `model` 字段并同步 `ContentLength`。
- **透明头**：`X-AI-Gateway-Fallback` / `X-AI-Gateway-Attempts`（failover 模式）。
- **熔断统计补全**：429 计入失败；mid-stream 断流计入失败（区分客户端断开）。
- **审计字段**：`fallback_chain` / `attempts` / `chain_truncated` / `stop_reason`。
- **故障注入**：`mockup.NewFaultServer`（500 / 429 / 连接重置 / 慢响应 / 流截断）+ 故障注入矩阵测试。
- 默认 `fail_fast` 保持历史行为不变（向后兼容）。

### 治理能力

- **vKey 生命周期（R1）**：`keygen` 生成随机 key + `disabled` 吊销；不做 HMAC 无状态签发、过期可选（[docs/backlog.md](docs/backlog.md) 1.6）。
- **限流与配额**：`auth.keys[].rate_limit`（令牌桶 QPS）、`quota_daily`（每日 token 配额，跨重启落盘 `quota.json`）；超限 429 且落审计。
- **拦截请求落审计**：`kernel.EmitAbortAudit` 为 401/403/429/413/502 落审计（`http_status` / `success` / `abort_reason`）。
- **stdout 开关统一**到内核（`kernel.SetAuditStdout`）。

### 内存防线（五层）

1. **请求体上限** `transport.max_request_body_bytes`（`ReadBody` 限额 + auth_vkey 早检，413 落审计）。
2. **非流式响应审计缓冲上限** `transport.max_audit_body_bytes`（超限停止缓冲、仍完整转发、usage 降级 estimated）。
3. **并发准入** `capacity.max_inflight`（信号量，满则 429 `busy` 快速失败不排队）。
4. **内存水位熔断**（`capacity.mem_soft_percent/hard_percent/hysteresis_samples`，cgroup 感知；软阈值降并发、硬阈值拒绝、迟滞防抖）。
5. **可观测**：`GET /metrics`（in-flight / 上限 / 软硬模式 / 阈值）+ 启动日志打印容量推导值。

配套：`http.Server` 补 `ReadHeaderTimeout` / `ReadTimeout` / `IdleTimeout` / `MaxHeaderBytes`（`WriteTimeout` 不设以免打断 SSE）。

### Agent 宽松模式（docs/agent-mode.md）

- 标识：`auth.keys[].agent_mode` + `keygen -agent`。
- 干预为零：不改写模型（内容路由跳过）、不注入模板、不降档（剔除 model_map）、TTFT 放大（懒克隆 Transport 改 `ResponseHeaderTimeout`）、body 上限可放大。
- 治理保留：审计全量（含 `agent_mode` 字段）、配额照常、安全仅敏感词 DLP（跳过注入正则）。
- `agent_gateway_meta: false` 默认不注入 gateway_meta（兼容严格 SSE 解析器）。

### 内容路由（阶段 1，docs/content-routing.md）

- `model_routing` 插件：按复杂度（token 阈值 + 关键词）选便宜档/推理档并改写 `CtxModel`；**选择时保证白名单**（选择域 = 意图档位 ∩ 用户可用模型，不做事后回退）；非对话请求跳过。

### 文档与工程化

- 文档体系 `docs/`（架构 / 插件 / 配置参考 / API 契约 / 审计格式 / 协议适配 / 部署 / 性能基线 / 发布清单 / backlog / roadmap / fallback-design / content-routing / cost-optimization / agent-mode / model-alias）。
- 容器化：`Dockerfile`（多阶段静态构建）、`docker-compose.yml`、`.dockerignore`。
- 工程化：`Makefile`、`.editorconfig`、Issue/PR 模板、`SECURITY.md`、`MAINTAINERS.md`、CI（build/vet/test）。
- 安全基线：`config.example.yaml` 扩充防注入清单（保守子集启用、激进子集注释留档）+ 敏感词表。

### 修复

- CI：`obs` 作业的 `cache-dependency-path` 从 `obs/go.sum` 修正为 `obs/go.mod`（零依赖模块无 go.sum）。
- `bench`：正常用例模型与 `config.example.yaml` 对齐（此前直接跑示例配置会 100% 403/400）。
- **多模态 content 数组被覆盖**：`injectTemplate` 改写首个 text 部件、保留 image_url/audio；无 user 消息时追加 user 消息而非误改 system。
- **多模态文本提取缺失**：`extractPromptText` 兼容 `string` 与 `[]any` 两种 content 形态（DLP 覆盖文件/图片文本）。
- **正则重复编译**：`config.Security` 新增 `SensitiveRegexps()` / `InjectionRegexps()`（`sync.Once` 惰性预编译）。
- **多尝试 body 耗尽**：failover 每次尝试重放原始请求体。
- **连接失败不切换**：`connect_retry=0` 时连接级失败仍按 `retry_on: connect` 切换上游。

### 已知限制

- 正常代理记录缺上游 `http_status`（拦截/拒绝记录已有）。
- `routers[].domain_tags` 仅解析未参与匹配。
- 非 OpenAI 路径未做显式 `bind_paths` 插件边界。
- 百度是唯一内置的非标协议适配。
- 多实例下限流/配额为进程内近似计数。
- 端侧算法分发（`X-Algorithm`）未实现。

详见 [README 已知限制](README.zh.md#已知限制与待办) 与 [docs/backlog.md](docs/backlog.md)。
