# Plugin Guide (plugins/)

**English** | [中文](plugins.md)

> This document explains how to understand, configure, and extend the plugin system of AI-Gateway-Core.
> The plugin code lives in `plugins/`, and the interface definitions live in `kernel/plugin.go`.

---

## 1. Plugin Mechanism Overview

### 1.1 Standard Interface

```go
// kernel/plugin.go
type Plugin interface {
    Name() string
    ApiVersion() string
    Init(configJSON string) error
    PreRequest(ctx *PluginContext, req *http.Request) error
    ModifyResponse(ctx *PluginContext, res *http.Response) error
}
```

- `Init`: called once when the gateway is constructed; it receives the JSON serialization of `plugins[].config` from `config.yaml`.
- `PreRequest`: executed **before** the request is forwarded upstream; it can authenticate, rewrite the Body, inject data, or call `ctx.Abort(...)` to intercept directly.
- `ModifyResponse`: executed after the upstream response returns; it can wrap `res.Body` to implement streaming interception.
- Inheriting from `kernel.BasePlugin` (all methods are empty implementations) saves you from writing irrelevant methods, and future interface upgrades will not break existing plugins.

### 1.2 Optional Extension Interfaces

```go
// DirectorHook: once implemented, it is called during the reverse proxy Director phase and carries route-level traffic splitting.
type DirectorHook interface {
    Director(ctx *PluginContext, req *http.Request) error
}
```

### 1.3 Three-Phase Execution

```
Gateway.ServeHTTP
  ├─ Pipeline.ExecutePreRequest    (before forwarding: auth_vkey → prompt_injector → security_guard)
  ├─ ReverseProxy.Director
  │    └─ Pipeline.ExecuteDirector (route level: dynamic_router → Router.Route)
  └─ ReverseProxy.ModifyResponse
       └─ Pipeline.ExecuteModifyResponse (response level: auditor)
```

### 1.4 Execution Order and Fault Tolerance

- Order: ascending by `plugins[].order` in `config.yaml` (`sort.SliceStable` inside `BuildPipeline`).
- Fault tolerance: `fail_strategy: abort` (a plugin error → the request is circuit-broken, PreRequest returns 502) / `bypass` (skip the failing plugin and continue forwarding).
- Interception: once any PreRequest plugin calls `ctx.Abort(status, body)`, the pipeline is interrupted and the gateway returns directly (the upstream is no longer proxied).

### 1.5 Plugin Registration

Plugin packages self-register in `init()`, triggered by a blank import in `main.go`:

```go
// plugins/xxx.go
func init() {
    kernel.RegisterPlugin("plugin_name", func() kernel.Plugin { return &XxxPlugin{} })
}
```

Afterwards, simply mount it under `plugins[]` in `config.yaml` (including order / fail_strategy / config).

### 1.6 Dynamic Mounting (Reserved Wasm Slot)

```go
// Future Wasm plugin marketplace: dynamically inject into the interception chain without modifying the kernel or restarting the gateway
pipeline.RegisterDynamicPlugin(plg) // appended to the tail of the chain as FailBypass
```

---

## 2. Built-in Plugins Quick Reference

| Plugin | Registration Name | Phase | Core Config | Context Keys Written | Can Intercept |
|---|---|---|---|---|---|
| vKey authentication | `auth_vkey` | PreRequest | `auth.keys[]`, `auth.hal_enabled` | `gw_auth_key` | 401 |
| Prompt injection | `prompt_injector` | PreRequest | `injector.templates` | `gw_model`, `gw_original_prompt`, `gw_final_prompt`, `gw_prompt_injected`, `gw_prompt_tokens_est` | 403 (model whitelist) |
| Security guard | `security_guard` | PreRequest | `security.sensitive_words`, `security.injection_patterns`, `security.block_response` | `gw_security_check` | 403 (disguised as a standard error) |
| Dynamic routing | `dynamic_router` | Director | `routers[]`, `upstreams[]` | `gw_upstream_name` (the selected node is mounted into req.Context) | 400/404/503 |
| Streaming audit | `auditor` | ModifyResponse | `transport.idle_read_timeout_sec`, `edge_storage` | read-only for the most part, writes nothing | — |

> Context keys all belong to the kernel-reserved namespace (`gw_` prefix, see `kernel/context.go`); for private keys, plugins should use their own prefix to avoid conflicts.

---

## 3. auth_vkey — vKey Authentication

**Responsibility**: extract `Authorization: Bearer <vKey>`, authenticate by equality comparison in an in-memory O(1) Map, and on success inject the identity, department, model whitelist, and multi-dimensional profile tags into the Context for downstream plugins to use.

**Registration name**: `auth_vkey` **Phase**: PreRequest **Default fail_strategy**: `abort`

**Config items**:

| Config | Type | Default | Description |
|---|---|---|---|
| `auth.hal_enabled` | bool | false | Whether to enable hardware HAL fingerprint verification (TPM2.0/TEE) |
| `auth.keys[]` | list | required | vKey table (see below) |

`auth.keys[]` fields: `vkey` (plaintext key), `username`, `department` (templates are matched against this), `model_whitelist` (hard whitelist constraint), `tags` (used for domain_tag policy matching), `hardware_fingerprint` (HAL fingerprint).

**Behavior and side effects**:

- No Bearer header → Abort 401 `missing_api_key`.
- vKey not found → Abort 401 `invalid_api_key`.
- `hal_enabled` with a mismatched fingerprint → Abort 401 `self_lockdown` (self circuit break); on a Mac without a TPM device it is skipped gracefully and a warning is logged.
- On success, writes `gw_auth_key` (`*config.AuthKey`).

**Security note**: vKeys are compared as plaintext for equality and have no lifecycle management (generation/expiry/rotation/revocation); at scale they should be replaced with HMAC stateless issuance or the `keygen` subcommand.

---

## 4. prompt_injector — Prompt Injection

**Responsibility**: parse the OpenAI JSON Body, read `model`, fetch the enterprise guideline template from `injector.templates` according to the user's `department`, seamlessly inject it at the head/tail of the first user message, and rewrite the Body with `io.NopCloser`.

**Registration name**: `prompt_injector` **Phase**: PreRequest **Default fail_strategy**: `bypass`

**Config items**: `injector.templates.<department>` = `{head, tail}`.

**Behavior and side effects**:

- Only handles `Content-Type: application/json`; non-JSON (small-model/industrial-algorithm paths) is skipped automatically and marked `gw_prompt_injected=false`.
- Writes `gw_model`, `gw_original_prompt`, `gw_final_prompt`, `gw_prompt_injected`, `gw_prompt_tokens_est`.
- **Hard model whitelist constraint**: if `auth.keys[].model_whitelist` is non-empty and the requested model is not in it → Abort 403 `model_not_allowed`.
- After injection, `req.Body` and `req.ContentLength` are rewritten.

**Example config**:

```yaml
injector:
  templates:
    R&D Department:
      head: "You are an internal enterprise R&D assistant. Please comply with the company's code security guidelines and produce rigorous technical answers."
      tail: ""
```

---

## 5. security_guard — Security Guard

**Responsibility**: deep-scan the final Prompt (preferring `gw_final_prompt`, falling back to `gw_original_prompt`); a hit on the sensitive word dictionary (DLP) or on an anti-injection regex triggers a circuit break and returns a **403 error JSON disguised as one from a standard large model**.

**Registration name**: `security_guard` **Phase**: PreRequest **Default fail_strategy**: `abort`

**Config items**:

| Config | Description |
|---|---|
| `security.sensitive_words[]` | sensitive word dictionary (case-insensitive substring matching) |
| `security.injection_patterns[]` | anti-prompt-injection regexes (`(?i)` is added automatically, case-insensitive) |
| `security.block_response` | error JSON body returned on a hit (`error.message/type/code`) |

**Behavior and side effects**:

- Hit on a sensitive word → `gw_security_check=BLOCKED_SENSITIVE`, Abort 403.
- Hit on an injection regex → `gw_security_check=BLOCKED_INJECTION`, Abort 403.
- Passed → `gw_security_check=PASSED`.
- Empty Prompt (non-conversational requests) → PASSED directly, no interception.

---

## 6. dynamic_router — Dynamic Routing (Director Phase)

**Responsibility**: during the reverse proxy Director phase, call `kernel.Router.Route` to pick the best upstream node following "path → circuit breaker filtering → hard model constraint → strategy", rewrite `req.URL` / `req.Host`, and safely replace the vKey with the real upstream credential (direct Bearer or Baidu key exchange `access_token`).

**Registration name**: `dynamic_router` **Phase**: Director (implements `DirectorHook`) **Default fail_strategy**: `abort`

**Config items**: `routers[]` (path/strategy/upstreams/default_model/domain_tags) + `upstreams[]` (base_url/api_key/protocol/auth_mode/models/cost_per_1k/tags/circuit).

**Error code semantics** (mapped by `defaultErrorHandler`):

| Error | HTTP Status |
|---|---|
| `ErrNoRoute` (path not matched) | 404 |
| `NoModelSupportError` (no upstream supports the model) | 400 |
| `ErrCircuitOpen` (circuit breaker open) | 503 |
| Other proxy errors | 502 |

**Credential replacement rules**:

- `auth_mode: direct` → set `Authorization: Bearer <ResolveAPIKey()>` (supports `api_key_env` environment variable injection).
- `auth_mode: token_exchange` (Baidu Qianfan) → exchange a temporary `access_token` from `aip.baidubce.com/oauth/2.0/token`, cache it in memory until 1 minute before expiry, append the URL parameters, and delete the Authorization header.

---

## 7. auditor — Streaming Audit (ModifyResponse Phase)

**Responsibility**: wrap the upstream `res.Body`, non-blockingly intercept streaming (SSE)/non-streaming (JSON) responses, incrementally count tokens, capture official billing and override local estimates, and asynchronously emit audit logs after the stream ends.

**Registration name**: `auditor` **Phase**: ModifyResponse **Default fail_strategy**: `bypass`

**Config items**: `transport.idle_read_timeout_sec` (SSE idle read timeout; on timeout the upstream stream is forcibly closed), `edge_storage` (persistence/reporting).

**Key behaviors**:

1. **Never read the whole body into memory**: parse SSE line by line, counting while forwarding.
2. **Official billing override**: capture `usage.prompt_tokens_details.cached_tokens` (DeepSeek/Qwen and other domestic models), override local estimates with official data, and mark `precision: official_cached`.
3. **Abnormal stream truncation handling**: EOF without `[DONE]` → forcibly append the missing error chunk + `[DONE]` to prevent downstream deadlock; idle timeouts are handled the same way.
4. **gateway_meta injection**: before `[DONE]`, inject `data: {"gateway_meta":{trace_id, executed_plugins, total_tokens, prompt_injected}}`.
5. **Asynchronous persistence**: an independent Goroutine emits the audit JSON to stdout + `EdgeStorage` (uba.json append + asynchronous reporting).

**Audit record fields**: see [audit-log.md](./audit-log.md).

---

## 8. How to Write a New Plugin (Three Steps)

**Step 1**: Create a new file under `plugins/` and implement the `Plugin` interface (inheriting `BasePlugin`):

```go
package plugins

import (
    "net/http"
    "github.com/dezd-ai/ai-gateway-core/kernel"
)

type MyPlugin struct{ kernel.BasePlugin }

func init() {
    kernel.RegisterPlugin("my_plugin", func() kernel.Plugin { return &MyPlugin{} })
}

func (p *MyPlugin) Name() string { return "my_plugin" }

func (p *MyPlugin) PreRequest(ctx *kernel.PluginContext, req *http.Request) error {
    // Read config: cfg, _ := ctx.GetValue(kernel.CtxConfig).(*config.Config)
    // Write data:  ctx.Set("my_plugin_xxx", value)
    // Intercept:   ctx.Abort(http.StatusForbidden, body)
    return nil
}
```

**Step 2**: Mount it under `plugins[]` in `config.yaml` (and `config.example.yaml`):

```yaml
plugins:
  - name: my_plugin
    enabled: true
    order: 6
    fail_strategy: bypass
    config:
      my_option: value
```

**Step 3**: Verify with `go build ./... && go vet ./... && go test ./...`.

> Tip: `Init(configJSON string)` receives the JSON of `plugins[].config`; use `json.Unmarshal` to parse it into the plugin's private struct.

---

## 9. Plugin Development Conventions

- Keep zero external dependencies: prefer the Go standard library.
- Concurrency safety: a plugin's reads and writes of `PluginContext` are thread-safe (internal RWMutex), but the plugin's own shared state must be locked by the plugin itself.
- Use a plugin prefix (e.g. `my_plugin_`) for private Context keys; do not occupy the `gw_` namespace.
- For large bodies, use pooled reads via `kernel.ReadBody` / `kernel.GetBuffer`; `io.ReadAll` with a one-shot large allocation is forbidden.
