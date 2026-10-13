# Configuration Reference (config.yaml)

**English** | [中文](config-reference.md)

> Every behavior of the gateway is declared in `config.yaml` (the GitOps single source of truth). This document is the complete field reference for each configuration block.
> The configuration file is ignored by `.gitignore`; what ships with the repository is `config.example.yaml` (secrets always go through environment variables).

---

## 1. Top-level structure

```yaml
server:        # listen address
log:           # logging
auth:          # vKey authentication table
plugins:       # plugin mounting
upstreams:     # upstream nodes
routers:       # route rules
security:      # sensitive words + injection prevention
injector:      # department prompt templates
transport:     # timeouts and connection pool
edge_storage:  # edge disk persistence
watchdog:      # watchdog
```

---

## 2. server — listen address

| Field | Type | Default | Description |
|---|---|---|---|
| `host` | string | `0.0.0.0` | Listen IP |
| `port` | int | `8080` | Listen port (1-65535, Validate hard validation) |

---

## 3. log — logging

| Field | Type | Default | Description |
|---|---|---|---|
| `level` | string | `info` | Log level |
| `path` | string | `""` | Audit log path; when left empty it degrades automatically by environment: Linux → `/var/log/ai-gateway/uba.json`, Mac → `./logs/uba.json` |

---

## 4. auth — authentication

| Field | Type | Default | Description |
|---|---|---|---|
| `hal_enabled` | bool | `false` | Enable hardware HAL fingerprint validation (TPM2.0/TEE); on Mac with no device it is skipped automatically with a warning |
| `keys[]` | list | required (at least one) | vKey authentication table |

`keys[]` fields:

| Field | Description |
|---|---|
| `vkey` | Plaintext key, O(1) Map equality comparison |
| `username` | Username (for audit records) |
| `department` | Department (matches `injector.templates` injection template) |
| `model_whitelist` | **Hard constraint** model whitelist: when non-empty, a request model not on the list is rejected directly with 403 `model_not_allowed` |
| `tags` | Multi-dimensional profile tags (the matching basis for the `domain_tag` strategy) |
| `hardware_fingerprint` | Hardware fingerprint (compared when `hal_enabled`) |
| `rate_limit` | QPS rate limit (requests per second, token bucket), `0` = unlimited; over the limit returns 429 `rate_limited` |
| `quota_daily` | Daily token quota, `0` = unlimited; when exhausted returns 429 `quota_exceeded` (used quota is persisted across restarts in `quota.json`) |
| `disabled` | Revocation switch: `true` = disable that key (403 `key_disabled`, takes effect on hot reload) |
| `agent_mode` | **Agent permissive mode**: `true` = governance retained (audit/quota/security), interference reduced to zero (no model swap / no injection / no timeout cutoff). See [agent-mode.md](./agent-mode.md) |

```yaml
auth:
  hal_enabled: false
  keys:
    - vkey: "sk-zhangsan-dev-0001"
      username: "张三"
      department: "研发部"
      model_whitelist: ["deepseek-v4-flash", "qwen-turbo"]
      tags: ["llm", "coding", "rd"]
      hardware_fingerprint: "tpm2-zhangsan-device-fp-0001"
```

---

## 5. plugins — plugin mounting

| Field | Type | Default | Description |
|---|---|---|---|
| `name` | string | required | Plugin registration name (see `docs/plugins.md`) |
| `enabled` | bool | `true` | Whether enabled |
| `order` | int | `0` | Execution order (ascending; **must not repeat**, Validate hard validation) |
| `fail_strategy` | string | `abort` | `abort` (circuit-break on error) \| `bypass` (skip and continue) |
| `config` | map | `{}` | Plugin-private configuration (passed to `Plugin.Init` after JSON serialization) |

```yaml
plugins:
  - name: auth_vkey
    enabled: true
    order: 1
    fail_strategy: abort
```

---

## 6. upstreams — upstream nodes

| Field | Type | Default | Description |
|---|---|---|---|
| `name` | string | required | Node name (**globally unique**, Validate hard validation) |
| `endpoint_path` | string | `""` | Path override for forwarding to the upstream (e.g. `/v1/chat/completions`) |
| `base_url` | string | required | Upstream base URL |
| `api_key` | string | `""` | Write the key directly (not recommended) |
| `api_key_env` | string | `""` | Read the key from an environment variable (recommended, takes precedence over `api_key`) |
| `secret_key` | string | `""` | Baidu Qianfan client_secret |
| `secret_key_env` | string | `""` | Read the secret from an environment variable |
| `protocol` | string | `openai` | `openai` \| `baidu_wenxin` |
| `auth_mode` | string | `direct` | `direct` (Bearer direct) \| `token_exchange` (Baidu key exchange, only for the `baidu_wenxin` protocol) |
| `models` | string[] | `[]` | List of supported models; **hard constraint**: a request model supported by no upstream → 400 `no upstream supports model`; an empty list is treated as unrestricted |
| `weight` | int | `1` | Weight (currently not used in strategy computation, metadata only) |
| `cost_per_1k` | float | `0` | Cost per 1k tokens (used by the `cost` strategy and obs cost statistics) |
| `tags` | string[] | `[]` | Domain tags (matching basis for the `domain_tag` strategy) |
| `circuit` | map | see below | Circuit breaker parameters |

`circuit` fields:

| Field | Default | Description |
|---|---|---|
| `failure_threshold` | `5` | Consecutive failure threshold; reaching it opens the circuit |
| `open_timeout_sec` | `30` | Duration the circuit stays open; on expiry it enters half_open |
| `half_open_max` | `3` | Maximum number of probe requests admitted during half_open |

```yaml
upstreams:
  - name: deepseek
    endpoint_path: /v1/chat/completions
    base_url: https://api.deepseek.com
    api_key_env: DEEPSEEK_API_KEY
    protocol: openai
    auth_mode: direct
    models: ["deepseek-v4-flash"]
    cost_per_1k: 0.14
    tags: ["llm", "coding"]
    circuit:
      failure_threshold: 5
      open_timeout_sec: 30
      half_open_max: 3
```

---

## 7. routers — route rules

| Field | Type | Default | Description |
|---|---|---|---|
| `path` | string | required | Matching path (must start with `/`, **must not repeat**, Validate hard validation) |
| `strategy` | string | `round_robin` | `round_robin` \| `least_connection` \| `latency` \| `cost` \| `domain_tag` |
| `upstreams` | string[] | required | List of candidate node names (must reference declared upstreams) |
| `default_model` | string | `""` | Default model (currently metadata only, not part of route decisions) |
| `domain_tags` | string[] | `[]` | Domain tags (**currently parsed only, not used in matching**, see known limitations) |

```yaml
routers:
  - path: /v1/chat/completions
    strategy: latency
    upstreams: ["deepseek", "qwen"]
    default_model: deepseek-v4-flash
  - path: /v1/iot/analyze
    strategy: domain_tag
    upstreams: ["ernie"]
    domain_tags: ["iot"]
```

**Three-level route filtering**: exact path match → circuit breaker filtering → model hard-constraint filtering → strategy picks the node.

---

## 8. security — security protection

| Field | Description |
|---|---|
| `sensitive_words[]` | Sensitive word dictionary (case-insensitive substring match; a hit → 403) |
| `injection_patterns[]` | Prompt injection prevention regexes (case is ignored automatically) |
| `block_response` | Disguised error JSON body returned on a hit |

```yaml
security:
  sensitive_words: ["机密", "公司未公开财报", "internal-secret"]
  injection_patterns:
    - "ignore previous instructions"
    - "忽略之前的指令"
    - "reveal your system prompt"
  block_response:
    error:
      message: "请求被安全策略拦截"
      type: "security_blocked"
      code: "content_filter"
```

---

## 9. injector — department prompt templates

| Field | Description |
|---|---|
| `templates.<department>` | Injection template matched by `auth.keys[].department` |

Each template contains `head` (injected at the beginning of the first user message) and `tail` (at the end):

```yaml
injector:
  templates:
    研发部:
      head: "你是企业内部研发助手，请遵守公司代码安全规范，输出严谨技术答案。"
      tail: ""
```

---

## 10. transport — timeouts and connection pool

| Field | Type | Default | Description |
|---|---|---|---|
| `connect_timeout_sec` | int | `5` | Connection timeout (Dial + TLS) |
| `ttft_timeout_sec` | int | `10` | Time to first token (TTFT) timeout |
| `idle_read_timeout_sec` | int | `30` | Streaming idle read timeout (forced disconnect on timeout, 504 semantics) |
| `max_idle_conns` | int | `100` | Global maximum idle connections of the connection pool per node |
| `max_idle_conns_per_host` | int | `10` | Maximum idle connections per Host per node |
| `max_request_body_bytes` | int64 | `0` | Request body limit (bytes), `0` = unlimited; over the limit returns 413 (memory guard layer 1) |
| `max_audit_body_bytes` | int64 | `0` | Audit buffer limit for non-streaming responses, `0` = unlimited; over the limit parsing of official usage is abandoned and audit degrades to estimated (memory guard layer 2) |
| `agent_ttft_timeout_sec` | int | `0` | First-token timeout amplification for agent keys (seconds), `0` = follow `ttft_timeout_sec` (long reasoning by reasoning models) |
| `agent_max_request_body_bytes` | int64 | `0` | Request body limit for agent keys, `0` = follow `max_request_body_bytes` (**not "unlimited"**) |
| `agent_gateway_meta` | bool | `false` | Whether agent mode injects the `gateway_meta` chunk (not injected by default, for compatibility with strict SSE parsers) |

> Validate hard validation: all three timeouts must be positive numbers.

---

## 11. edge_storage — edge disk persistence

| Field | Type | Default | Description |
|---|---|---|---|
| `enabled` | bool | `false` | Enable disk persistence + asynchronous reporting |
| `max_disk_mb` | int64 | `512` | Ring file limit; on reaching the threshold FIFO rotation occurs (`.1` overwrite) |
| `strategy` | string | `rotate` | Rotation strategy (currently only `rotate`) |
| `report_url` | string | `""` | Remote reporting address (when empty, persist only without reporting) |

```yaml
edge_storage:
  enabled: true
  max_disk_mb: 512
  strategy: rotate
  report_url: "https://uba.example.com/ingest"
```

---

## 12. watchdog — watchdog

| Field | Type | Default | Description |
|---|---|---|---|
| `enabled` | bool | `true` | Enable feeding the watchdog |
| `device` | string | `/dev/watchdog` | Watchdog device path (on Mac with no device it is skipped automatically with a warning) |
| `interval_sec` | int | `30` | Watchdog feed interval |

## 12.5 capacity — capacity and memory guard (layers 3/4)

| Field | Type | Default | Description |
|---|---|---|---|
| `max_inflight` | int | `0` (automatic) | Concurrency admission limit; when full returns 429 `busy`; automatic = 1 concurrency per 5MB of memory, clamped to 16~4096 |
| `mem_soft_percent` | int | `0` (automatic 80) | Memory soft threshold (cgroup `memory.current/max` or /proc approximation); triggers **concurrency reduction** (limit halved) rather than rejection |
| `mem_hard_percent` | int | `0` (automatic 90) | Memory hard threshold; triggers rejection of new requests with 429 `memory_hard` |
| `hysteresis_samples` | int | `0` (automatic 3) | N consecutive samples above the threshold are required to trigger/recover (anti-flapping) |

> Platform notes: the watermark circuit breaker takes effect when cgroup (containers) or /proc (bare-metal Linux) is readable; darwin/windows cannot read memory → watermarks are disabled and only concurrency admission takes effect. The metrics endpoint `GET /metrics` outputs in-flight / limit / soft and hard modes.

---

## 13. Secret management conventions

- The repository publishes only `config.example.yaml`; the real `config.yaml` is gitignored and must never be committed.
- Secrets go through environment variables by preference: `upstreams[].api_key_env` / `secret_key_env`.
- Adding a model requires changing two places in sync: `auth.keys[].model_whitelist` + `upstreams[].models`.
- Adding an upstream requires changing three places: add an entry to `upstreams` → add it to some `routers[].upstreams` → add the model to the corresponding vKey's `model_whitelist`.

---

## 14. Static audit (Validate) checks

Validated at startup; failure rejects startup outright:

- `server.port` is within 1-65535;
- `auth.keys` has at least one vKey;
- upstream names are non-empty and unique; `base_url` is required; `protocol` ∈ {openai, baidu_wenxin}; `token_exchange` is limited to `baidu_wenxin`;
- router paths start with `/` and are unique; the strategy is valid; referenced upstreams exist;
- plugin names are unique, `order` does not conflict, `fail_strategy` is valid;
- transport timeouts are positive numbers.
