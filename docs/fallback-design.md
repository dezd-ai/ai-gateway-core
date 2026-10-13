# 失败转移（Fallback）设计

> **状态**：设计已冻结（决策见第 14 节），待实现（当前优先事项，先于内容路由插件）。
> **关联**：[roadmap.md](./roadmap.md) R2 · [backlog.md](./backlog.md)（429 入熔断、mid-stream 断流入熔断、请求体上限 #7）· [api.md](./api.md)（透明头契约）。
> **核心承诺**：默认 `fail_fast` 保持现有行为**零变化**；`failover` 需显式开启。

---

## 0. 目标与明确不做

**目标**：把现有「断路器级切换」（失败后**下一次**请求避开坏节点）升级为「请求级 fallback」（**本次**请求就切换），默认保守、可显式开启、全程可审计。

**明确不做**：

- 无限重试 / 全链路重放（风暴 + 重复计费）；
- mid-stream（首字节后）切换（会拼坏流，永远不切）；
- 自动降级到"任意更便宜/更贵的模型"（降级仅来自显式 `model_map`，无惊喜）。

---

## 1. 双窗口模型

| 窗口 | 触发 | 现有行为 | fallback 后 | 客户端可见性 |
|---|---|---|---|---|
| **A. 首字节前** | 连接失败 / 5xx / 429 / TTFT 超时 | 直接 502/503 | failover 时按候选链切换 | fail_fast：硬错误；failover：成功响应 + 透明头 |
| **B. 首字节后**（mid-stream） | 流中途断 / 空闲超时 | 补错误 chunk + [DONE] | **不切换**，保持补齐 | 解析流尾错误 chunk 才知道 |

fallback 只作用于**窗口 A**；窗口 B 的处理（截断补齐）与 fallback 互补，不进开关。

---

## 2. 模式开关（单开关双模式）

```yaml
fallback:
  mode: fail_fast          # fail_fast（默认，现状，保守）| failover（需显式开启）
```

| 模式 | 行为 | 适用 |
|---|---|---|
| `fail_fast` | 失败即 502/503，客户端明确知道 | 合规/对账场景、笨客户端自己会重试的场景 |
| `failover` | 窗口 A 透明切换，客户端拿到成功响应 | SLA/体验优先场景、无重试逻辑的客户端 |

**向后兼容承诺**：不写 `fallback` 区块 = 完全现状；`fail_fast` 下其余 fallback 配置忽略（不报错，文档说明）。

---

## 3. 失败分类判定树

```
一次尝试失败
  ├─ 4xx（400/401/403…）─────► 放弃整条链，直接返回      ← 请求本身有问题，重试/切换无用
  ├─ 连接级失败（建连/TLS 失败，
  │   请求未送达）──────────► 同节点重试 connect_retry 次   ← 服务端大概率未收到，无双计费
  │                             再失败 → 切下一个候选
  ├─ 5xx / 429 / TTFT 超时 ──► 不重试同节点，直接切        ← 请求已到服务端，可能已计费，
  │                                                         重试同节点 = 双计费风险
  └─ mid-stream（首字节已发）► 永不切换，走补齐            ← 客户端已有半截内容
```

> 注意：**"先重试当前模型"只适用于连接级失败**。5xx/429/超时直接切，避免把双计费风险带进默认行为。

---

## 4. 候选链 = (上游, 模型) 二元组序列

构建规则（两步，均在路由阶段完成）：

```
请求 model = M，路由 upstreams = [A, B, C]

① 同模型候选（强约束，按策略排序）
   只在声明支持 M 的上游里选 →  [(A, M), (B, M)]（若 B 也支持 M）

② 降级候选（仅开启 model_map 时追加）
   把不支持的 (上游, 映射模型) 追加到链尾 → [(A, M), (B, M), (C, M')]

最终候选链 = ① 同模型（策略排序） + ② 降级（显式声明顺序）
```

### 场景推演（示例配置 deepseek/qwen/ernie）

| 场景 | 请求 | 候选链 | 行为 |
|---|---|---|---|
| 单模型独占上游 | `deepseek-v4-flash`，未开降级 | `[(deepseek, d4f)]` 链长 1 | deepseek 失败 → 无候选可切 → 502（**正确行为，不硬切**） |
| 开启降级 | `deepseek-v4-flash` + model_map | `[(deepseek,d4f), (qwen,qwen-turbo), (ernie,ernie-bot)]` | 逐级尝试，改写 body 的 model + ContentLength |
| 多上游同模型 | `M` 且 qwen 也支持 | `[(deepseek,M), (qwen,M)]`（策略排序） | 失败直接切，模型不变无改写 |
| 无映射 | `qwen-turbo`，deepseek 不支持 | `[(qwen, qwen-turbo)]` | qwen 挂 → 502，不硬切到不支持的模型 |

### 硬约束

1. **降级后的模型必须过 vKey 的 `model_whitelist`**——否则 fallback 成为越权/绕过审计的通道。该校验在配置 `Validate` 期完成，而非运行期。
2. **降级方向仅来自显式 `model_map`**——不允许自动挑便宜/贵的模型（商业契约与无惊喜原则）。

---

## 5. max_attempts 语义（上限，非目标）

设候选链长 N=3 `[A,B,C]`：

| 关系 | 行为 | 结果 |
|---|---|---|
| `max_attempts == N`（默认） | 尝试整条链 | A→B→C |
| `max_attempts < N` | **截断** | A→B 失败即返回，C 永不尝试；审计标 `chain_truncated: true` |
| `max_attempts > N` | **空转** | 链耗尽即停，剩余额度作废（防重试风暴） |

**关键语义**：

- 候选链是**运行时动态**的（模型过滤后链长可变，如场景 1 链长 1），所以 `max_attempts > 链长` 是常态，必须定义为"耗尽即停"；
- **计数口径**：`max_attempts` = 整条链的 `RoundTrip` 总调用次数（含首次、含连接级同节点重试）；
- `max_attempts == 1` = 禁用切换（等价 fail_fast），合法但文档标注；
- 省略 `max_attempts` → 默认 = 候选链长（不写就不会截断，安全默认）；
- 候选链构建期**去重**：model_map 映射回原 (上游,模型) 时剔除或告警。

---

## 6. 总超时预算（双上限，先到先停）

```
max_attempts = 3   （次数上限）
total_timeout_ms = 15000   （时间上限，整条链共享 deadline）
A 超时 10s → B 超时 10s → 已 20s > 15s → 剩余次数作废，直接返回
```

- 每尝试的超时按**剩余预算递减**（首试 TTFT 10s，次试剩余 5s…）；
- 审计记录**停止原因**：`stop_reason: exhausted | timeout`——否则对账时说不清为什么没试完；
- 无 `total_timeout_ms` 时退化为"每尝试各自超时"（现状语义 × N 次）。

---

## 7. 每次尝试必须重新 Rewrite + 请求体重放

1. **每次尝试前重新 `Node.Rewrite`**：凭证（deepseek 的 key 打到 qwen 必 401）、URL/EndpointPath、百度 token 换取——切上游必须换凭证；
2. **请求体重放**：JSON 路径已被 `prompt_injector` 全量读入缓存（重放零成本 ✅）；**非 JSON 路径**（跳过 injector）需先读缓存——与 [backlog.md](./backlog.md) #7（body 上限）耦合，实现时同批做；
3. **model_map 改写**：改 body 的 `model` 字段 + **同步更新 `ContentLength`**（漏了上游直接报错）。

---

## 8. 熔断统计补全（不做则"坏节点切不走"）

| 补全项 | 现状 | 必要性 |
|---|---|---|
| **429 纳入 `RecordFailure`** | 只记 5xx/连接错误 | 限流供应商不会被熔断保护 |
| **mid-stream 断流纳入 `RecordFailure`** | 200 后流断不记失败 | 否则"每次发 200 然后流中途死"的上游永远不被熔断 |

另外：候选链切换仍要过 `circuitAllowed`——**不切到已熔断节点**；并发场景所有请求同时切到候选[1]，由候选[1] 自身的断路器（跨请求共享）自动开闸保护，属设计内行为（写入文档，避免使用者误以为 fallback 放大故障）。

---

## 9. 全失败错误选择 + 透明头 + 审计

**全失败时**：返回候选链中**最有信息量**的错误（优先原始 429/5xx，而非最后一个 503），附尝试明细供审计。

**透明头**（failover 模式下附加，进 [api.md](./api.md) 契约）：

```
X-AI-Gateway-Fallback: deepseek→qwen
X-AI-Gateway-Attempts: 2
```

**客户端契约（已定：默认零强制要求）**：

| 方向 | 头/配置 | 作用 | 强制？ |
|---|---|---|---|
| 读（响应头） | `X-AI-Gateway-Fallback` / `X-AI-Gateway-Attempts` | 感知实际切换链与尝试次数 | 否（可选） |
| 发（请求头） | `X-AI-Gateway-No-Downgrade: true` | 单次调用禁用模型降级（宁 502 不换模型） | 否（可选） |
| 配置（vKey 级） | `auth.keys[].allow_downgrade: false` | 该 key 永久禁用降级 | 否（可选，管理员配置） |
| 配置（路由级） | `mode: fail_fast` | 整条路由禁止一切切换 | 否（可选） |

- `X-AI-Gateway-No-Downgrade` 为网关内部头，**转发上游前必须摘除**；
- ⚠️ **行为提醒**：默认自动降级下，响应体 `model` 字段可能与请求不一致（客户端若校验 `response.model == 请求 model` 会注意到不匹配），文档需提示。

**审计新增字段**：

```
fallback_chain: [deepseek, qwen]    # 实际尝试过的 (上游,模型) 序列
attempts: 2                          # 实际 RoundTrip 次数
chain_truncated: true                # 是否因 max_attempts 截断
stop_reason: exhausted | timeout     # 停止原因
```

**obs 联动**：failover 使失败"更隐蔽"（200 + 截断），obs 需渲染 fallback 标记，否则 200+截断会被误渲染为 PASSED（与已知限制 1 一并治）。

---

## 10. 内核/插件边界与改动面

**判定标准**：fallback 的每个动作都必须摸到内核资源（连接池/断路器/路由表/凭证），故**机制进内核**；策略部分进配置；插件仅跟进调用与审计。

| 文件 | 改动 |
|---|---|
| `kernel/router.go` | `RouteCandidates()` 候选链构建；`classifyError()` 失败分类（新建）；`Node.RoundTrip` 补 429 / mid-stream 入熔断 |
| `kernel/proxy.go` | `routerRoundTripper` 改循环尝试（总超时预算、每尝试 Rewrite、透明头） |
| `kernel/context.go` | 新增 `CtxFallbackChain` / `CtxAttempts` 等 `gw_` 键 |
| `config/config.go` | `fallback` 配置结构 + `Validate`（mode 枚举 / retry_on 合法值 / max_attempts / model_map 引用与白名单校验） |
| `plugins/dynamic_router.go` | 一行：`router.Route` → `router.RouteWithFallback`（内核接口升级，插件跟进） |
| `plugins/auditor.go` | 审计记录加 fallback 字段 |
| `docs/api.md` | 透明头契约 |

**未来插件扩展点**（本轮不做）：可选接口 `CandidateStrategy`，第三方可注册自定义候选顺序/降级规则，符合现有 `strategyRegistry` 模式，内核零改动。

**分层承诺保持**：`kernel` 不依赖 `plugins`；fallback 在内核 Transport 层之下，所有走 `routerRoundTripper` 的请求自动获得能力。

---

## 11. 故障注入测试（不做则盲飞）

现有 mock 上游只会成功。需支持按场景注入失败：

| 注入场景 | 验证点 |
|---|---|
| 返回 429 | 直接切候选[1]；429 计入熔断 |
| 返回 5xx | 直接切；全失败返回原始 5xx |
| 连接重置 / 建连失败 | 同节点重试 connect_retry 次后再切 |
| TTFT 超时 | 直接切；总超时预算递减生效 |
| mid-stream 断流 | 不切换、走补齐；计入熔断 |

---

## 12. 配置草案（完整示例）

```yaml
routers:
  - path: /v1/chat/completions
    strategy: latency
    upstreams: ["deepseek", "qwen", "ernie"]
    fallback:
      mode: fail_fast            # fail_fast（默认）| failover
      max_attempts: 3            # 整条链尝试上限（含首次），默认 = 候选链长
      total_timeout_ms: 15000    # 整条链总预算（可选）
      connect_retry: 0           # 仅连接级失败的同节点重试次数（已定默认 0；
                                 #   网络抖动明显的部署可调 1）
      retry_on: ["429", "5xx", "timeout", "connect"]   # 4xx 永不重试
      transparent_header: true   # failover 下附加 X-AI-Gateway-Fallback
      model_map:                 # 降级映射（显式声明，可空）；
                                 #   默认自动降级，可用请求头
                                 #   X-AI-Gateway-No-Downgrade 或
                                 #   auth.keys[].allow_downgrade=false 禁用
        - from: deepseek-v4-flash
          to:                    # ⚠️ 手写 YAML 解析器不支持行内 flow map，
            upstream: qwen       #    to 必须用块式写法
            model: qwen-turbo
```

---

## 13. 里程碑

| 里程碑 | 内容 | 完成标志 |
|---|---|---|
| M1 | 候选链构建 + `classifyError` + fail_fast 路径 | ✅ 已完成（2025-08）：`RouteCandidates` / `attach` / `RouteWithFallback`（fail_fast）/ `classifyFailure` / fallback 配置解析与 Validate / dynamic_router 一行切换；单测与 bench 冒烟通过，行为零变化 |
| M2 | failover 循环尝试 + 总超时预算 + 429/mid-stream 入熔断 | ✅ 已完成（2025-08）：`fallbackPlan.RoundTrip`（状态码分流 / connect 重试 / retry_on 过滤 / max_attempts / 总超时预算 / 透明头）；`Node.RoundTrip` 429 入熔断；auditor mid-stream 断流入熔断（区分上游断/客户端断）；kernel 集成测试 8 例 + plugins 截断 e2e 通过 |
| M3 | model_map 降级 + 透明头 + 审计字段 + 故障注入测试 | ✅ 已完成（2025-08）：`fallbackCandidate{node,model}` 候选链（model_map 追加降级 + vKey 白名单运行期校验 + 每尝试 body 重放与 model 改写 + ContentLength 同步）；审计记录新增 `fallback_chain` / `attempts` / `chain_truncated` / `stop_reason`；`mockup.NewFaultServer` 故障注入（500/429/连接重置/超时/mid-stream 截断）；故障注入矩阵 e2e（切换/不切换/审计字段）全绿；obs 展示 fallback 标记 |

---

## 14. 决策记录（已定，设计冻结）

| # | 决策 | 结论 | 理由 |
|---|---|---|---|
| 1 | `connect_retry` 默认值 | **0**（保守），可调 1 | 内网抖动少；保持 `max_attempts` 计数口径简单；"为一次抖动切换上游"的代价可控 |
| 2 | `model_map` 降级策略 | **默认自动降级（方案 A）+ opt-out 逃生口**（vKey `allow_downgrade: false` / 请求头 `X-AI-Gateway-No-Downgrade`） | 内网信任模型：降级是管理员策略；透明度靠透明头 + 审计而非 opt-in；opt-out 比 opt-in 少一层默认拒绝逻辑 |
| 3 | 降级透明度 | 响应头 `X-AI-Gateway-Fallback` + 审计 `fallback_chain` | 客户端"默认不知道" ≠ "无法知道" |

> **未来风险提示**：若产品走向对外多租户，`model_map` 应翻转为 per-key 默认关闭（opt-in 方向）——对外调用方对 SLA 有契约话语权。届时仅改默认值与文档，机制不变。
