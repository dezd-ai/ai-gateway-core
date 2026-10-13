# 模型别名设计（Model Aliases，roadmap R3 展开）

> **状态**：设计稿（2025-08），待实现。
> **目标**：客户端用"逻辑名（别名）"，网关翻译成真实上游模型名——客户端与后端完全解耦，换模型/供应商不改客户端代码。
> **关联**：`roadmap.md` R3 · `content-routing.md`（同为"改写 CtxModel"机制）· `agent-mode.md`（对 agent 隐形原则）

---

## 1. 解决的问题

| 痛点 | 例子 |
|---|---|
| 多供应商模型名不一致 | 客户端要记 `deepseek-chat`、`qwen-plus`、`glm-4`… |
| 供应商改名/换型号 | `deepseek-chat` 升级为 `deepseek-v4`，所有客户端代码要改 |
| 多云切换 | 从 A 家换 B 家（价格/质量），客户端不想改 |
| SDK 兼容 | 想让 openai-python/openai-node 直接连（`base_url` 指向网关），需要稳定的模型名 |

**类比**：DNS——客户端用域名（别名），网关解析成 IP（真实模型）。

---

## 2. 设计（两部分）

### ① 别名映射（config）

```yaml
model_aliases:
  "chat":      "deepseek-v4-flash"    # 客户端传 chat → 翻译为 deepseek-v4-flash
  "reasoning": "deepseek-r1"
  "cheap":     "qwen-turbo"
```

请求流程：

```
客户端 {"model":"chat"}
  → injector 提取 model="chat" 写入 CtxModel
  → 别名翻译：chat → deepseek-v4-flash（改写 CtxModel）
  → 白名单 / 模型过滤 / 上游选择 全用真实名 deepseek-v4-flash
```

- **机制**：与内容路由/时间路由一致——都是"改写 `CtxModel`"，但别名是**翻译**（语义等价），不是"降级/调档"（语义变化）；
- **顺序**：别名翻译应**最早**（injector 之后立即），这样后续的内容路由/时间路由在白名单校验时看到的是真实模型名；
- **未命中别名**：按原样传递（向后兼容，客户端传真实模型名照常工作）。

### ② `/v1/models` 端点

```
GET /v1/models
→ {
    "object": "list",
    "data": [
      {"id": "chat",      "object": "model", "owned_by": "gateway"},
      {"id": "reasoning", "object": "model", "owned_by": "gateway"},
      {"id": "cheap",     "object": "model", "owned_by": "gateway"}
    ]
  }
```

- 客户端拉一次即知可用的别名列表，无需查文档；
- 返回**别名**（客户端视角），不暴露真实上游模型名（可配置是否暴露）；
- 需要鉴权（走 vKey），可按 key 过滤（只列该 key 白名单内的别名）。

---

## 3. 与其它"改写 CtxModel"机制的关系

```
客户端传别名 chat
  → ① 别名翻译       chat → deepseek-v4-flash      （本设计，语义等价翻译）
  → ② 内容路由       按复杂度 → qwen-turbo / r1     （语义变化：定档）
  → ③ 时间路由       按峰谷 → 调档                  （语义变化：调档）
  → 路由选上游
```

三者同一机制（改写 CtxModel），**顺序固定：别名翻译 → 内容路由 → 时间路由**（先翻译成真实名，再做策略调整）。

**agent 模式**：别名翻译**保留**（翻译不是换模型，不违反"不干预"）；内容/时间路由对 agent 禁用（已实现）。

---

## 4. 白名单与别名（重要约束）

- 白名单（`model_whitelist` / 未来角色档位集）校验的是**翻译后的真实模型名**（因为翻译发生在最前）；
- 或者按需支持"白名单内写别名"（宽松模式）——**建议校验真实名**（唯一真相源，避免别名/真实名两套白名单）；
- backlog 1.8（白名单 → 角色档位集）落地后，别名翻译照常先执行，档位选择在真实名上做。

---

## 5. 实现落点（改动面预估）

| 落点 | 改动 |
|---|---|
| `config` | `model_aliases map[string]string` + Validate（目标模型需存在于某上游 `models`） |
| 翻译位置 | 新增插件 `alias_resolver`（order 紧随 injector 之后）或 injector 内一步；**推荐独立插件**（职责单一、可关闭） |
| 路由 | `GET /v1/models` 处理（当前 `Router.match` 无此路径 → 需在 Gateway 或新增 handler 处理） |
| 测试 | 别名翻译（命中/未命中/非对话跳过）+ `/v1/models` 返回 + 白名单校验真实名 |

**/v1/models 的实现选择**：
- 方案 A：Gateway 入口特判（类似 `/metrics`），返回别名列表——简单直接；
- 方案 B：作为一条路由（伪上游）处理——灵活但绕。

推荐 **A**（与 `/metrics` 一致的处理方式）。

---

## 6. 对开源/Agent 的意义

- **开源入场券**：OpenAI SDK 直连（`base_url` = 网关，`model` = 别名），零改造；
- **Agent 友好**：agent 用别名，换供应商/型号时 agent 代码不动——符合 roadmap 0.1「对 agent 隐形」；
- **迁移钩子**：从直连官方切到网关，客户端只需改 `base_url` + 用别名。

---

## 7. 开放问题（实现前确认）

1. **别名是否回写响应**？即响应体 `model` 字段返回别名（`chat`）还是真实名（`deepseek-v4-flash`）？——推荐返回**别名**（客户端视角一致），真实名落审计；
2. **`/v1/models` 是否按 vKey 过滤**（只列该 key 白名单内别名）——推荐是；
3. **别名翻译用独立插件还是 injector 内一步**——推荐独立插件（`alias_resolver`，order 紧随 injector）。

---

## 8. 关联文档

- 增长路线定位：`roadmap.md`（R3 + 0.1 Agent 时代定位）
- 同机制设计：`content-routing.md`（改写 CtxModel）
- Agent 原则：`agent-mode.md`（翻译保留、策略禁用）
- 白名单调整：`backlog.md` 1.8
