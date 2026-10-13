# Agent 宽松模式设计（Agent Mode）

> **状态**：✅ 已实现（2025-08）。定位遵循 roadmap 0.1「治理层不干预」原则。
> **目标**：Agent/Harness 流量经网关时——**治理可见（审计/成本/配额），干预为零（不换模型、不注入、不掐超时）**。

---

## 1. 背景与原则

Agent/Harness（长推理、工具调用、自适应重试）成为主流 AI 消费方。网关若对 agent 流量做"模型决策类干预"（降级/换模型/超时掐断/内容注入），会直接破坏 agent 的先进能力 → 被绕过。

**原则**：agent 流量 = 只做治理（审计/配额/安全底线），不做干预（模型/内容/超时）。

---

## 2. 标识：Agent Key（分配时标记）

agent 流量用**专用 vKey**，在 `auth.keys[]` 里声明 `agent_mode: true`，身份绑定、客户端不可自封：

```yaml
auth:
  keys:
    - vkey: "sk-agent-xxx"
      agent_mode: true            # ← 该 key 是 agent 专用
      quota_daily: 50000000       # 配额保留，默认配大值减影响
      model_whitelist: ["deepseek-v4-flash", "deepseek-reasoner"]
```

- 生成：`keygen` 增加 `-agent` 标志（或手写配置）；
- 权威来源 = vKey 配置；**不提供**客户端请求头自封（防普通请求伪装成 agent 绕过约束）。

---

## 3. 行为矩阵（agent_mode=true 时的差异）

| 环节 | 正常模式 | Agent 宽松模式 | 理由 |
|---|---|---|---|
| 内容/时间路由（改写 model） | 生效 | **禁用** | 模型由 agent 完全决定 |
| fallback 降档（model_map） | 生效 | **禁用降档**（同模型切上游保留） | 不悄悄换模型 |
| 提示词注入（injector 模板） | 生效 | **跳过** | agent 上下文不容污染 |
| 安全扫描 | 敏感词 + 注入正则 | **仅敏感词 DLP**（跳过注入正则） | 安全底线保留，避免误伤工具调用内容 |
| TTFT 首字超时 | 10s（transport 配置） | **放大**（`agent_ttft_timeout_sec`，建议 120s） | 推理长思考 |
| 请求体上限 | `max_request_body_bytes` | **放大**（`agent_max_request_body_bytes`） | 长上下文/工具累积 |
| 限流 rate_limit | 按配置 | 默认不设（0）或大值 | agent 高频 |
| 配额 quota_daily | 按配置 | **保留**（默认配大值） | 成本治理核心 |
| SSE gateway_meta 注入 | 注入 | **默认关闭**（`agent_gateway_meta: false`，可配置开） | 严格解析器兼容 |
| 审计 | 全量 | **全量保留** | 治理核心 |

---

## 4. 配置草案

```yaml
# auth.keys[] 增加字段
auth:
  keys:
    - vkey: "sk-agent-xxx"
      agent_mode: true

# 新增 agent 全局参数（随 transport 或独立 agent 块）
transport:
  agent_ttft_timeout_sec: 120          # agent 首字超时放大
  agent_max_request_body_bytes: 0      # agent body 上限；0 = 沿用全局（非"不限"）
                                       #   全局 0（不限）→ agent 也不限；
                                       #   全局配了上限 → agent 默认同全局，需放大再单独配
  agent_gateway_meta: false            # agent 模式是否注入 gateway_meta（默认关）
```

---

## 5. 实现落点（改动面预估）

| 落点 | 改动 |
|---|---|
| `config` | `AuthKey.AgentMode` + `agent_*` 参数 + Validate |
| `config.example.yaml` / `keygen` | `-agent` 标志 + 示例 |
| `plugins/injector.go` | agent key → 跳过模板注入（仍提取 model 供白名单/路由） |
| `plugins/model_routing.go` | agent key → 不改写 CtxModel |
| `plugins/security_guard.go` | agent key → 仅敏感词，跳过注入正则 |
| `kernel/fallback.go` | agent key → 候选链剔除 model_map 降级条目（同模型保留） |
| `kernel/gateway.go` | agent 请求早检用放大 body 上限 |
| `kernel/proxy.go` | agent 请求用 context deadline 放大 TTFT（**方案①：不动共享 Transport**，类似 fallback deadline 机制） |
| `plugins/auditor.go` | agent 模式默认不注入 gateway_meta（可配置开） |

---

## 6. 已定决策（本轮确认）

| 决策点 | 结论 |
|---|---|
| 安全扫描 | **保留，仅敏感词 DLP**（跳过注入正则） |
| 标识 | **分配 key 时标记** `agent_mode: true`（+ keygen `-agent`），无客户端自封 |
| 配额 | **保留 quota_daily**，默认配大值减影响 |
| 内容/时间路由 | 禁用（agent key 不改写模型） |
| fallback | 禁用 model_map 降档，同模型切上游保留 |
| 注入模板 | 跳过 |
| SSE gateway_meta | **默认关**（`agent_gateway_meta: false`），可配置开（初稿默认，待确认） |
| agent body 上限 | **默认 0 = 沿用全局**（非独立上限；全局 0=不限则 agent 也不限） |

---

## 7. 开放问题（实现前确认）

1. **TTFT 方案①（context deadline 包裹）**是否认可？——不动共享 Transport，改动最小（我推荐）；
2. ~~agent 请求体放大默认值~~ ✅ 已定：默认 0 = 沿用全局；
3. **agent 专属路由**：agent 是否也走 `/v1/chat/completions` 同入口（我认为是，按 key 区分即可），还是需要独立入口 path？
4. **audit 标记**：审计记录是否加 `agent_mode: true` 字段（便于成本归属按 agent 统计，我倾向加）。

---

## 8. 关联

- 定位依据：`roadmap.md` 0.1「Agent/Harness 时代的定位」
- 冲突原则：不干预（与内容路由/时间路由/fallback 降档的"干预型"能力对立，按 key 隔离）
- 成本治理：`docs/backlog.md` 1.6（quota_daily）
