# 审计日志格式（uba.json）

> 网关把每次代理请求的审计记录异步追加到日志文件（默认 `logs/uba.json`，路径见 `log.path` 与环境自适应规则），
> 同时可选异步上报 `edge_storage.report_url`。观测台 `obs/` 直接消费本格式。
> 文件为 JSON-lines：每行一条记录，`\n` 分隔。

---

## 1. 字段说明

| 字段 | 类型 | 说明 |
|---|---|---|
| `trace_id` | string | 32 位 hex TraceID，与响应头 `X-AI-Gateway-Trace-ID` 一致 |
| `timestamp` | string | RFC3339Nano 时间戳（审计完成时刻） |
| `username` | string | vKey 对应的用户名（未通过鉴权为 `""`） |
| `department` | string | vKey 对应的部门 |
| `model` | string | 请求体中的模型名 |
| `upstream` | string | 本次实际选中的上游节点名 |
| `prompt_injected` | bool | 是否发生部门模板注入 |
| `security_check` | string | `PASSED` / `BLOCKED_SENSITIVE` / `BLOCKED_INJECTION` |
| `prompt_tokens` | int64 | prompt token 数 |
| `completion_tokens` | int64 | completion token 数 |
| `total_tokens` | int64 | 总 token 数 |
| `cached_tokens` | int64 | 国产模型 Prompt Cache 命中数（`usage.prompt_tokens_details.cached_tokens`） |
| `precision` | string | 计费精度：`official_cached`（官方含缓存）/ `official`（官方）/ `estimated`（本地预估） |
| `executed_plugins` | string[] | 实际执行插件名列表 |
| `duration_ms` | int64 | 请求端到端耗时（毫秒） |
| **`agent_mode`** | bool | 是否 agent 宽松模式 key（成本归属用） |
| **`fallback_chain`** | string[] | 失败转移实际尝试过的上游序列（failover 模式） |
| **`attempts`** | int64 | 实际 RoundTrip 尝试次数 |
| **`chain_truncated`** | bool | 是否因 `max_attempts` 截断（还有候选未尝试） |
| **`stop_reason`** | string | 停止原因：`exhausted` / `timeout`（成功时为空） |
| **`http_status`** | int | **仅拦截/拒绝记录**：401/403/429/413/502 等 |
| **`success`** | bool | **仅拦截/拒绝记录**：恒为 `false`（正常代理记录缺省此字段） |
| **`abort_reason`** | string | **仅拦截/拒绝记录**：如 `invalid_api_key` / `content_filter` / `model_not_allowed` / `rate_limited` / `quota_exceeded` / `key_disabled` / `busy` / `memory_hard` / `request_too_large` |

> 两类记录：**正常代理记录**（auditor 产出）与**拦截/拒绝记录**（`kernel.EmitAbortAudit` 产出）。后者额外带 `http_status`/`success`/`abort_reason`，且 token 字段为 0、`precision` 为空。

## 2. 示例记录

```json
{
  "trace_id": "a1b2c3d4e5f60718293a4b5c6d7e8f90",
  "timestamp": "2025-08-15T21:53:00.123456789+08:00",
  "username": "张三",
  "department": "研发部",
  "model": "deepseek-v4-flash",
  "upstream": "deepseek",
  "prompt_injected": true,
  "security_check": "PASSED",
  "prompt_tokens": 42,
  "completion_tokens": 6,
  "total_tokens": 48,
  "cached_tokens": 18,
  "precision": "official_cached",
  "executed_plugins": ["auth_vkey", "prompt_injector", "security_guard", "dynamic_router", "auditor"],
  "duration_ms": 486
}
```

---

## 3. 计费精度（precision）语义

| 值 | 含义 |
|---|---|
| `official_cached` | 捕获到上游官方 usage，且 `cached_tokens > 0`（国产模型 Prompt Cache 命中） |
| `official` | 捕获到上游官方 usage，无缓存命中 |
| `estimated` | 未捕获官方 usage，采用本地预估（CJK 每字符 1 token，拉丁每 4 字符 1 token） |

## 4. 成本计算（obs 口径）

```
cost = total_tokens / 1000 × upstreams[].cost_per_1k
```

费率从 `config.yaml` 读取；未配置费率的上游 `rate_known=false`。

---

## 5. 已知限制（与审计相关的现状）

1. **缺 `http_status` / `success` 字段**：上游 401/5xx 与 200 在日志中无法区分（观测台也因此把失败渲染成 PASSED 绿色）。治本需给 auditor 补字段。
2. **被 PreRequest 拦截的请求不落审计**：auth/security 的 401/403 到不了 auditor，不产生审计记录。
3. **审计数据包含用户 Prompt 相关元数据**：`original_prompt` / `final_prompt` 目前在 Context 中，但**未写入审计记录**（记录仅含 token 数等聚合值）；若未来开启 Prompt 内容落盘，请务必评估隐私合规。
