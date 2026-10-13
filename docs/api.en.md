# API Contract
**English** | [中文](api.md)

> For gateway callers and integrators: the complete contract for HTTP endpoints, response headers, error codes, and streaming extensions.
> The gateway keeps an OpenAI-compatible request/response shape, so clients need not be aware of multi-model backend integrations.

---

## 1. Request

### 1.1 Authentication

All protected paths require:

```
Authorization: Bearer <vKey>
```

- No Bearer header → `401` (`missing_api_key`).
- Invalid vKey → `401` (`invalid_api_key`).
- Hardware HAL fingerprint mismatch (when enabled) → `401` (`self_lockdown`).

### 1.2 Request Body

OpenAI-compatible `POST /v1/chat/completions`:

```json
{
  "model": "deepseek-v4-flash",
  "stream": true,
  "messages": [
    {"role": "user", "content": "explain zero-copy"}
  ]
}
```

- `model`: required; must hit the `model_whitelist` of that vKey (otherwise 403) and be declared as supported by at least one upstream `models` (otherwise 400).
- `stream: true`: SSE streaming response; `false`/omitted: JSON response.

### 1.3 Optional Request Headers (Fallback-related)

| Header | Effect | Notes |
|---|---|---|
| `X-AI-Gateway-No-Downgrade: true` | Disables model downgrade for a **single call** (prefer 502 over switching models) | Optional; handled inside the gateway and **not forwarded upstream** |

> Default case (no headers set): automatic fallback downgrade takes effect according to the routing configuration in `config.yaml`, with zero extra requirements on clients.
> For the full fallback mechanism, see [fallback-design.md](./fallback-design.md).

---

## 2. Response Headers

All responses proxied through the gateway (including blocked responses) are augmented with:

| Header | Description |
|---|---|
| `X-AI-Gateway-Trace-ID` | 32-character hex TraceID, globally unique (crypto/rand 128bit), usable to retrieve the call chain in audit logs / the observability console |
| `X-AI-Gateway-Executed-Plugins` | Names of the plugins actually executed, comma-separated, e.g. `auth_vkey,prompt_injector,security_guard,dynamic_router,auditor` |

**Added under fallback mode (`failover`)**:

| Header | Description |
|---|---|
| `X-AI-Gateway-Fallback` | The actual switch chain, e.g. `deepseek→qwen`; this header is absent when no switch occurred |
| `X-AI-Gateway-Attempts` | The actual number of attempts (including the first), e.g. `2` |

> Note: during `failover` automatic downgrade, the `model` field in the response body may differ from the requested `model` (the model actually used is authoritative in the response; cross-check it with the two headers above).

---

## 3. Error Codes at a Glance

| HTTP | Scenario | `error.type` | `error.code` |
|---|---|---|---|
| 400 | Requested model is not supported by any upstream | `invalid_request_error` | `no upstream supports model` (message contains the model name) |
| 401 | Missing Bearer / invalid vKey | `invalid_request_error` | `missing_api_key` / `invalid_api_key` |
| 401 | HAL hardware fingerprint mismatch | `self_lockdown` | `hal_mismatch` |
| 403 | Model not in the vKey whitelist | `permission_error` | `model_not_allowed` |
| 403 | Sensitive-word hit / anti-injection | `security_blocked` (configurable) | `content_filter` (configurable) |
| 404 | No matching route for the path | `invalid_request_error` | `no matching route` |
| 502 | Plugin abort circuit break / proxy error | `plugin_error` / `gateway_proxy_error` | `plugin_aborted` |
| 503 | All upstreams have circuit break enabled | `gateway_proxy_error` | `circuit breaker open` |

### Unified Error JSON Shape

```json
{
  "error": {
    "message": "human-readable message",
    "type": "error_type",
    "code": "error_code"
  }
}
```

When blocked by PreRequest, the same shape is returned (disguised as a standard LLM error), together with the `X-AI-Gateway-Trace-ID` / `X-AI-Gateway-Executed-Plugins` headers.

---

## 4. Streaming Response (SSE) Extensions

### 4.1 Standard Stream

Upstream native chunks are passed through line by line (`data: {...}`); clients can parse them according to the OpenAI SSE protocol.

### 4.2 Gateway Audit Chunk (gateway_meta)

A custom chunk is injected **before** the terminator `data: [DONE]`:

```
data: {"gateway_meta":{"trace_id":"<32hex>","executed_plugins":["auth_vkey",...],"total_tokens":123,"prompt_injected":true}}
```

| Field | Description |
|---|---|
| `trace_id` | TraceID of this request |
| `executed_plugins` | List of executed plugin names |
| `total_tokens` | Total tokens (official billing takes precedence, otherwise a local estimate) |
| `prompt_injected` | Whether department template injection occurred |

### 4.3 Abnormal Truncation Repair

When an upstream stream is abnormally interrupted before `[DONE]` (network EOF / idle timeout), the gateway forcibly repairs it:

```
data: {"error":{"message":"upstream stream truncated before [DONE]","type":"stream_truncated","code":"gateway_stream_eof"}}
data: [DONE]
```

This ensures downstream devices never fall into a receive deadlock. For the idle-timeout case the message is `upstream stream idle timeout`.

---

## 5. Non-streaming Response

OpenAI-compatible JSON, with the `usage` field returned natively by the upstream; local estimates are used for audit records only when the official usage cannot be captured, and do not affect the response body itself.

---

## 6. Built-in Paths

| Path | Description |
|---|---|
| `/v1/chat/completions` | Example route: OpenAI-like chat (policy in the example configuration is `latency`) |
| `/v1/iot/analyze` | Example route: IoT domain tag routing (policy in the example configuration is `domain_tag`, bound only to ernie) |

> Routing paths are declared entirely by `routers[]` in `config.yaml`; the above are example configuration paths only.
