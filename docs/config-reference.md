# 配置参考（config.yaml）

> 网关的一切行为由 `config.yaml` 声明（GitOps 单一事实来源）。本文件是各配置区块的完整字段参考。
> 配置文件已被 `.gitignore` 忽略，随仓库发布的是 `config.example.yaml`（密钥一律走环境变量）。

---

## 1. 顶层结构

```yaml
server:        # 监听地址
log:           # 日志
auth:          # vKey 鉴权表
plugins:       # 插件挂载
upstreams:     # 上游节点
routers:       # 路由规则
security:      # 敏感词 + 防注入
injector:      # 部门提示词模板
transport:     # 超时与连接池
edge_storage:  # 边缘落盘
watchdog:      # 看门狗
```

---

## 2. server — 监听地址

| 字段 | 类型 | 默认 | 说明 |
|---|---|---|---|
| `host` | string | `0.0.0.0` | 监听 IP |
| `port` | int | `8080` | 监听端口（1-65535，Validate 强校验） |

---

## 3. log — 日志

| 字段 | 类型 | 默认 | 说明 |
|---|---|---|---|
| `level` | string | `info` | 日志级别 |
| `path` | string | `""` | 审计日志路径；留空按环境自动降级：Linux → `/var/log/ai-gateway/uba.json`，Mac → `./logs/uba.json` |

---

## 4. auth — 鉴权

| 字段 | 类型 | 默认 | 说明 |
|---|---|---|---|
| `hal_enabled` | bool | `false` | 启用硬件 HAL 指纹校验（TPM2.0/TEE）；Mac 无设备自动跳过并告警 |
| `keys[]` | 列表 | 必填（至少一把） | vKey 鉴权表 |

`keys[]` 字段：

| 字段 | 说明 |
|---|---|
| `vkey` | 明文密钥，O(1) Map 等值比对 |
| `username` | 用户名（审计记录用） |
| `department` | 部门（匹配 `injector.templates` 注入模板） |
| `model_whitelist` | **强约束**模型白名单：非空时，请求模型不在名单直接 403 `model_not_allowed` |
| `tags` | 多维画像标签（`domain_tag` 策略的匹配依据） |
| `hardware_fingerprint` | 硬件指纹（`hal_enabled` 时比对） |
| `rate_limit` | QPS 限流（每秒请求数，令牌桶），`0` = 不限；超限 429 `rate_limited` |
| `quota_daily` | 每日 token 配额，`0` = 不限；用尽 429 `quota_exceeded`（已用额度跨重启落盘 `quota.json`） |
| `disabled` | 吊销开关：`true` = 禁用该 key（403 `key_disabled`，热加载生效） |
| `agent_mode` | **Agent 宽松模式**：`true` = 治理保留（审计/配额/安全）、干预为零（不换模型/不注入/不掐超时）。见 [agent-mode.md](./agent-mode.md) |

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

## 5. plugins — 插件挂载

| 字段 | 类型 | 默认 | 说明 |
|---|---|---|---|
| `name` | string | 必填 | 插件注册名（见 `docs/plugins.md`） |
| `enabled` | bool | `true` | 是否启用 |
| `order` | int | `0` | 执行顺序（升序；**不可重复**，Validate 强校验） |
| `fail_strategy` | string | `abort` | `abort`（出错熔断）\| `bypass`（跳过继续） |
| `config` | map | `{}` | 插件私有配置（JSON 序列化后传入 `Plugin.Init`） |

```yaml
plugins:
  - name: auth_vkey
    enabled: true
    order: 1
    fail_strategy: abort
```

---

## 6. upstreams — 上游节点

| 字段 | 类型 | 默认 | 说明 |
|---|---|---|---|
| `name` | string | 必填 | 节点名（**全局唯一**，Validate 强校验） |
| `endpoint_path` | string | `""` | 转发到上游的路径覆盖（如 `/v1/chat/completions`） |
| `base_url` | string | 必填 | 上游基础 URL |
| `api_key` | string | `""` | 直接写密钥（不推荐） |
| `api_key_env` | string | `""` | 从环境变量读密钥（推荐，优先于 `api_key`） |
| `secret_key` | string | `""` | 百度千帆 client_secret |
| `secret_key_env` | string | `""` | 从环境变量读 secret |
| `protocol` | string | `openai` | `openai` \| `baidu_wenxin` |
| `auth_mode` | string | `direct` | `direct`（Bearer 直连）\| `token_exchange`（百度换钥，仅 `baidu_wenxin` 协议） |
| `models` | string[] | `[]` | 支持的模型列表；**强约束**：请求模型无任何上游支持 → 400 `no upstream supports model`；空列表视为不限 |
| `weight` | int | `1` | 权重（当前未参与策略计算，仅元数据） |
| `cost_per_1k` | float | `0` | 每 1k token 成本（`cost` 策略与 obs 成本统计用） |
| `tags` | string[] | `[]` | 领域标签（`domain_tag` 策略匹配依据） |
| `circuit` | map | 见下 | 断路器参数 |

`circuit` 字段：

| 字段 | 默认 | 说明 |
|---|---|---|
| `failure_threshold` | `5` | 连续失败阈值，达到即熔断 |
| `open_timeout_sec` | `30` | 熔断开启时长，到期进入 half_open |
| `half_open_max` | `3` | half_open 期间最大放行探测数 |

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

## 7. routers — 路由规则

| 字段 | 类型 | 默认 | 说明 |
|---|---|---|---|
| `path` | string | 必填 | 匹配路径（须以 `/` 开头，**不可重复**，Validate 强校验） |
| `strategy` | string | `round_robin` | `round_robin` \| `least_connection` \| `latency` \| `cost` \| `domain_tag` |
| `upstreams` | string[] | 必填 | 候选节点名列表（须引用已声明的 upstream） |
| `default_model` | string | `""` | 默认模型（当前仅元数据，不参与路由决策） |
| `domain_tags` | string[] | `[]` | 领域标签（**当前仅解析未参与匹配**，见已知限制） |

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

**路由三级过滤**：路径精确匹配 → 断路器过滤 → 模型强约束过滤 → 策略挑节点。

---

## 8. security — 安全防护

| 字段 | 说明 |
|---|---|
| `sensitive_words[]` | 敏感词字典（大小写不敏感子串匹配，命中 → 403） |
| `injection_patterns[]` | 防提示词注入正则（自动忽略大小写） |
| `block_response` | 命中后的伪装错误 JSON 体 |

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

## 9. injector — 部门提示词模板

| 字段 | 说明 |
|---|---|
| `templates.<部门>` | 按 `auth.keys[].department` 匹配的注入模板 |

每个模板含 `head`（注入到首个 user 消息头部）与 `tail`（尾部）：

```yaml
injector:
  templates:
    研发部:
      head: "你是企业内部研发助手，请遵守公司代码安全规范，输出严谨技术答案。"
      tail: ""
```

---

## 10. transport — 超时与连接池

| 字段 | 类型 | 默认 | 说明 |
|---|---|---|---|
| `connect_timeout_sec` | int | `5` | 建连超时（Dial + TLS） |
| `ttft_timeout_sec` | int | `10` | 首字响应（TTFT）超时 |
| `idle_read_timeout_sec` | int | `30` | 流式空闲读超时（超时强制断开，504 语义） |
| `max_idle_conns` | int | `100` | 每节点连接池全局最大空闲连接 |
| `max_idle_conns_per_host` | int | `10` | 每节点每 Host 最大空闲连接 |
| `max_request_body_bytes` | int64 | `0` | 请求体上限（字节），`0` = 不限制；超限返回 413（内存防线 层 1） |
| `max_audit_body_bytes` | int64 | `0` | 非流式响应审计缓冲上限，`0` = 不限制；超限放弃解析官方 usage、审计降级 estimated（内存防线 层 2） |
| `agent_ttft_timeout_sec` | int | `0` | agent key 的首字超时放大（秒），`0` = 沿用 `ttft_timeout_sec`（推理模型长思考） |
| `agent_max_request_body_bytes` | int64 | `0` | agent key 的请求体上限，`0` = 沿用 `max_request_body_bytes`（**非"不限"**） |
| `agent_gateway_meta` | bool | `false` | agent 模式是否注入 `gateway_meta` chunk（默认不注入，兼容严格 SSE 解析器） |

> Validate 强校验：三个超时均为正数。

---

## 11. edge_storage — 边缘落盘

| 字段 | 类型 | 默认 | 说明 |
|---|---|---|---|
| `enabled` | bool | `false` | 开启落盘 + 异步上报 |
| `max_disk_mb` | int64 | `512` | 环形文件上限，达阈值 FIFO 轮转（`.1` 覆盖） |
| `strategy` | string | `rotate` | 轮转策略（当前仅 `rotate`） |
| `report_url` | string | `""` | 远端上报地址（空则只落盘不上报） |

```yaml
edge_storage:
  enabled: true
  max_disk_mb: 512
  strategy: rotate
  report_url: "https://uba.example.com/ingest"
```

---

## 12. watchdog — 看门狗

| 字段 | 类型 | 默认 | 说明 |
|---|---|---|---|
| `enabled` | bool | `true` | 启用喂狗 |
| `device` | string | `/dev/watchdog` | 看门狗设备路径（Mac 无设备自动跳过并告警） |
| `interval_sec` | int | `30` | 喂狗周期 |

## 12.5 capacity — 容量与内存防线（层 3/4）

| 字段 | 类型 | 默认 | 说明 |
|---|---|---|---|
| `max_inflight` | int | `0`（自动） | 并发准入上限，满则 429 `busy`；自动 = 每 5MB 内存 1 并发，clamp 16~4096 |
| `mem_soft_percent` | int | `0`（自动 80） | 内存软阈值（cgroup `memory.current/max` 或 /proc 近似），触发**降并发**（上限减半）而非拒绝 |
| `mem_hard_percent` | int | `0`（自动 90） | 内存硬阈值，触发拒绝新请求 429 `memory_hard` |
| `hysteresis_samples` | int | `0`（自动 3） | 连续 N 次采样超阈值才触发/恢复（防抖动） |

> 平台说明：cgroup（容器）或 /proc（Linux 裸机）可读时水位熔断生效；darwin/windows 无法读内存 → 水位禁用、仅并发准入生效。指标端点 `GET /metrics` 输出 in-flight / 上限 / 软硬模式。

---

## 13. 密钥管理约定

- 仓库只发布 `config.example.yaml`；真实 `config.yaml` 被 gitignore，绝不提交。
- 密钥优先走环境变量：`upstreams[].api_key_env` / `secret_key_env`。
- 新增模型需同步改两处：`auth.keys[].model_whitelist` + `upstreams[].models`。
- 新增上游需改三处：`upstreams` 新增条目 → 加入某条 `routers[].upstreams` → 对应 vKey 的 `model_whitelist` 加模型。

---

## 14. 静态审计（Validate）检查项

启动即校验，失败直接拒绝启动：

- `server.port` 在 1-65535；
- `auth.keys` 至少一把 vKey；
- upstream 名称非空且唯一；`base_url` 必填；`protocol` ∈ {openai, baidu_wenxin}；`token_exchange` 仅限 `baidu_wenxin`；
- router 路径以 `/` 开头且唯一；策略合法；引用的 upstream 存在；
- 插件名唯一、`order` 不冲突、`fail_strategy` 合法；
- transport 超时为正数。
