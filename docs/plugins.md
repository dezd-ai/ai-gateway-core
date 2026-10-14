# 插件说明（plugins/）

> 本文档说明如何理解、配置与扩展 AI-Gateway-Core 的插件体系。
> 插件代码位于 `plugins/`，接口定义位于 `kernel/plugin.go`。

---

## 1. 插件机制总览

### 1.1 标准接口

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

- `Init`：网关构建时调用一次，传入 `config.yaml` 中 `plugins[].config` 的 JSON 序列化结果。
- `PreRequest`：请求转发上游**之前**执行，可鉴权、改写 Body、注入数据，或调用 `ctx.Abort(...)` 直接拦截。
- `ModifyResponse`：上游响应返回后执行，可包装 `res.Body` 实现流式拦截。
- 继承 `kernel.BasePlugin`（全部空实现）可免写无关方法，未来接口升级也不破坏既有插件。

### 1.2 可选扩展接口

```go
// DirectorHook：实现后会在反向代理 Director 阶段被调用，承载路由级分流。
type DirectorHook interface {
    Director(ctx *PluginContext, req *http.Request) error
}
```

### 1.3 三阶段执行

```
Gateway.ServeHTTP
  ├─ Pipeline.ExecutePreRequest    （转发前：auth_vkey → prompt_injector → security_guard）
  ├─ ReverseProxy.Director
  │    └─ Pipeline.ExecuteDirector （路由级：dynamic_router → Router.Route）
  └─ ReverseProxy.ModifyResponse
       └─ Pipeline.ExecuteModifyResponse（响应级：auditor）
```

### 1.4 执行顺序与容错

- 顺序：按 `config.yaml` 中 `plugins[].order` 升序（`BuildPipeline` 内 `sort.SliceStable`）。
- 容错：`fail_strategy: abort`（插件出错 → 请求熔断，PreRequest 返回 502）/ `bypass`（跳过故障插件继续转发）。
- 拦截：任一 PreRequest 插件调用 `ctx.Abort(status, body)` 后，管线中断，网关直接返回（不再代理上游）。

### 1.5 插件注册方式

插件包在 `init()` 中自注册，`main.go` 空导入触发：

```go
// plugins/xxx.go
func init() {
    kernel.RegisterPlugin("插件注册名", func() kernel.Plugin { return &XxxPlugin{} })
}
```

随后在 `config.yaml` 的 `plugins[]` 挂载即可（含 order / fail_strategy / config）。

### 1.6 动态挂载（Wasm 预留槽位）

```go
// 未来 Wasm 插件市场：不修改内核、不重启网关，动态注入拦截链
pipeline.RegisterDynamicPlugin(plg) // 以 FailBypass 追加到链尾
```

---

## 2. 内置插件速查表

| 插件 | 注册名 | 阶段 | 核心配置 | 写入的 Context 键 | 可拦截 |
|---|---|---|---|---|---|
| vKey 鉴权 | `auth_vkey` | PreRequest | `auth.keys[]`、`auth.hal_enabled` | `gw_auth_key` | 401 |
| 提示词注入 | `prompt_injector` | PreRequest | `injector.templates` | `gw_model`、`gw_original_prompt`、`gw_final_prompt`、`gw_prompt_injected`、`gw_prompt_tokens_est` | 403（模型白名单） |
| 安全防护 | `security_guard` | PreRequest | `security.sensitive_words`、`security.injection_patterns`、`security.block_response` | `gw_security_check` | 403（伪装标准错误） |
| 动态路由 | `dynamic_router` | Director | `routers[]`、`upstreams[]` | `gw_upstream_name`（选中节点挂入 req.Context） | 400/404/503 |
| 流式审计 | `auditor` | ModifyResponse | `transport.idle_read_timeout_sec`、`edge_storage` | 只读为主，不写 | — |

> Context 键均为内核保留命名空间（`gw_` 前缀，见 `kernel/context.go`），插件私有键请使用自己的前缀，避免冲突。

---

## 3. auth_vkey — vKey 鉴权

**职责**：提取 `Authorization: Bearer <vKey>`，在内存 O(1) Map 中等值比对鉴权，通过后将身份、部门、模型白名单与多维画像标签注入 Context，供下游插件使用。

**注册名**：`auth_vkey`　**阶段**：PreRequest　**默认 fail_strategy**：`abort`

**配置项**：

| 配置 | 类型 | 默认 | 说明 |
|---|---|---|---|
| `auth.hal_enabled` | bool | false | 是否启用硬件 HAL 指纹校验（TPM2.0/TEE） |
| `auth.keys[]` | 列表 | 必填 | vKey 表（见下） |

`auth.keys[]` 字段：`vkey`（明文密钥）、`username`、`department`（注入模板按此匹配）、`model_whitelist`（强约束白名单）、`tags`（domain_tag 策略匹配用）、`hardware_fingerprint`（HAL 指纹）。

**行为与副作用**：

- 无 Bearer 头 → Abort 401 `missing_api_key`。
- vKey 未命中 → Abort 401 `invalid_api_key`。
- `hal_enabled` 且指纹不一致 → Abort 401 `self_lockdown`（自我熔断）；Mac 无 TPM 设备时优雅跳过并告警。
- 通过后写入 `gw_auth_key`（`*config.AuthKey`）。

**安全注意**：vKey 为明文等值比对、无生命周期管理（生成/过期/轮换/吊销），规模化后应替换为 HMAC 无状态签发或 `keygen` 子命令。

---

## 4. prompt_injector — 提示词注入

**职责**：解析 OpenAI JSON Body，读取 `model`，按用户的 `department` 从 `injector.templates` 取企业准则模板，无感注入首个 user 消息的头/尾，并用 `io.NopCloser` 重写 Body。

**注册名**：`prompt_injector`　**阶段**：PreRequest　**默认 fail_strategy**：`bypass`

**配置项**：`injector.templates.<部门>` = `{head, tail}`。

**行为与副作用**：

- 仅处理 `Content-Type: application/json`；非 JSON（小模型/工业算法路径）自动跳过并标记 `gw_prompt_injected=false`。
- 写入 `gw_model`、`gw_original_prompt`、`gw_final_prompt`、`gw_prompt_injected`、`gw_prompt_tokens_est`。
- **模型白名单强约束**：`auth.keys[].model_whitelist` 非空且请求模型不在其中 → Abort 403 `model_not_allowed`。
- 注入后重写 `req.Body` 与 `req.ContentLength`。

**示例配置**：

```yaml
injector:
  templates:
    研发部:
      head: "你是企业内部研发助手，请遵守公司代码安全规范，输出严谨技术答案。"
      tail: ""
```

---

## 5. security_guard — 安全防护

**职责**：深度扫描最终 Prompt（优先 `gw_final_prompt`，回退 `gw_original_prompt`），命中敏感词字典（DLP）或防注入正则即熔断，返回**伪装成标准大模型的 403 错误 JSON**。

**注册名**：`security_guard`　**阶段**：PreRequest　**默认 fail_strategy**：`abort`

**配置项**：

| 配置 | 说明 |
|---|---|
| `security.sensitive_words[]` | 敏感词字典（大小写不敏感子串匹配） |
| `security.injection_patterns[]` | 防提示词注入正则（自动加 `(?i)`，忽略大小写） |
| `security.block_response` | 命中后返回的错误 JSON 体（`error.message/type/code`） |

**行为与副作用**：

- 命中敏感词 → `gw_security_check=BLOCKED_SENSITIVE`，Abort 403。
- 命中注入正则 → `gw_security_check=BLOCKED_INJECTION`，Abort 403。
- 通过 → `gw_security_check=PASSED`。
- Prompt 为空（非对话类请求）→ 直接 PASSED，不拦截。

---

## 6. dynamic_router — 动态路由（Director 阶段）

**职责**：在反向代理 Director 阶段调用 `kernel.Router.Route`，按「路径 → 断路器过滤 → 模型强约束 → 策略」挑选最佳上游节点，重写 `req.URL` / `req.Host`，并安全替换 vKey 为真实上游凭证（直连 Bearer 或百度换钥 `access_token`）。

**注册名**：`dynamic_router`　**阶段**：Director（实现 `DirectorHook`）　**默认 fail_strategy**：`abort`

**配置项**：`routers[]`（path/strategy/upstreams/default_model/domain_tags）+ `upstreams[]`（base_url/api_key/protocol/auth_mode/models/cost_per_1k/tags/circuit）。

**错误码语义**（由 `defaultErrorHandler` 映射）：

| 错误 | HTTP 状态 |
|---|---|
| `ErrNoRoute`（路径未匹配） | 404 |
| `NoModelSupportError`（模型无上游支持） | 400 |
| `ErrCircuitOpen`（熔断开启） | 503 |
| 其它代理错误 | 502 |

**凭证替换规则**：

- `auth_mode: direct` → 设置 `Authorization: Bearer <ResolveAPIKey()>`（支持 `api_key_env` 环境变量注入）。
- `auth_mode: token_exchange`（百度千帆）→ 向 `aip.baidubce.com/oauth/2.0/token` 换取临时 `access_token`，内存缓存至过期前 1 分钟，拼接 URL 参数并删除 Authorization 头。

---

## 7. auditor — 流式审计（ModifyResponse 阶段）

**职责**：包装上游 `res.Body`，非阻塞拦截流式（SSE）/非流式（JSON）响应，增量统计 Token，捕获官方计费并覆盖本地预估，流结束后异步输出审计日志。

**注册名**：`auditor`　**阶段**：ModifyResponse　**默认 fail_strategy**：`bypass`

**配置项**：`transport.idle_read_timeout_sec`（SSE 空闲读超时，超时强制关闭上游流）、`edge_storage`（落盘/上报）。

**关键行为**：

1. **绝不整读内存**：逐行解析 SSE，边转发边统计。
2. **官方计费覆盖**：捕获 `usage.prompt_tokens_details.cached_tokens`（DeepSeek/千问等国产模型），以官方数据覆盖本地预估，标记 `precision: official_cached`。
3. **异常流截断处理**：EOF 未见 `[DONE]` → 强制补齐错误 chunk + `[DONE]`，杜绝下游死锁；空闲超时同样处理。
4. **gateway_meta 注入**：`[DONE]` 前注入 `data: {"gateway_meta":{trace_id, executed_plugins, total_tokens, prompt_injected}}`。
5. **异步落盘**：独立 Goroutine 输出审计 JSON 至 stdout + `EdgeStorage`（uba.json 追加 + 异步上报）。

**审计记录字段**：见 [audit-log.md](./audit-log.md)。

---

## 8. 如何编写一个新插件（三步）

**第 1 步**：在 `plugins/` 新建文件，实现 `Plugin` 接口（继承 `BasePlugin`）：

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
    // 读配置：cfg, _ := ctx.GetValue(kernel.CtxConfig).(*config.Config)
    // 写数据：ctx.Set("my_plugin_xxx", value)
    // 拦截：  ctx.Abort(http.StatusForbidden, body)
    return nil
}
```

**第 2 步**：在 `config.yaml`（及 `config.example.yaml`）的 `plugins[]` 挂载：

```yaml
plugins:
  - name: my_plugin
    enabled: true
    order: 6
    fail_strategy: bypass
    config:
      my_option: value
```

**第 3 步**：`go build ./... && go vet ./... && go test ./...` 验证。

> 提示：`Init(configJSON string)` 接收的是 `plugins[].config` 的 JSON，用 `json.Unmarshal` 解析为插件私有结构体。

---

## 9. 插件开发约定

- 保持零外部依赖：优先使用 Go 标准库。
- 并发安全：插件读写 `PluginContext` 是线程安全的（内部 RWMutex），但插件自身的共享状态需自行加锁。
- 私有 Context 键使用插件前缀（如 `my_plugin_`），不要占用 `gw_` 命名空间。
- 大 body 处理使用 `kernel.ReadBody` / `kernel.GetBuffer` 池化读取，禁止 `io.ReadAll` 一次性大分配。
