# AI-Gateway-Core v0.1

[English](README.en.md) | **中文**

[![CI](https://github.com/dezd-ai/ai-gateway-core/actions/workflows/ci.yml/badge.svg)](https://github.com/dezd-ai/ai-gateway-core/actions/workflows/ci.yml)
[![License](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)
[![Go Version](https://img.shields.io/badge/go-%3E%3D1.23-00ADD8.svg)](go.mod)
[![Dependencies](https://img.shields.io/badge/dependencies-0-brightgreen.svg)](go.mod)

一个纯 Go 标准库打造、可高度动态扩展、全插件化的 **AI 网关内核原型**。

遵循「Open Core 核心开源、高级插件闭源商业化、软硬融合走向边缘操作系统」的长期战略。**零外部依赖**，仅用 `net/http` 与 `net/http/httputil.ReverseProxy` 纯手工实现。

---

## 当前状态（v0.1）

| 维度 | 状态 |
|---|---|
| 核心引擎 | ✅ 插件管线 / 多维路由 / 断路器 / 反向代理 / 流式审计 / 边缘落盘 / 热加载快照 |
| 内置插件 | ✅ 6 个：auth_vkey、prompt_injector、security_guard、dynamic_router、auditor、**model_routing** |
| 路由策略 | ✅ 5 种：轮询 / 最少连接 / 延迟(EMA) / 成本 / 领域标签 + **模型感知强约束** |
| 请求级容错 | ✅ **失败转移 fallback**：`fail_fast`/`failover` 开关、候选链、状态码分流、model_map 降档、总超时预算、透明头 |
| 治理能力 | ✅ **vKey 生命周期**（keygen 生成 / `disabled` 吊销 / QPS 限流 / 每日 token 配额）+ **拦截请求落审计** |
| 内存防线 | ✅ 五层：请求体上限 / 响应缓冲上限 / 并发准入(429) / 内存水位熔断 / `/metrics` + 启动日志 |
| Agent 友好 | ✅ **Agent 宽松模式**：治理保留（审计/配额/安全），干预为零（不换模型、不注入、不掐超时） |
| 成本优化 | ✅ `cost` 策略、**内容路由**（简单→便宜档，复杂→推理档）、Prompt Cache 命中计量 |
| 诊断与工具 | ✅ `check`（诊断）+ `bench`（压测）+ `keygen`（密钥生成）三个子命令 |
| 观测看板 | ✅ 本仓库 `obs/` 子目录（只读网页，见 `obs/README.md`） |
| 测试 | ✅ 16 个测试文件（含 fallback 故障注入矩阵、内存防线、agent 模式 e2e） |
| 真实联调 | ✅ DeepSeek 已联调通过（流式 + 非流式，官方计费覆盖） |
| 已知待办 | ⚠️ 见文末「已知限制与待办」 |

---

## 目录结构

```
ai-gateway-core/
├── main.go                 # 入口：serve / check / bench / keygen 子命令
├── keygen.go               # vKey 生成（随机 key + 可粘贴 config 片段）
├── config.yaml             # 声明式配置（GitOps 单一事实来源，gitignore）
├── config.example.yaml     # 配置示例（随仓库发布，密钥走环境变量）
├── go.mod                  # module ai-gateway-core · go 1.23 · 零依赖
├── Dockerfile              # 多阶段静态构建
├── docker-compose.yml      # 一键编排（含 logs 卷与密钥环境变量）
├── Makefile                # build / test / bench / docker 快捷命令
├── config/
│   ├── yaml.go             # 手写零依赖 YAML 子集解析器
│   └── config.go           # 类型化配置加载 + Validate 静态审计
├── kernel/                 # 内核（不依赖 plugins）
│   ├── context.go          # PluginContext(RWMutex) + TraceID + gw_ 命名空间
│   ├── plugin.go           # Plugin 接口 + BasePlugin + DirectorHook
│   ├── pipeline.go         # 插件链 + 插件注册表 + RegisterDynamicPlugin
│   ├── buffer.go           # sync.Pool 字节池 + ReadBody(限额读取)
│   ├── router.go           # 路由策略工厂 + 模型感知 + 候选链 + 节点运行时状态
│   ├── fallback.go         # 请求级失败转移（候选链循环尝试/总超时预算/透明头）
│   ├── failclass.go        # 失败分类（4xx/429/5xx/timeout/connect）
│   ├── circuit.go          # 状态机断路器 + JitterBackoff 抖动退避
│   ├── proxy.go            # 反向代理 + 背压中转 + 每节点独立连接池 + 错误映射
│   ├── gateway.go          # Gateway 编排 + RWMutex 热加载 + 容量准入 + /metrics
│   ├── memguard.go         # 内存防线：并发准入 + 水位熔断（cgroup 感知）
│   ├── ratelimit.go        # vKey 限流（令牌桶）+ 每日配额（落盘）
│   ├── audit.go            # 拦截审计发射器（401/403/429/413 落审计）
│   ├── watchdog.go         # 看门狗 + Mac 日志路径自适应
│   └── storage.go          # 边缘环形时序落盘(FIFO 轮转) + 异步上报队列
├── plugins/                # 标准拦截插件
│   ├── auth_vkey.go        # vKey 鉴权 + 限流/配额/吊销 + HAL 硬件指纹预留
│   ├── injector.go         # 部门模板注入 + 模型白名单 + Body 重写
│   ├── security_guard.go   # 敏感词 DLP + 防注入正则 + 伪装 403（正则预编译）
│   ├── dynamic_router.go   # Director 阶段路由 + 凭证替换 + 百度换钥
│   ├── model_routing.go    # 内容感知模型路由（复杂度定档，选择时保证白名单）
│   ├── auditor.go          # SSE 非阻塞流式拦截 + 官方计费覆盖 + 截断补齐
│   └── util.go             # Token 预估 / Prompt 提取 / 模板注入等共享工具
├── doctor/doctor.go        # check 子命令（诊断引擎）
├── bench/bench.go          # bench 子命令（压测引擎）
├── mockup/mockup.go        # 内存 Mock 流式上游 + 故障注入上游（fallback 测试）
├── examples/uba.json       # 示例审计数据（供 obs 直接体验看板）
├── obs/                    # 只读观测台（独立 go.mod，中英双语文档）
├── docs/                   # 完整文档（架构/插件/配置/API/审计/协议/部署/设计稿）
└── logs/uba.json           # 运行时审计日志（JSON-lines，gitignore）
```

---

## 文档

| 文档 | 内容 | English |
|---|---|---|
| [架构说明](docs/architecture.md) | 分层架构 / 请求时序 / 模块职责 / 并发设计 | [EN](docs/architecture.en.md) |
| [插件说明](docs/plugins.md) | 6 个内置插件的职责、配置、行为与编写指南 | [EN](docs/plugins.en.md) |
| [配置参考](docs/config-reference.md) | `config.yaml` 全字段类型/默认值/约束 | [EN](docs/config-reference.en.md) |
| [API 契约](docs/api.md) | 响应头 / 错误码 / `gateway_meta` / 截断补齐 | [EN](docs/api.en.md) |
| [审计日志格式](docs/audit-log.md) | `uba.json` 字段 schema 与计费精度语义 | — |
| [协议适配](docs/protocols.md) | 百度换钥流程 / 如何新增非标协议 | — |
| [部署指南](docs/deployment.md) | Docker / 热加载 / 生产建议 | — |
| [性能基线](docs/benchmark.md) | 实测压测数据与复现方法 | — |
| [观测台说明](obs/README.md) | 只读看板：审计/调用链/成本/拦截与 fallback 标记 | [EN](obs/README.en.md) |
| [发布检查清单](docs/release-checklist.md) | 开源发布逐项核对表（当前状态与阻塞点） | — |

**设计文档**（中文）：[缺陷/安全待办](docs/backlog.md) · [增长路线](docs/roadmap.md) · [Fallback 设计](docs/fallback-design.md) · [内容路由设计](docs/content-routing.md) · [成本优化方案](docs/cost-optimization.md) · [Agent 宽松模式](docs/agent-mode.md) · [模型别名设计](docs/model-alias.md)
（欢迎贡献英文翻译。）

---

## 核心设计

### 1. 请求生命周期

```
客户端 → Gateway.ServeHTTP
        ├─ 生成 TraceID，创建 PluginContext（线程安全 Scratchpad）
        ├─ PreRequest 插件链（auth_vkey → injector → security_guard）
        │     └─ 任一插件 Abort → 直接返回 401/403（伪装标准错误 JSON）
        └─ 反向代理
              ├─ Director 阶段：dynamic_router 选节点 + 重写 URL/Host + 替换凭证
              ├─ Transport：每节点独立连接池 + 断路器 + 连接计数 + 延迟 EMA
              └─ ModifyResponse 阶段：auditor 非阻塞流式拦截
                    └─ 流结束：注入 gateway_meta chunk + [DONE] + 异步审计落盘
```

### 2. 插件机制（向后兼容）

```go
type Plugin interface {
    Name() string
    ApiVersion() string
    Init(configJSON string) error
    PreRequest(ctx *PluginContext, req *http.Request) error
    ModifyResponse(ctx *PluginContext, res *http.Response) error
}
```

- 继承 `BasePlugin` 空结构体防止未来接口升级造成 Breaking Changes。
- `DirectorHook` 可选扩展接口承载路由级插件。
- `RegisterDynamicPlugin` 预留 Wasm 插件市场动态挂载槽位（无需改内核、无需重启）。
- 插件按 `config.yaml` 的 `order` 排序，支持 `fail_strategy: abort | bypass` 容错。
- 内置 5 个插件的职责、配置项、Context 副作用与拦截语义详见 [docs/plugins.md](docs/plugins.md)；如何编写新插件见该文档末尾。

### 3. 路由机制（三级过滤 + 策略）

```
匹配路径 → 断路器过滤 → 模型过滤(强约束) → 策略挑节点
```

| 策略 | 挑法 |
|---|---|
| `round_robin` | 轮询 |
| `least_connection` | 最少连接 |
| `latency` | 延迟画像（EMA 移动平均，未测量节点不当首选） |
| `cost` | 成本最低（`cost_per_1k`） |
| `domain_tag` | 领域标签匹配（用户 tags × 上游 tags 求交集） |

**模型感知路由**（强约束）：请求体里的 `model` 必须先命中某上游的 `models` 列表，否则返回 `400 no upstream supports model`。模型过滤在策略之前执行。

### 4. 流式审计（auditor）

- **绝不一次性读入内存**：包装 `res.Body`，逐行增量解析 SSE。
- **官方计费覆盖**：捕获国产模型（DeepSeek/千问）的 `usage.prompt_tokens_details.cached_tokens`，用官方数据覆盖本地预估，标记 `precision: official_cached`。
- **异常流截断处理**：EOF 未见 `[DONE]` 时，强行补齐错误 chunk + `[DONE]`，杜绝下游死锁。
- **网关审计注入**：在 `[DONE]` 前注入 `data: {"gateway_meta":{trace_id, executed_plugins, total_tokens, prompt_injected}}`。
- **异步落盘**：独立 Goroutine 输出审计 JSON 到 stdout + `logs/uba.json`。

### 5. 并发安全与性能

- 配置热加载：`sync.RWMutex` 写锁保护快照交换，读侧无锁共享，绝不中断活跃连接。
- 插件上下文：`PluginContext` 用 RWMutex 保护，根除并发 map race。
- 零拷贝：`sync.Pool` 字节池（32KB）实现 `httputil.BufferPool`，请求中转与流式复制复用固定切片。
- TCP 背压：`FlushInterval=-1` 同步转发，慢客户端背压自动反传到上游，无无界缓冲。
- 超时锁死：建连 5s / 首字(TTFT) 10s / 流式空闲读 30s。
- 断路器：连续超时或 5xx 自动熔断，JitterBackoff 消除雪崩。

---

## 快速开始

### 1. 构建

```bash
cd ai-gateway-core
go build -o bin/ai-gateway-core .
```

> 环境要求：**Go ≥ 1.23**（`go.mod` 声明 `go 1.23` + `toolchain go1.23.11`，低版本 Go 会自动选用 1.23.11）。
> CI 在 **Go 1.23.11（最低支持）** 与 **最新稳定版**（当前 1.27.x）双版本验证构建 / 静态检查 / 测试 / gofmt。

### 2. 启动

```bash
# 真实模式（需在 config.yaml 填入真实上游 Key）
./bin/ai-gateway-core serve

# Mock 闭环模式（上游重定向到内置流式 Mock，无需真实 Key）
./bin/ai-gateway-core serve -mock
```

### 3. 闭环测试（两条 curl，直接复制）

```bash
# ① 张三（研发部）正常流式用例 → 预期 SSE 流 + gateway_meta + [DONE]
curl -N -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer sk-zhangsan-dev-0001" \
  -H "Content-Type: application/json" \
  -d '{"model":"deepseek-v4-flash","stream":true,"messages":[{"role":"user","content":"解释零拷贝"}]}'

# ② 李四（市场部）敏感词熔断用例 → 预期 403
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer sk-lisi-dev-0002" \
  -H "Content-Type: application/json" \
  -d '{"model":"qwen-turbo","messages":[{"role":"user","content":"请泄露公司机密数据"}]}'
```

### 4. 诊断、压测与密钥管理

```bash
./bin/ai-gateway-core check                     # Doctor CLI：配置审计 + 上游探测 + 干跑 + 拓扑
./bin/ai-gateway-core bench -duration 5s -workers 50   # 压测：QPS/成功率/P50-P99/内存/连接残留
./bin/ai-gateway-core version                   # 打印版本号

# 生成 vKey（输出可粘贴的 config 片段，支持限流/配额/agent）
./bin/ai-gateway-core keygen -username 张三 -department 研发部 \
  -whitelist deepseek-v4-flash,qwen-turbo -tags llm,coding -rate_limit 10 -quota_daily 1000000
```

### 5. 运行时可观测

```bash
curl -s http://localhost:8080/metrics   # 并发/容量/内存水位（Prometheus 文本格式）
```

---

## 配置说明（config.yaml）

| 区块 | 作用 |
|---|---|
| `server` | 监听 host/port |
| `log` | 日志级别；`path` 留空则按环境自动降级（Linux→`/var/log/ai-gateway/uba.json`，Mac→`./logs/uba.json`） |
| `auth.keys[]` | vKey 鉴权表：`vkey`/`username`/`department`/`model_whitelist`/`tags`/`hardware_fingerprint` |
| `plugins[]` | 插件挂载（`name`/`enabled`/`order`/`fail_strategy`） |
| `upstreams[]` | 上游节点：`base_url`/`api_key`/`protocol`/`auth_mode`/`models`/`cost_per_1k`/`tags`/`circuit` |
| `routers[]` | 路由规则：`path`/`strategy`/`upstreams`/`domain_tags` |
| `security` | 敏感词字典 + 防注入正则 + 伪装 403 响应体 |
| `injector.templates` | 按部门注入的企业提示词模板（`head`/`tail`） |
| `transport` | 超时与连接池（建连/首字/空闲读/最大连接数） |
| `edge_storage` | 边缘落盘（`max_disk_mb`/`strategy:rotate`/`report_url`） |
| `watchdog` | 看门狗设备与喂狗周期 |

**关键约定**：
- `auth.keys[].model_whitelist` 是**强约束**（不在白名单直接 403）。
- `upstreams[].models` 是**强约束**（模型无上游支持直接 400）。两者缺一不可。
- `upstreams[].tags` 与 `auth.keys[].tags` 是 `domain_tag` 策略的匹配依据。

---

## 如何新增一个模型 / 上游

**给现有上游加模型**：改两处 —— `auth.keys[].model_whitelist` + `upstreams[].models`。

**新增一个上游（如智谱）**：改三处 ——
1. `upstreams` 新增条目（含 `models`、`cost_per_1k`、`circuit`）；
2. 把它加进某条 `routers[].upstreams`；
3. 把它的模型名加进对应 vKey 的 `model_whitelist`。

**非标协议（如百度千帆）**：`protocol: baidu_wenxin` + `auth_mode: token_exchange`，内核会自动换取 access_token 并拼接 URL 参数。其它非标协议需新增适配代码。

---

## 如何扩展

- **加插件**：在 `plugins/` 新建文件，实现 `Plugin` 接口，`init()` 里 `kernel.RegisterPlugin("名字", 工厂)`，再在 `config.yaml` 的 `plugins[]` 挂载。
- **加路由策略**：在 `kernel/router.go` 实现 `RoutingStrategy` 接口，注册进 `strategyRegistry`。
- **加非标协议适配**：参考 `dynamic_router.go` 的 `ensureAccessToken`，扩展 `Node.Rewrite`。
- **动态挂载 Wasm 插件**：调用 `Pipeline.RegisterDynamicPlugin`。

---

## 已知限制与待办

1. **上游失败仍显示「成功」**：正常审计记录缺 `http_status`/`success`（**拦截/拒绝类记录已带**），上游 5xx 与 200 在正常记录里仍不易区分。完全治本需给 auditor 补上游状态字段。
2. **`routers[].domain_tags` 未接入匹配**：当前 `domain_tag` 实际匹配用的是「用户 tags × 上游 tags」，路由声明的 `domain_tags` 仅解析未使用。
3. ~~**403 拦截不落审计**~~ ✅ 已修复：被 PreRequest 拦截的 401/403/429/413/502 现由 `kernel.EmitAbortAudit` 落审计（含 `http_status`/`success`/`abort_reason`）。
4. **非 OpenAI 路径未做显式「插件路径边界」**：工业算法/端侧请求目前靠 injector/security 对非 JSON body 自动跳过，规格书规划的 `bind_paths` 显式边界待实现（见 [docs/backlog.md](docs/backlog.md) 1.7）。
5. **百度是唯一内置的非标协议适配**。
6. **`default_model` 是纯元数据**，不参与路由决策。
7. ~~**vKey 无生命周期管理**~~ ✅ 已解决：`keygen` 生成 + `disabled` 吊销 + 限流/配额（R1）。**不做 HMAC 无状态签发、过期可选**（[docs/backlog.md](docs/backlog.md) 1.6）。
8. **多实例下限流/配额是近似的**：令牌桶与配额状态为进程内存态（配额落盘），多实例部署时每实例独立计数；精确共享限流需外部存储（违背零依赖，暂不做）。
9. **端侧算法分发（`X-Algorithm`）未实现**：统一入口 + 显式声明分发算法待落地（[docs/backlog.md](docs/backlog.md) 1.7）。

---

## Roadmap

> 公开路线图（社区可参与）。详见 [CHANGELOG.md](CHANGELOG.md) 与 [docs/roadmap.md](docs/roadmap.md)。

| 版本 | 规划内容 |
|---|---|
| v0.2 | 模型别名 + `/v1/models`（[model-alias.md](docs/model-alias.md)）；端侧 `algorithm` 显式分发；`domain_tags` 路由接入匹配 |
| v0.3 | 时间路由（峰谷价）、`max_tokens` 上限、obs 容量卡片与失败率视图；更多非标协议适配 |
| v0.4 | Wasm 插件市场动态挂载（`RegisterDynamicPlugin` 落地）；显式 `bind_paths` 插件边界；PII 规则引擎 |
| v0.5 | MCP 兼容、多租户计费看板、agent 安全边界；生产硬化（TLS、多实例状态共享评估） |

---

## 关联项目

- **obs/**（本仓库子目录）：只读观测台，网页查看审计/调用链/成本，独立 go.mod。详见 `obs/README.md`。
  **无需运行网关即可看效果**——仓库内附示例审计数据：
  ```bash
  cd obs && go build -o bin/ai-gateway-obs . && ./bin/ai-gateway-obs -log ../examples/uba.json
  # 浏览器打开 http://localhost:9090
  ```
- **SECURITY.md**：安全策略、已知安全限制与漏洞报告渠道。
- **CONTRIBUTING.md**：贡献流程与扩展指南。
- **docs/release-checklist.md**：开源发布逐项核对表。

## License

[Apache-2.0](LICENSE)
