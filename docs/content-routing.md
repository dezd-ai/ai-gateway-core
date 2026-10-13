# 内容感知模型路由（Content-Aware Model Routing）设计

> **状态**：未来（vNext）设计稿 —— **依赖 fallback 内核改造（[fallback-design.md](./fallback-design.md)）完成后实施**。
> **定位**：简单请求 → 便宜模型；复杂请求 → 推理模型；按请求内容动态定档。
> **关联**：[roadmap.md](./roadmap.md) R17 · [plugins.md](./plugins.md)（插件机制）· [config-reference.md](./config-reference.md)。

---

## 0. 目标

```
"帮我写个 hello world"          → 便宜档（qwen-turbo）
"分析这段代码的并发安全并给出优化" → 推理档（deepseek-r1）
"今天天气怎么样"                → 便宜档
```

按请求内容的复杂度/类型，在**多档模型**间动态选择，实现成本优化。

---

## 1. 为什么可以纯插件实现（内核已具备的扩展点）

| 内核事实 | 含义 |
|---|---|
| `CtxModel` 是插件可写的 Context 键（`kernel/context.go`） | 插件改写它 = 改写路由输入 |
| `dynamic_router` 的模型过滤（`modelAllowed`）读 `ctx.GetString(CtxModel)` | 改完的模型自动参与路由与白名单校验 |
| 白名单强约束（`auth.keys[].model_whitelist`） | 定档后的模型必须过白名单，越权被拦 |

**唯一约束**：内容评估插件必须**先于** `dynamic_router`（order 4）执行，即在 `prompt_injector`（order 2）之后、`dynamic_router` 之前挂载（如 order 3，与 security_guard 同段需避免冲突，建议独立 order）。

---

## 2. 插件逻辑草案（`plugins/model_routing.go`）

```
PreRequest 阶段：
  1. 读取 CtxFinalPrompt / CtxOriginalPrompt / token 预估（injector 已写入）
  2. 复杂度评估：
     - token 数 < 阈值 且 无推理类关键词 → 简单
     - 含 "分析/推理/代码/算法/并发/优化" 等关键词 或 token 数大 → 复杂
     - 可扩展：按路由路径 / 按用户 department 定制
  3. 改写 ctx.Set(CtxModel, 目标档位模型)
  4. 记录 ctx.Set(CtxModelTier, "cheap"|"reasoning")  供审计
```

**配置草案**（插件私有 config，挂载在 `plugins[].config`）：

```yaml
plugins:
  - name: model_routing
    enabled: false          # 默认关闭，显式开启
    order: 3                # 必须 < dynamic_router(4)
    fail_strategy: bypass
    config:
      cheap_model: qwen-turbo
      reasoning_model: deepseek-r1
      cheap_token_threshold: 200
      reasoning_keywords: ["分析", "推理", "并发", "算法", "优化", "review"]
```

**上游声明配套**（config.yaml，多档模型必须有上游支持）：

```yaml
upstreams:
  - name: qwen
    models: ["qwen-turbo"]          # 便宜档
  - name: deepseek
    models: ["deepseek-v4-flash", "deepseek-r1"]   # 推理档
```

---

## 3. 与 fallback 的衔接（未来内核配合点）

| 阶段 | 能力 | 依赖 |
|---|---|---|
| **阶段 1（纯插件）** | 内容定档 + 改写模型；推理模型挂了 → 502（无降档） | 无需内核改造 |
| **阶段 2（fallback 落地后）** | 插件把"期望档位"写入 Context（如 `gw_model_tier`），fallback 候选链**按档位降级**：推理档挂 → 自动切便宜档 | 内核 fallback 的 `model_map` 机制 + 档位感知候选链 |

阶段 2 的具体机制：fallback 构建候选链时，若首选 (deepseek, deepseek-r1) 失败，按 model_map 降级到 (qwen, qwen-turbo)——**降级方向仍必须显式声明**（fallback-design.md 第 4 节约束），禁止自动"随便挑个便宜的"。

---

## 4. 边界与限制（写死的）

1. **流式中途不能改档**：响应已开始，无法切换（与 fallback 窗口 B 一致）；
2. **跨请求画像**（按用户历史行为调档）：内核不提供跨请求状态，插件需自管（不推荐 v1 做）；
3. **【强制约束】模型策略选择时即保证白名单（不靠事后回退）**：
   - 改写 `CtxModel` 的插件在**挑选目标模型时**，选择域 = 「角色/部门档位集 ∩ 上游能力」，从构造上保证最终模型合法；
   - **不做"改写后再校验再回退"**（多插件串联时每层回退逻辑分散、易错）；
   - 后续白名单检查仅作**防御性断言**（发现越权 → 日志告警），不承担业务回退；
   - 详见 [backlog.md](./backlog.md) 1.8「白名单定位调整」；
4. **误定档风险**：关键词启发式会误判（"帮我分析一下为什么报错"可能是简单问题）。评估器要可观测——审计记录 `model_tier` + 评估依据（token 数/命中关键词），便于事后校准阈值；
5. **成本对账**：定档/降级都必须在审计 `model` 字段如实记录**实际使用**的模型，不能只记请求原始模型。

---

## 5. 落地路径

```
阶段 1（已完成 2025-08，纯插件）：
  plugins/model_routing.go —— 评估复杂度（token 阈值 + 关键词）→ 改写 CtxModel；
  白名单保证：选择域 = 意图档位 ∩ 用户可用模型（当前来自 vKey model_whitelist，
  未来按 backlog 1.8 换角色档位集）；非对话请求跳过。
  配置示例见 config.example.yaml（默认未启用，启用需调整 order）。

阶段 2（内核配合，随 fallback 迭代）：
  档位感知候选链：推理挂 → 降级便宜档（依赖 fallback-design.md M3 的 model_map）
```

> 当前进度约定：**先实现 [fallback-design.md](./fallback-design.md)（M1→M3），本设计进入 backlog 待命**；阶段 1 与阶段 2 均不再需要改内核路由核心（仅依赖 fallback 已落地的 model_map 能力）。
