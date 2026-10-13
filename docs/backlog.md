# 项目待办与路线（Backlog）

> 本文件是项目的**活文档**：记录安全/健壮性相关的待修缺陷、PII 方案设计与路线图。
> 定位与决策依据见各章节；状态随实现推进更新。

---

## 0. 定位与威胁模型（决策前提）

- **使用场景**：公司内网部署（内部员工使用），非公网、不对外部客户暴露。
- **威胁模型**：**防呆**（防员工手滑/无意违规、泄露内部信息）+ 防内部滥用；**不是**对抗外部攻击者。
- **推论**：
  1. 规则级防御（敏感词 + 防注入正则 + 伪装 403）的定位正确，阶段 0 即可覆盖内网 90% 真实需求；
  2. **误报率优先级 > 覆盖率**：误杀员工合法请求（如 coding 工具）的代价远高于漏掉一次攻击；
  3. 语义检测 / 向量分类 / 外部数据源校验属 v0.5+ 范畴，v0.1 内网定位明确不做。

---

## 1. 待修缺陷（代码）

| # | 项 | 优先级 | 状态 | 说明 |
|---|---|---|---|---|
| 1 | `injectTemplate` 覆盖多模态 content 数组 | 🔴 高 | ✅ 已修复 | `content` 为数组（多模态：图片+文本）时被断言为空串后覆盖为字符串，图片内容丢失；现改为改写首个 text 部件并保留 image_url/audio，无 user 消息时追加 user 消息而非误改 system |
| 2 | `extractPromptText` 不提取数组 content | 🔴 高 | ✅ 已修复 | 多模态/文件内容的文本提取不到 → 安全扫描盲区；现兼容 string 与 []any 两种 content 形态 |
| 3 | 敏感词/防注入正则每次请求重新编译 | 🟡 中 | ✅ 已修复 | `config.Security` 新增 `SensitiveRegexps()` / `InjectionRegexps()`（sync.Once 惰性预编译），security_guard 零重复编译，热加载重建配置时自动重编译 |
| 4 | 被 PreRequest 拦截的请求（401/403）不落审计 | 🟡 中 | ✅ 已修复 | 安全事件无痕，无法统计拦截量；现 `kernel.EmitAbortAudit` 为 401/403/502 拦截落一条审计（含 `http_status`/`success=false`/`abort_reason`），obs 展示拦截标记 |
| 7 | 请求体无大小上限 | 🟡 中 | ✅ 已修复 | `transport.max_request_body_bytes` + `ReadBody` 限额 + 网关入口 Content-Length 早检，超限 413 落审计；见「内存防线」层 1 |
| 8 | 非流式响应体全量缓冲无上限 | 🟡 中 | ✅ 已修复 | `transport.max_audit_body_bytes` 限额 `jsonAuditBody` 缓冲，超限停止缓冲（仍转发）、usage 降级 estimated；见「内存防线」层 2 |
| 9 | 代理阶段错误（404/502/503）不落审计 | 🟡 中 | ⬜ 待修 | **端到端演练发现**（2025-08-28）：路由不匹配 `404 no route`、全上游熔断 `503`、代理错误 `502` 走 `defaultErrorHandler`，既不经过 `writeAbort` 也不经过 auditor（RoundTrip 未成功）→ 无审计记录。#4 只覆盖 PreRequest 拦截的 401/403/429/413/502。<br>**修法**（约 10 行）：`kernel/proxy.go` 的 `defaultErrorHandler` 内用 `PluginContextFromRequest(r)` 取上下文，调用现成 `EmitAbortAudit(ctx, status, code)`。 |

---

## 1.5 内存防线（多人并发场景）

> 背景：多人同时使用时内存吃紧，Go 进程**不会优雅返回"内存不足"**，而是「变慢→超时→断路器切流→GC 风暴→OOM 崩溃」。必须主动分层防，不能靠等它报错。

### 内存消耗点（代码事实）

| 消耗点 | 现状 | 风险 |
|---|---|---|
| 请求体 | `ReadBody` 无限 append；failover 还缓存 `origBody` | 🔴 单请求可吃爆 |
| **非流式响应体** | `jsonAuditBody.buf` 全量缓冲再解析 usage | 🔴 单响应可吃爆（隐藏点） |
| 并发 × 每请求开销 | 无并发上限，来一个起一个 goroutine | 🔴 多人同时打爆 |
| SSE 流式路径 | `pending`/`lineBuf` 边读边排空 | ✅ 有界安全 |
| EdgeStorage 队列 | 容量 4096 | ✅ 有界安全 |

### 流式 vs 非流式对内存的影响

| | 流式（stream:true） | 非流式（stream:false） |
|---|---|---|
| 响应形态 | SSE 逐行 + [DONE] | 一次性完整 JSON |
| 网关内存 | ✅ 边读边转，有界 | 🔴 全量缓冲再解析 |
| 审计精度 | 可捕获官方 usage（official_cached） | 超限放弃缓冲后降级 estimated |

### 五层防线

| 层 | 内容 | 挡什么 | 状态 |
|---|---|---|---|
| 1 | 请求体上限（`max_request_body_bytes`） | 单请求灾难 | ✅ 已实现 |
| 2 | 响应体缓冲上限（`max_audit_body_bytes`） | 单响应灾难 | ✅ 已实现 |
| 3 | 并发准入（in-flight 信号量，满则 429 快速失败不排队） | 多人并发堆积 | ✅ 已实现 |
| 4 | 内存水位熔断（软阈值**降并发** / 硬阈值 GC+拒绝；cgroup 75-90% + 迟滞） | GC 风暴→OOM | ✅ 已实现 |
| 5 | 可观测（/metrics + 启动日志；obs 容量卡片留待） | 预警 | ✅ 部分（/metrics + 启动日志已实现） |

### 硬件自适应（层 3/4 的配置来源）

- **启动时探测 + 自动推导**：读系统内存（**容器内优先读 cgroup `memory.max`**，否则 `/proc/meminfo`），按比例推导并发上限与软/硬阈值，结果 clamp 到安全区间；
- **人工覆盖优先**：config 显式写死值 > 自动推导；
- **最大支持人数只能估数量级**：`max_inflight / 平均每用户并发`，不承诺精确值，用 bench 实测标定 + obs 容量卡片展示「当前并发/上限」；
- **动态自适应**（内存高→收紧准入）是层 4 的核心，见下方「水位阈值」。

### 水位阈值（层 4 关键修正）

> ⚠️ **不要用 40% 堆比例**：`HeapAlloc` 含大量未回收垃圾，忙时到 40% 是常态，不代表快 OOM；且单次采样超阈值就 429 会「闪断闪恢复」。

| 项 | 正确做法 |
|---|---|
| 指标 | cgroup `memory.current / memory.max`（容器内）或 RSS / 总内存（裸机） |
| 软阈值 | 75%~80% → **触发「降并发」（调低 in-flight 上限），不是直接 429** |
| 硬阈值 | 90% → 拒绝新请求 + 主动 `runtime.GC()` |
| 迟滞 | 连续 N 次采样（如 3 次）超阈值才触发，回落 M 次才恢复 ← 防抖动 |

### 落地顺序

1. 层 1 + 层 2（已完成）：绝对值上限，config 可覆盖 + 合理默认（宽松兜底）；
2. 层 3 + 层 4：自动推导 + 可覆盖（含水位迟滞、降并发优先）；
3. 层 5：可观测。

### 设计注意事项（本轮补充）

1. **慢客户端占槽**：层 3 信号量覆盖整个请求生命周期（含响应写完），慢客户端会长期占用槽位 → 需配合 `http.Server` 的 `WriteTimeout` / `ReadTimeout`；
2. **http.Server 超时参数缺失**：现在 `main.go` 的 `http.Server{Addr, Handler}` 未设 `ReadTimeout` / `WriteTimeout` / `IdleTimeout` / `MaxHeaderBytes`——慢客户端与 header 轰炸的另一层隐患，应一并补；
3. **审计 goroutine 无上限**：异步 `emitAudit` goroutine 目前无并发上限，极端流量下会堆积，可加信号量或 worker 池复用；
4. **信号量释放位置**：必须在 `ServeHTTP` 返回时释放（覆盖完整生命周期），不能在 PreRequest 后提前释放，否则慢响应会绕过准入。

---

## 1.6 vKey 生命周期与限流（roadmap R1 设计要点）

### keygen 分配 key（不搞「只显示一次」）

> 决策：key 可查、可作废、有审计兜底，**不做"一次性显示"的保密仪式**——内网防呆定位下成本低、摩擦小。

- 生成用 `crypto/rand` 32 字节 hex（防猜测，非保密仪式）；
- key 明文存 config.yaml，**随时可查**；泄露/丢失 → 作废重生成即可；
- 有拦截审计兜底（backlog #4）：key 被滥用时能查到 `invalid_api_key` 尝试记录；
- 命令（已实现）：`keygen -username 张三 -department 研发部 -whitelist deepseek-v4-flash,qwen-turbo -tags llm,coding -rate_limit 10 -quota_daily 1000000` → 生成随机 key + **打印可粘贴的 config 片段**（不自动写回，手写 YAML 无序列化器，写回易错）；
- 吊销（已实现）：给条目加 `disabled: true`（热加载生效），无需删条目。

### key 有效期（过期控制）

> 决策：**过期非刚需，默认不做；HMAC 无状态签发明确砍掉**（把过期/权限编码进 key 复杂度高、收益低，违背内网实用优先）。生命周期收敛为「keygen 生成 + `disabled` 吊销 + 审计兜底」。

- 若未来确实需要，可加可选字段 `auth.keys[].expires_at`（空 = 永久，向后兼容），auth_vkey 惰性判断 `now > expires_at` → 401 `key_expired` + 落审计；
- 不做 HMAC 无状态签发（不把过期时间编码进 key 本身）。

### 限流达到后如何扩容

- 改 config.yaml 调大 `rate_limit` / `quota` → **热加载生效（2s，不中断）**；
- **额度状态必须跨热加载/重启保留**：令牌桶/计数挂在 Gateway（如 `NodeRuntime` 一样），不随配置重建归零；日配额「今日已用」需落盘，否则 reload/重启即重置 = 无限额漏洞；
- **多实例**：v0.1 做单实例精确 + 文档明确标注「多实例下限流是近似的」（每实例独立）；精确共享限流需 Redis（违背零依赖），留到后续。

### 注意事项

1. **原子写**：`keygen --append` 写回 config.yaml 要「临时文件 + rename」，防热加载读到半截文件；
2. **配额/限流计数原子性**：并发下计数需 atomic/mutex，防超卖；
3. **暴力试 key 防护**：限流应含「按 IP 失败尝试限流」或全局试错限流，配合审计（否则可无限试 key）；
4. **限流触发的 429 也落审计**（与 #4 一致，`abort_reason=rate_limited`）。

---

## 1.7 边缘端侧算法接入（统一路由 + 标志位）

### 场景

车道违章等边缘场景：摄像头/边缘盒子上传图片 + 元数据（相机ID/时间戳/车道号）→ 网关 → 具体算法服务，返回判定结果。与 LLM 对话的差异：请求体是图片/结构化数据（非对话 JSON）、鉴权主体是**设备**、无 token 计费、低延迟要求、端侧可离线。

### 设计原则（规格书 §2 已确认，不搞显式路径插件链）

> **统一路由入口 + 标志位分派**：插件通过 `bind_paths` 声明适用路径边界；上游通过 `endpoint_path` 绑定；业务分流用领域标签（Domain-Tag Matching）；工业算法绕过大模型 Prompt/Token 插件流，但统一并入审计。

标志位清单（分派依据）：

| 标志位 | 决定什么 | 现状 |
|---|---|---|
| `path`（路由精确匹配） | 走哪条路由/哪些上游 | ✅ 已实现 |
| `endpoint_path`（上游路径） | 转发到哪个算法端点 | ✅ 已实现 |
| `domain_tag`（设备 tags × 算法 tags） | 挑哪个算法上游 | ✅ 已实现（`domain_tag` 策略） |
| body 特征（是否有 `messages`/`model`） | 是否对话体 → 是否走注入/安全插件 | ✅ 已实现（插件自动跳过非 JSON） |
| `bind_paths`（插件路径边界） | 哪些插件适用于该路径 | ⬜ 未显式实现（当前靠自动跳过兜底，见下） |

### 车道违章走当前设计的完整链路

```
摄像头（设备 key: sk-cam-01，tags: [iot, vision, lane]）
  │ POST /v1/vision/lane-violation
  │ body: {"image_base64":"...","camera_id":"C-01","lane":3,"ts":"..."}
  ▼
Gateway.ServeHTTP（统一入口）
  ├─ auth_vkey：设备 key 鉴权（可选 HAL 指纹绑定硬件）→ rate_limit/quota 检查
  ├─ prompt_injector：无 messages → 跳过注入（不污染算法输入）
  ├─ security_guard：无 prompt → PASSED
  └─ dynamic_router：path → domain_tag（设备 tags × 算法 tags）→ 选 lane_alg → Rewrite
  ▼
反向代理 → 车道违章算法 → {"violation":true,"plate":"沪A12345","confidence":0.98}
  ▼
auditor：非流式 JSON 审计 → 落 uba.json
```

### 显式声明分发（algorithm，与 model 同构）

> 用户心智：算法分发 = 模型分发的同构翻版。LLM 客户端必须传 `model`（强约束分发标志位）；算法请求同样显式声明，路由按上游能力匹配 + 强约束兜底。

| | LLM（现状） | 算法（新设计） |
|---|---|---|
| 分发标志位 | body `model`（**客户端必须传**，缺省白名单/模型过滤不精确） | `X-Algorithm` 请求头（或 body `algorithm`） |
| 上游能力声明 | `upstreams[].models` | `upstreams[].algorithms`（新增） |
| 匹配逻辑 | `modelAllowed`（models 包含请求值；**未声明 models = 视为不限**） | 新增 `algorithmAllowed`（仿 modelAllowed） |
| 强约束 | 无上游支持 → 400 | 无上游支持 → 400/404 |
| 白名单 | vKey `model_whitelist` | （可选）vKey `algorithm_whitelist` |

### 配置约定（入口隔离，必须写死）

1. **入口分开**：LLM 走 `/v1/chat/completions`，算法走 `/v1/infer`（统一算法入口 + `X-Algorithm` 分发）；
2. **上游按类型分绑，禁止混绑**：LLM 上游绑 LLM 入口，算法上游绑算法入口——混绑时"未声明 models 的算法上游会被 LLM 请求视为不限而串流"（`supportsModel` 空 models = 不限），是真实坑；
3. **匹配范围**：请求只遍历**该入口 `routers[].upstreams` 绑定的那组**上游（`match(path)` → `rule.Nodes` 内过滤/挑选），不匹配全局所有上游；O(入口绑定数)，性能可忽略；
4. **两个分发语义互不干扰**：LLM 上游不声明 `algorithms`、算法上游不声明 `models` → 各自分派逻辑对对方请求不触发。

### 差距与待办

1. **`algorithm` 显式分发扩展**（规格书 §2 语义 + 上述同构设计）：config `upstreams[].algorithms` 字段 + `RouteCandidates` 读 `X-Algorithm`/body `algorithm` 精确选上游（仿 `modelAllowed` 写 `algorithmAllowed`），声明缺失时回退现有策略（向后兼容）；与 LLM 设计零冲突（见上）；
2. **`bind_paths` 插件路径边界**（规格书原设计，可选增强）：插件声明 `bind_paths`，Pipeline 按请求路径过滤适用插件——比"自动跳过"更显式、可配置，是端侧多算法接入的正解；标志位驱动，不引入独立插件链配置；
3. **算法审计语义**：当前审计面向 OpenAI usage（token），算法无 token；需为算法路径记录调用次数/耗时/结果状态/入参大小（而非 token），否则"哪个相机调用最频繁、算法有没有判错"不可查；
4. **设备 key 用法文档化**：vKey 属性面向人（username/department/template），设备 key 复用同一套（username 填设备名、tags 填业务意图），需在文档明确用法；
5. **端侧配置参考**：图片上传需显式配置 `transport.max_request_body_bytes`；低延迟场景调 `ttft_timeout_sec`。

---

## 1.8 白名单定位调整（个人级 → 角色/部门档位集）—— 低优先级

> 决策（2025-08）：**模型选择机制不要掺"个人级白名单"**——个人白名单把模型选择复杂化（每人一份清单、每个插件都要带个人约束、加模型要改所有人）。改为「角色/部门档位集 + 上游能力」两级约束。

### 现状问题

- 每个 vKey 一个 `model_whitelist`（个人级）；
- 内容路由/时间路由/别名选档都要 "意图档位 ∩ 个人白名单" → 复杂度随插件数增长；
- 新增模型 = 改所有个人白名单（README 已知限制的放大版）。

### 目标设计

```yaml
# 全局档位表（不是每人一份清单）
model_tiers:
  default:  [qwen-turbo]
  engineer: [qwen-turbo, deepseek-r1]

# vKey 只声明"角色归属"，不重复维护模型清单
auth:
  keys:
    - vkey: "sk-zhangsan"
      department: "研发部"     # → 命中 engineer 档位集
```

**模型选择规则** = 意图档位 ∩ 角色档位集 ∩ 上游能力（全部集合级约束，无个人维度）。
内容路由/时间路由在**档位集内选择**（构造上合法），不做事后回退（见 content-routing.md 第 4 节约束 3）。

### 取舍

- 粒度从"个人"变"部门/角色"——同部门权限相同，多数内网场景够用；
- 保留个人级 `model_whitelist` 解析作向后兼容 / 例外覆盖（默认无）；
- **优先级放低**：与内容路由/时间路由实现时一并处理，不单独立项。

---

## 2. PII 规则引擎（方案 B，阶段 2）

### 目标

结构化识别个人敏感信息（手机号/身份证等），支持**动作分级**，而非一刀切 403。

### 设计（已确认）

```yaml
security:
  pii_rules:                    # 新增：与 sensitive_words / injection_patterns 语义分离
    - name: mobile_cn
      type: mobile_cn           # 内置号段校验：1[3-9]\d{9} + 号段白名单
      action: redact            # block=403拦截 | redact=脱敏后放行 | audit=仅审计
    - name: id_card_cn
      type: id_card_cn          # 内置校验位算法（ISO 7064 MOD 11-2），区分真证与任意 18 位数字
      action: redact
```

### 关键实现点（零依赖约束内可用标准库完成）

1. **校验降误报**：身份证必须跑 MOD 11-2 校验位；手机号做号段白名单（13x/15x/18x/19x…）。
2. **归一化**：去空格/横线/括号、全角转半角、剥离 `+86`/`86` 前缀、`X` 统一大写（防 `138 0000 0000` 绕过）。
3. **redact 动作**：命中处替换 `***` 后重写 Body 继续转发（复用 `io.NopCloser` + 更新 `ContentLength`），脱敏前后记入 Context。
4. **响应侧 DLP**：ModifyResponse 阶段对模型输出扫同规则，可脱敏/拦截（生产合规刚需，当前完全空白）。
5. **审计落盘**：新增 `pii_hits`（类型/位置/动作）；拦截请求落审计（顺带解决 #4）。
6. **预编译**：规则在插件 `Init` 时编译一次（顺带解决 #3）。

### 前置依赖（硬约束）

> **必须先实现 #6（coding 工具误报防护）再上线 PII**——否则代码仓库里合法的 `13800138000`、测试身份证会被 403 误杀，安全功能第一周就会被下线。

---

## 3. Coding 工具误报防护（#6，PII 的前置）

### 背景

Qwen Code / Cursor 等编码工具把**文件内容以纯文本**塞进 `messages`（非附件类型），进入网关后与用户指令混在同一段文本里被扫描 → 合法代码/文档中的指令短语、测试手机号/身份证会命中规则被 403。

### 实现方向（选一或组合，实现前确认）

1. **代码块边界识别**：解析 ` ```...``` ` 代码块，PII/敏感词扫描区分块内外（块内默认 `audit`/`redact` 而非 `block`）；
2. **路径/模型豁免**：按路由路径或模型（如 `qwen-coder`）提供豁免/降级配置；
3. **来源标注**：扫描结果记录命中来源（user 指令 vs 文件内容），供审计与误报分析。

### 附加事实（已核实）

- OpenAI 兼容 `/v1/chat/completions` 协议无"附件"概念，文件内容 = user 消息字符串；
- `extractPromptText` **不过滤 role**：system/assistant/tool 消息的字符串 content 也会被拼进扫描文本（对 DLP 有利，但放大误报面）。

---

## 4. 路线图

| 阶段 | 内容 | 性质 | 状态 |
|---|---|---|---|
| **阶段 0** | 扩充 `injection_patterns`（保守子集 ①② 启用，③-⑥ 注释留档）+ 敏感词表配全 | 零代码，改配置即生效（热加载） | ✅ 已写入 `config.example.yaml`（2025-08） |
| **阶段 1** | 修 #1 多模态覆盖、#2 数组 content 提取、#3 正则预编译 | 低风险高确定性代码改动 | ⬜ 待排期 |
| **阶段 2** | PII 方案 B（校验位 + redact/block/audit + 响应侧 DLP + `pii_hits` 审计） | 需设计确认 | ⬜ 前置依赖 #6/#4/#7 |
| **明确不做**（v0.1） | 语义检测、向量分类器、外部数据源校验 | — | — |

依赖链：**阶段 2 = #6（coding 误报防护）→ #4（拦截落审计）→ #7（body 上限）→ PII 方案 B**。

---

## 5. 防注入清单启用建议

| 子集 | 内容 | 建议 | 理由 |
|---|---|---|---|
| ① 指令覆盖 | `ignore previous instructions` 等 9 条 | **立即启用** | 覆盖最常见越权指令，业务文本误报面小 |
| ② 系统提示词窃取 | `reveal your system prompt` 等 10 条 | **立即启用** | 覆盖套取系统提示词，误报面小 |
| ③ 角色越狱 | DAN / jailbreak 等 5 条 | 可启用 | 意图明确，误报很低 |
| ④ 编码/翻译绕行 | base64 / translate 等 3 条 | 谨慎 | 英文短语与技术文档/代码注释重叠 |
| ⑤ 间接注入 | `执行以下新指令` 等 6 条 | 内部先关 | 与正常任务描述重叠，误杀概率最高 |
| ⑥ 数据外带 | `复述整个对话` 等 5 条 | 先关 | 与正常沟通重叠 |

**上线流程建议**：先保守子集（当前 config.example.yaml 状态）跑 1 周，观察 `security_check` 命中与误报，再决定放量；彻底解决误报依赖 #6。

---

## 6. 关联文档

- 插件机制与 security_guard 行为：[docs/plugins.md](./plugins.md)
- 配置字段参考：[docs/config-reference.md](./config-reference.md)
- 审计日志字段：[docs/audit-log.md](./audit-log.md)
- 性能基线（PII 规则上线前后建议复测）：[docs/benchmark.md](./benchmark.md)
