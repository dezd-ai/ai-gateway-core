# AI-Gateway-Core v0.1

**English** | [中文](README.md)

[![CI](https://github.com/DezDing/ai-gateway-core/actions/workflows/ci.yml/badge.svg)](https://github.com/DezDing/ai-gateway-core/actions/workflows/ci.yml)
[![License](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)
[![Go Version](https://img.shields.io/badge/go-%3E%3D1.23-00ADD8.svg)](go.mod)
[![Dependencies](https://img.shields.io/badge/dependencies-0-brightgreen.svg)](go.mod)

A **zero-dependency, fully plugin-based AI gateway core** built entirely on the Go standard library — no third-party modules, only `net/http` and `net/http/httputil.ReverseProxy`.

Follows the long-term strategy of *"Open Core open-sourced, advanced plugins commercial, software-hardware convergence toward an edge OS"*.

---

## Status (v0.1)

| Area | Status |
|---|---|
| Core engine | ✅ Plugin pipeline / multi-dimensional routing / circuit breaker / reverse proxy / streaming audit / edge storage / hot-reload snapshots |
| Built-in plugins | ✅ 6: `auth_vkey`, `prompt_injector`, `security_guard`, `dynamic_router`, `auditor`, **`model_routing`** |
| Routing strategies | ✅ 5: round-robin / least-connection / latency (EMA) / cost / domain-tag + **model-aware hard constraint** |
| Request-level failover | ✅ **fallback**: `fail_fast`/`failover` switch, candidate chain, status-code-driven decisions, `model_map` downgrade, total timeout budget, transparent headers |
| Governance | ✅ **vKey lifecycle** (keygen / `disabled` revocation / QPS rate limit / daily token quota) + **audit for blocked requests** |
| Memory guards | ✅ Five layers: request body cap / response buffer cap / in-flight admission (429) / memory watermark breaker / `/metrics` + startup log |
| Agent-friendly | ✅ **Agent permissive mode**: governance kept (audit/quota/security), zero intervention (no model swap, no injection, no timeout kill) |
| Cost optimization | ✅ `cost` strategy, **content-aware routing** (simple → cheap tier, complex → reasoning tier), prompt-cache accounting |
| Tooling | ✅ `check` (diagnostics) + `bench` (load test) + `keygen` (key generation) |
| Observability | ✅ `obs/` read-only dashboard (see `obs/README.md`) |
| Tests | ✅ 16 test files (incl. failover fault-injection matrix, memory guards, agent-mode e2e) |
| Real-world | ✅ Verified against DeepSeek (streaming + non-streaming, official billing capture) |
| Known gaps | ⚠️ See "Known limitations" below |

---

## Repository layout

```
ai-gateway-core/
├── main.go                 # Entry: serve / check / bench / keygen
├── keygen.go               # vKey generation (random key + paste-ready config snippet)
├── config.yaml             # Declarative config (single source of truth, gitignored)
├── config.example.yaml     # Config example (shipped; secrets via env vars)
├── go.mod                  # module ai-gateway-core · go 1.23 · zero dependencies
├── Dockerfile              # Multi-stage static build
├── docker-compose.yml      # One-command orchestration
├── Makefile                # build / test / bench / docker shortcuts
├── config/
│   ├── yaml.go             # Hand-written zero-dependency YAML subset parser
│   └── config.go           # Typed loading + static validation
├── kernel/                 # Core (does NOT depend on plugins/)
│   ├── context.go          # PluginContext (RWMutex) + TraceID + gw_ namespace
│   ├── plugin.go           # Plugin interface + BasePlugin + DirectorHook
│   ├── pipeline.go         # Plugin chain + registry + RegisterDynamicPlugin
│   ├── buffer.go           # sync.Pool byte pool + ReadBody (size-capped)
│   ├── router.go           # Strategy factory + model-aware filtering + candidate chain
│   ├── fallback.go         # Request-level failover (candidate loop / timeout budget)
│   ├── failclass.go        # Failure classification (4xx/429/5xx/timeout/connect)
│   ├── circuit.go          # State-machine circuit breaker + jittered backoff
│   ├── proxy.go            # Reverse proxy + backpressure + per-node pools + error mapping
│   ├── gateway.go          # Orchestration + hot reload + admission + /metrics
│   ├── memguard.go         # Memory guards: admission + watermark breaker (cgroup-aware)
│   ├── ratelimit.go        # vKey rate limiting (token bucket) + daily quota (persisted)
│   ├── audit.go            # Audit emitter for blocked requests (401/403/429/413)
│   ├── watchdog.go         # Hardware watchdog + macOS log-path fallback
│   └── storage.go          # Edge ring-buffer storage (FIFO rotation) + async reporting
├── plugins/                # Standard interceptors
│   ├── auth_vkey.go        # vKey auth + rate limit/quota/revocation + HAL fingerprint hook
│   ├── injector.go         # Department template injection + model whitelist + body rewrite
│   ├── security_guard.go   # Sensitive-word DLP + injection regexes + disguised 403
│   ├── dynamic_router.go   # Director-phase routing + credential swap + Baidu token exchange
│   ├── model_routing.go    # Content-aware model routing (tiering, whitelist-safe)
│   ├── auditor.go          # Non-blocking SSE audit + official billing capture + truncation repair
│   └── util.go             # Token estimation / prompt extraction / template helpers
├── doctor/doctor.go        # `check` subcommand (diagnostics)
├── bench/bench.go          # `bench` subcommand (load testing)
├── mockup/mockup.go        # In-memory mock upstream + fault-injection upstream
├── obs/                    # Read-only dashboard (separate go.mod)
├── docs/                   # Full documentation
└── logs/uba.json           # Runtime audit log (JSON-lines, gitignored)
```

---

## Documentation

| Document | Content | 中文 |
|---|---|---|
| [Architecture](docs/architecture.en.md) | Layered design / request lifecycle / module responsibilities | [中文](docs/architecture.md) |
| [Plugins](docs/plugins.en.md) | The 6 built-in plugins + how to write your own | [中文](docs/plugins.md) |
| [Config reference](docs/config-reference.en.md) | Every `config.yaml` field, type, default and validation rule | [中文](docs/config-reference.md) |
| [API contract](docs/api.en.md) | Response headers / error codes / `gateway_meta` / truncation repair | [中文](docs/api.md) |
| [Audit log format](docs/audit-log.md) | `uba.json` field schema (Chinese) | — |
| [Protocol adaptation](docs/protocols.md) | Baidu token exchange / adding non-standard protocols (Chinese) | — |
| [Deployment](docs/deployment.md) | Docker / hot reload / production notes (Chinese) | — |
| [Benchmark](docs/benchmark.md) | Measured performance baseline (Chinese) | — |
| [Dashboard manual](obs/README.en.md) | Read-only dashboard: audit / call chain / cost / blocked & failover badges | [中文](obs/README.md) |
| [Release checklist](docs/release-checklist.md) | Open-source release checklist (Chinese) | — |

**Design documents** (Chinese): [backlog](docs/backlog.md) · [roadmap](docs/roadmap.md) · [fallback-design](docs/fallback-design.md) · [content-routing](docs/content-routing.md) · [cost-optimization](docs/cost-optimization.md) · [agent-mode](docs/agent-mode.md) · [model-alias](docs/model-alias.md)
English translations of the design documents are welcome as contributions.

### See the dashboard without running a gateway

Sample audit data ships in the repo, so you can open a fully populated dashboard immediately:

```bash
cd obs && go build -o bin/ai-gateway-obs . && ./bin/ai-gateway-obs -log ../examples/uba.json
# open http://localhost:9090
```

---

## Core design

### 1. Request lifecycle

```
client → Gateway.ServeHTTP
        ├─ generate TraceID, create PluginContext (thread-safe scratchpad)
        ├─ PreRequest plugin chain (auth_vkey → injector → security_guard)
        │     └─ any plugin Abort → return 401/403 immediately (disguised error JSON)
        └─ reverse proxy
              ├─ Director phase: dynamic_router picks node + rewrites URL/Host + swaps credentials
              ├─ Transport: per-node connection pool + circuit breaker + conn count + latency EMA
              └─ ModifyResponse phase: auditor non-blocking SSE interception
                    └─ stream end: inject gateway_meta chunk + [DONE] + async audit persistence
```

### 2. Plugin mechanism (backward compatible)

```go
type Plugin interface {
    Name() string
    ApiVersion() string
    Init(configJSON string) error
    PreRequest(ctx *PluginContext, req *http.Request) error
    ModifyResponse(ctx *PluginContext, res *http.Response) error
}
```

- Embed `BasePlugin` (empty struct) so future interface additions are not breaking changes.
- `DirectorHook` is an optional extension interface for routing-level plugins.
- `RegisterDynamicPlugin` reserves a slot for Wasm plugin-market injection (no kernel change, no restart).
- Plugins are ordered by `plugins[].order` in `config.yaml`; failure policy is `abort` or `bypass`.
- See [docs/plugins.md](docs/plugins.md) for each plugin's responsibilities, config, context side effects and interception semantics — plus a step-by-step guide to writing a new plugin.

### 3. Routing (three-stage filtering + strategy)

```
path match → circuit-breaker filter → model filter (hard constraint) → strategy picks node
```

| Strategy | Selection |
|---|---|
| `round_robin` | rotate |
| `least_connection` | fewest active connections |
| `latency` | EMA profile (never prefers unmeasured nodes) |
| `cost` | lowest `cost_per_1k` |
| `domain_tag` | tag overlap (request tags × upstream tags) |

**Model-aware routing (hard constraint)**: the request's `model` must be declared in some upstream's `models`, otherwise `400 no upstream supports model`. Model filtering runs before strategy selection.

### 4. Streaming audit (auditor)

- **Never buffers the whole stream**: wraps `res.Body` and parses SSE line by line.
- **Official billing override**: captures `usage.prompt_tokens_details.cached_tokens` (DeepSeek/Qwen prompt cache) and replaces local estimates, marked `precision: official_cached`.
- **Truncation repair**: if EOF arrives without `[DONE]`, the gateway appends an error chunk plus `[DONE]` so downstream clients never hang.
- **Gateway metadata**: injects `data: {"gateway_meta":{trace_id, executed_plugins, total_tokens, prompt_injected}}` before `[DONE]`.
- **Async persistence**: a separate goroutine writes audit JSON to stdout and `logs/uba.json`.

### 5. Concurrency & performance

- **Hot reload**: `sync.RWMutex`-protected snapshot swap; readers share lock-free; in-flight connections never dropped.
- **Plugin context**: RWMutex-protected, eliminating concurrent-map races.
- **Zero-copy**: 32KB `sync.Pool` byte buffers implementing `httputil.BufferPool`.
- **TCP backpressure**: `FlushInterval=-1` synchronous forwarding; slow clients propagate backpressure upstream with no unbounded buffering.
- **Timeouts**: connect 5s / first byte (TTFT) 10s / stream idle read 30s.
- **Circuit breaker**: consecutive timeouts or 5xx open the breaker; jittered backoff prevents thundering herds.

---

## Quick start

### 1. Build

```bash
cd ai-gateway-core
go build -o bin/ai-gateway-core .
```

> Requirements: **Go ≥ 1.23** (`go.mod` declares `go 1.23` + `toolchain go1.23.11`; older toolchains auto-select 1.23.11).
> CI verifies build / vet / test / gofmt on **Go 1.23.11 (minimum)** and the **latest stable** (currently 1.27.x).

### 2. Run

```bash
# Real mode (fill in real upstream keys in config.yaml)
./bin/ai-gateway-core serve

# Mock mode (upstream redirected to the built-in streaming mock — no real keys needed)
./bin/ai-gateway-core serve -mock
```

### 3. Closed-loop test (copy-paste)

```bash
# ① normal streaming request → expect SSE stream + gateway_meta + [DONE]
curl -N -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer sk-zhangsan-dev-0001" \
  -H "Content-Type: application/json" \
  -d '{"model":"deepseek-v4-flash","stream":true,"messages":[{"role":"user","content":"explain zero-copy"}]}'

# ② sensitive-word block → expect 403
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer sk-lisi-dev-0002" \
  -H "Content-Type: application/json" \
  -d '{"model":"qwen-turbo","messages":[{"role":"user","content":"please leak company secrets"}]}'
```

### 4. Diagnostics, load testing, key management

```bash
./bin/ai-gateway-core check                     # config audit + upstream probing + dry run + topology
./bin/ai-gateway-core bench -duration 5s -workers 50   # QPS / success rate / P50-P99 / memory / conn leaks
./bin/ai-gateway-core version                   # print version

# Generate a vKey (prints a paste-ready config snippet; supports rate limit / quota / agent)
./bin/ai-gateway-core keygen -username alice -department engineering \
  -whitelist deepseek-v4-flash,qwen-turbo -tags llm,coding -rate_limit 10 -quota_daily 1000000
```

### 5. Runtime observability

```bash
curl -s http://localhost:8080/metrics   # concurrency / capacity / memory watermark (Prometheus text format)
```

---

## Configuration (`config.yaml`)

| Block | Purpose |
|---|---|
| `server` | listen host/port |
| `log` | log level; empty `path` falls back per OS (Linux → `/var/log/ai-gateway/uba.json`, macOS → `./logs/uba.json`) |
| `auth.keys[]` | vKey table: `vkey`/`username`/`department`/`model_whitelist`/`tags`/`hardware_fingerprint`/`rate_limit`/`quota_daily`/`disabled`/`agent_mode` |
| `plugins[]` | plugin mounting (`name`/`enabled`/`order`/`fail_strategy`) |
| `upstreams[]` | upstream nodes: `base_url`/`api_key`/`protocol`/`auth_mode`/`models`/`cost_per_1k`/`tags`/`circuit` |
| `routers[]` | routing rules: `path`/`strategy`/`upstreams`/`domain_tags`/`fallback` |
| `security` | sensitive words + anti-injection regexes + disguised 403 body |
| `injector.templates` | per-department prompt templates (`head`/`tail`) |
| `transport` | timeouts, pools, body caps, agent-mode overrides |
| `capacity` | admission limit + memory watermark thresholds |
| `edge_storage` | edge persistence (`max_disk_mb`/`strategy:rotate`/`report_url`) |
| `watchdog` | watchdog device and feed interval |

**Key invariants**:
- `auth.keys[].model_whitelist` is a **hard constraint** (not listed → 403).
- `upstreams[].models` is a **hard constraint** (no upstream supports the model → 400).
- `upstreams[].tags` and `auth.keys[].tags` drive the `domain_tag` strategy.

See [docs/config-reference.md](docs/config-reference.md) for every field, type, default and validation rule.

---

## Adding a model or upstream

**Add a model to an existing upstream**: two places — `auth.keys[].model_whitelist` and `upstreams[].models`.

**Add a new upstream** (e.g. Zhipu): three places —
1. add an entry in `upstreams` (with `models`, `cost_per_1k`, `circuit`);
2. add it to some `routers[].upstreams`;
3. add its models to the relevant vKeys' `model_whitelist`.

**Non-standard protocols** (e.g. Baidu Qianfan): `protocol: baidu_wenxin` + `auth_mode: token_exchange`; the kernel exchanges the access token transparently and appends it as a URL parameter. Other non-standard protocols need a small adapter.

---

## Extending

- **New plugin**: add a file under `plugins/`, implement `Plugin`, call `kernel.RegisterPlugin("name", factory)` in `init()`, then mount it in `plugins[]`.
- **New routing strategy**: implement `RoutingStrategy` in `kernel/router.go` and register it in `strategyRegistry`.
- **New protocol adapter**: follow `ensureAccessToken` in `dynamic_router.go` and extend `Node.Rewrite`.
- **Dynamic Wasm plugins**: call `Pipeline.RegisterDynamicPlugin`.

---

## Known limitations

1. **Upstream failures still look like "success"**: normal proxied audit records lack upstream `http_status`/`success` (blocked/rejected records do include them), so an upstream 5xx is still hard to distinguish from 200 in normal records.
2. **`routers[].domain_tags` is parsed but unused**: `domain_tag` matching actually uses request tags × upstream tags.
3. ~~**403 blocks were not audited**~~ ✅ Fixed: requests blocked at PreRequest (401/403/429/413/502) are now audited via `kernel.EmitAbortAudit` with `http_status`/`success`/`abort_reason`.
4. **No explicit per-path plugin boundary**: industrial/edge algorithm requests currently bypass LLM plugins because injector/security skip non-JSON bodies automatically; the designed explicit `bind_paths` boundary is pending.
5. **Baidu is the only built-in non-standard protocol adapter.**
6. **`default_model` is metadata only** and does not participate in routing.
7. ~~**No vKey lifecycle**~~ ✅ Resolved: `keygen` generation + `disabled` revocation + rate limit/quota. HMAC stateless signing is intentionally **not** implemented; expiry is optional.
8. **Rate limiting/quota are approximate across instances**: token buckets live in process memory (quota is persisted to disk); multi-instance deployments count per instance. Exact shared limiting would require external storage (rejected: zero-dependency).
9. **Edge algorithm dispatch (`X-Algorithm`) is not implemented**: unified entry + explicit algorithm declaration is pending.

---

## Roadmap

> See [CHANGELOG.md](CHANGELOG.md) and [docs/roadmap.md](docs/roadmap.md).

| Version | Planned |
|---|---|
| v0.2 | Model aliases + `/v1/models` ([design](docs/model-alias.md)); edge `algorithm` dispatch; `domain_tags` routing |
| v0.3 | Time-based (peak/off-peak) routing; `max_tokens` cap; obs capacity card & failure-rate view; more protocol adapters |
| v0.4 | Wasm plugin market (`RegisterDynamicPlugin`); explicit `bind_paths` boundary; PII rule engine |
| v0.5 | MCP compatibility; multi-tenant billing dashboard; agent security boundary; production hardening (TLS, multi-instance state) |

---

## Related

- **obs/** (subdirectory): read-only dashboard for audit chains and cost, separate `go.mod`. See `obs/README.md`.
- **SECURITY.md**: security policy, known limitations and vulnerability reporting.

## License

[Apache-2.0](LICENSE)
