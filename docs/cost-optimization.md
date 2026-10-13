# Token 成本优化方案（Cost Optimization）

> 目标：降低 token 使用与模型调用成本。**先度量再优化**——obs 已有成本分布与缓存命中率，先看清钱花在哪个用户/模型/上游、缓存命中多少，再决定上哪个方案。

---

## 1. 方案总览

| 层 | 方案 | 现状 | 省 token 量级 | 优先级 |
|---|---|---|---|---|
| 输入侧 | Prompt Cache 友好（固定前缀命中官方缓存） | ✅ 有 80% 地基 | 🟠 大（价格 ~1/10） | 0（零代码） |
| 输入侧 | 语义缓存（相同/相似请求直接返回，0 token） | ⬜ roadmap R6 | 🔴 最大头 | 2 |
| 模型侧 | 内容路由（简单→便宜模型） | 📐 设计稿就绪（content-routing.md） | 🟠 大 | 3 |
| 模型侧 | cost 策略（同模型挑最便宜上游） | ✅ 已实现 | 🟡 中 | 0（改配置） |
| 输出侧 | max_tokens 上限 | ⬜ 小改动 | 🟡 中 | 4 |
| 服务商侧 | 模型降级 fallback（贵挂→切便宜） | ✅ M3 已实现 | 🟡 兜底 | 1 |
| 治理 | quota_daily 预算 + obs 成本看板 | ✅ R1 已实现 | 控成本天花板 | 0（已就绪） |

---

## 2. 逐项说明

### 2.1 Prompt Cache 友好（先做，零代码）

DeepSeek/千问的 Prompt Cache：输入**前缀相同** → 缓存命中，价格约 1/10。

- 利用现有 `injector.templates` 的 `head`：所有请求的 system 前缀一致 = 天然缓存命中——**把企业准则模板写得长而稳定，命中率越高**；
- auditor 已捕获 `usage.prompt_tokens_details.cached_tokens`，obs 已有缓存命中数——先看数据；
- 动作：统一/稳定 system 前缀（配置），零代码。

### 2.2 语义缓存（R6，头号降本）—— ⏸️ 暂缓实现

**状态决策（2025-08）**：当前业务场景（对话/端侧算法）重复请求少，命中率预期低，**暂不实现**。保留为「知识库等高频重复场景」的专属能力——**该网关可插拔支持此类场景的证据**。

相同/语义相似请求直接返回缓存，**不调上游 = 0 token**。适合 FAQ、固定模板、重复查询。

- 纯插件可做（PreRequest 查缓存 → ModifyResponse 写缓存）；
- **边界（生死线）**：只缓存安全幂等请求——必须跳过 `stream:true`、非默认随机参数、个性化/敏感请求；**按 vKey 隔离缓存键，防跨用户数据泄露**；
- 缓存需有界（`max_entries` + TTL），否则自身变成内存炸弹（呼应 backlog 1.5）；
- 启用信号：obs 中若出现大量 prompt 几乎相同的请求（知识库问答），即值得启用；
- 实现时：指纹 = `vKey + model + messages` 哈希，`per_key_isolation`、`ttl_sec`、`max_entries` 配置。

### 2.3 内容路由（阶段 1 设计稿就绪）

简单请求 → 便宜模型（qwen-turbo）；复杂请求 → 推理模型。见 [content-routing.md](./content-routing.md)：

- 阶段 1 纯插件（改写 `CtxModel`，白名单自动生效），不改内核；
- 阶段 2 依赖 fallback 的 model_map 做档位降级。

### 2.4 cost 策略（已实现，改配置即用）

```yaml
routers:
  - path: /v1/chat/completions
    strategy: cost          # 同模型多上游时永远挑 cost_per_1k 最低的
    upstreams: ["deepseek", "qwen"]
```

### 2.5 max_tokens 上限（输出侧，小改动）

每 vKey/每请求设 `max_tokens` 上限，防止模型滔滔不绝。控制输出 token——对话场景输出占比高时收益明显。

### 2.6 模型降级 fallback（已实现）

上游挂 → 按 model_map 切到便宜模型（M3）。贵模型故障时的成本兜底。

### 2.7 治理（成本天花板）

- `quota_daily`（R1）：每日 token 预算，用完 429——硬上限；
- obs 成本看板：按用户/模型/上游看钱流向。

---

## 3. 时间路由（峰谷价，纯插件方案）

### 需求

部分模型有谷价/峰价（时段定价）。希望根据当前时间调用不同模型：**峰时用便宜档，谷时用贵档**（或反之）。

### 结论：纯插件可实现（与内容路由同构，零内核改动）

时间路由 = 内容路由换一个维度：内容路由按"复杂度"改写 `CtxModel`，时间路由按"当前时段"改写 `CtxModel`——**机制完全一样**：

```go
// 插件 time_routing（PreRequest，order < dynamic_router）
func (p *TimeRoutingPlugin) PreRequest(ctx, req) error {
    if inPeakHours(time.Now(), p.config.peakWindows) {
        ctx.Set(kernel.CtxModel, p.config.peakModel)    // 峰时 → 便宜模型
    } else {
        ctx.Set(kernel.CtxModel, p.config.offPeakModel) // 谷时 → 贵模型
    }
    return nil
}
```

- 路由自动按新 model 过滤/选上游（`modelAllowed` 读 `CtxModel`）；
- vKey 白名单自动生效（改写后的模型必须过白名单）；
- 与内容路由插件可串联（先复杂度定档、再时间调档）。

**配置**（插件私有 config）：

```yaml
plugins:
  - name: time_routing
    enabled: false
    order: 3                    # < dynamic_router(4)
    config:
      peak_windows: [["08:00","18:00"]]   # 峰时窗口
      peak_model: qwen-turbo              # 峰时用便宜档
      offpeak_model: deepseek-r1          # 谷时用贵档
```

### 边界与前提

1. **同一模型名无法表达峰谷**：若"峰谷"是同一个模型名的两个价格（无第二个模型可切），改写 `CtxModel` 不适用——但此类场景网关通常无需动作（计费由服务商处理），或峰时降级到便宜模型（仍是改写 model）；
2. **改的是"模型档位"而非"上游"**：若需求是"同模型多上游按峰谷挑上游"（少见），受限于内核策略注册表未开放——需内核加 `RegisterStrategy` 导出函数（约 10 行，与 `RegisterPlugin` 对称）后即可纯插件注册 `time_based` 策略；否则仍是改写模型方案；
3. **时区**：峰谷窗口按服务商时区声明（config 注明时区或统一 UTC+8）；
4. **与白名单/审计联动**：改写后模型过白名单；审计记录实际使用模型（content-routing.md 已有约定）。

---

## 4. 推荐落地顺序

| 顺序 | 方案 | 类型 |
|---|---|---|
| 0 | 看 obs 成本/缓存数据 + cost 策略换配置 | 零代码 |
| 1 | Prompt Cache 友好（稳定 head） | 零代码 |
| 2 | 语义缓存（R6） | 纯插件 |
| 3 | 内容路由阶段 1 | 纯插件 |
| 4 | 时间路由（峰谷） | 纯插件（同构） |
| 5 | max_tokens 上限 | 小改动 |

## 5. 关联

- 内容路由设计：[content-routing.md](./content-routing.md)
- 上游成本字段：`config-reference.md` 的 `upstreams[].cost_per_1k`
- 配额治理：`docs/backlog.md` 1.6
