# API 契约

> 面向网关调用方与集成方：HTTP 接口、响应头、错误码与流式扩展的完整约定。
> 网关对外保持 OpenAI 兼容的请求/响应形态，客户端无需感知后端多模型接入。

---

## 1. 请求

### 1.1 鉴权

所有受保护路径要求：

```
Authorization: Bearer <vKey>
```

- 无 Bearer 头 → `401`（`missing_api_key`）。
- vKey 无效 → `401`（`invalid_api_key`）。
- 硬件 HAL 指纹不一致（启用时）→ `401`（`self_lockdown`）。

### 1.2 请求体

OpenAI 兼容 `POST /v1/chat/completions`：

```json
{
  "model": "deepseek-v4-flash",
  "stream": true,
  "messages": [
    {"role": "user", "content": "解释零拷贝"}
  ]
}
```

- `model`：必填；需命中该 vKey 的 `model_whitelist`（否则 403）且至少一个上游 `models` 声明支持（否则 400）。
- `stream: true`：SSE 流式响应；`false`/缺省：JSON 响应。

### 1.3 可选请求头（Fallback 相关）

| 头 | 作用 | 说明 |
|---|---|---|
| `X-AI-Gateway-No-Downgrade: true` | **单次调用**禁用模型降级（宁 502 不换模型） | 可选；网关内部处理，**不会转发给上游** |

> 默认情况（不写任何头）：fallback 自动降级按 `config.yaml` 路由配置生效，客户端零额外要求。
> 完整 fallback 机制见 [fallback-design.md](./fallback-design.md)。

---

### 1.4 GET /v1/models（模型清单）

需要 vKey 鉴权（`Authorization: Bearer <vKey>`），返回该 key 可用的模型清单（OpenAI 兼容 `list` 格式）。

```bash
curl -H "Authorization: Bearer sk-xxx" http://localhost:8080/v1/models
```

```json
{"object":"list","data":[{"id":"chat","object":"model","created":1792248725,"owned_by":"gateway"}]}
```

- 配置了 `model_aliases` 时返回**别名**（客户端视角），按该 vKey 白名单过滤（按翻译后的真实名判定）；
- 未配置别名时返回上游声明的模型并集（白名单为空则全部）；
- 无/错 vKey → 401（与其它接口一致）；该端点不落审计（无 token 消耗）。

> 模型别名翻译：客户端 `{"model":"chat"}` 在网关内部翻译为真实模型名后再路由；响应体 `model` 字段保持上游真实名（不回写别名）。

---

## 2. 响应头

所有经网关代理的响应（含被拦截响应）附加：

| 头 | 说明 |
|---|---|
| `X-AI-Gateway-Trace-ID` | 32 位 hex TraceID，全局唯一（crypto/rand 128bit），可用于在审计日志/观测台检索调用链 |
| `X-AI-Gateway-Executed-Plugins` | 实际执行的插件名，逗号分隔，如 `auth_vkey,prompt_injector,security_guard,dynamic_router,auditor` |

**Fallback 模式（`failover`）下附加**：

| 头 | 说明 |
|---|---|
| `X-AI-Gateway-Fallback` | 实际切换链，如 `deepseek→qwen`；未发生切换时无此头 |
| `X-AI-Gateway-Attempts` | 实际尝试次数（含首次），如 `2` |

> 注意：`failover` 自动降级时，响应体 `model` 字段可能与请求的 `model` 不一致（实际使用模型以响应为准，可结合上面两个头核对）。

---

## 3. 错误码一览

| HTTP | 场景 | `error.type` | `error.code` |
|---|---|---|---|
| 400 | 请求模型无任何上游支持 | `invalid_request_error` | `no upstream supports model`（message 内含模型名） |
| 401 | 缺 Bearer / vKey 无效 | `invalid_request_error` | `missing_api_key` / `invalid_api_key` |
| 401 | HAL 硬件指纹不一致 | `self_lockdown` | `hal_mismatch` |
| 403 | 模型不在 vKey 白名单 | `permission_error` | `model_not_allowed` |
| 403 | 命中敏感词 / 防注入 | `security_blocked`（可配置） | `content_filter`（可配置） |
| 404 | 路径无匹配路由 | `invalid_request_error` | `no matching route` |
| 502 | 插件 abort 熔断 / 代理错误 | `plugin_error` / `gateway_proxy_error` | `plugin_aborted` |
| 503 | 上游全部熔断开启 | `gateway_proxy_error` | `circuit breaker open` |

### 错误 JSON 统一形态

```json
{
  "error": {
    "message": "人类可读信息",
    "type": "error_type",
    "code": "error_code"
  }
}
```

被 PreRequest 拦截时同样返回该形态（伪装成标准大模型错误），并带 `X-AI-Gateway-Trace-ID` / `X-AI-Gateway-Executed-Plugins` 头。

---

## 4. 流式响应（SSE）扩展

### 4.1 标准流

上游原生 chunk 逐行透传（`data: {...}`），客户端按 OpenAI SSE 协议解析即可。

### 4.2 网关审计 Chunk（gateway_meta）

在结束符 `data: [DONE]` **之前**注入一个自定义 chunk：

```
data: {"gateway_meta":{"trace_id":"<32hex>","executed_plugins":["auth_vkey",...],"total_tokens":123,"prompt_injected":true}}
```

| 字段 | 说明 |
|---|---|
| `trace_id` | 本次请求 TraceID |
| `executed_plugins` | 已执行插件名列表 |
| `total_tokens` | 总 token（官方计费优先，否则本地预估） |
| `prompt_injected` | 是否发生了部门模板注入 |

### 4.3 异常截断补齐

上游流在 `[DONE]` 前异常中断（网络 EOF / 空闲超时）时，网关会强行补齐：

```
data: {"error":{"message":"upstream stream truncated before [DONE]","type":"stream_truncated","code":"gateway_stream_eof"}}
data: [DONE]
```

确保下游设备绝不陷入接收死锁。空闲超时场景 message 为 `upstream stream idle timeout`。

---

## 5. 非流式响应

OpenAI 兼容 JSON，`usage` 字段按上游原生返回；本地预估仅在无法捕获官方 usage 时用于审计记录，不影响响应体本身。

---

## 6. 内置路径

| 路径 | 说明 |
|---|---|
| `/v1/chat/completions` | 示例路由：类 OpenAI 对话（示例配置中策略为 `latency`） |
| `/v1/iot/analyze` | 示例路由：物联网领域标签路由（示例配置中策略为 `domain_tag`，仅绑定 ernie） |

> 路由路径完全由 `config.yaml` 的 `routers[]` 声明，以上仅为示例配置路径。
