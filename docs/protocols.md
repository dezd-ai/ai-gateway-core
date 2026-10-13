# 非标协议适配（Protocols）

> 当前内核内置两类上游协议：
> - `openai`：类 OpenAI 标准轨（DeepSeek、千问 Qwen 兼容模式、智谱 GLM 等），Bearer 直连；
> - `baidu_wenxin`：百度文心一言（自定义信创轨），无感令牌换取（token_exchange）。
>
> 本文档说明协议适配机制与扩展方法。

---

## 1. 协议选择

`upstreams[].protocol` 决定凭证与请求形态，`upstreams[].auth_mode` 决定凭证注入方式：

| protocol | auth_mode | 行为 |
|---|---|---|
| `openai` | `direct`（默认） | `Authorization: Bearer <api_key>` 直连 |
| `baidu_wenxin` | `token_exchange` | 向百度千帆换取临时 `access_token`，拼接 URL 参数 |

Validate 强约束：`token_exchange` 仅允许搭配 `baidu_wenxin` 协议。

---

## 2. 百度千帆换钥流程（token_exchange）

```
请求到达 dynamic_router
  └─ Node.Rewrite（auth_mode == token_exchange）
       ├─ ensureAccessToken()
       │    ├─ 内存缓存命中（未过期且距过期 > 1min）→ 直接返回
       │    └─ 缓存未命中：
       │         POST https://aip.baidubce.com/oauth/2.0/token
       │             ?grant_type=client_credentials
       │             &client_id=<api_key>
       │             &client_secret=<secret_key>
       │         → 解析 {access_token, expires_in}
       │         → 缓存至 expires_in - 1min
       ├─ 拼接 req.URL.Query().Set("access_token", token)
       └─ 删除 Authorization 头
```

- 客户端侧保持 100% 标准 OpenAI 形态调用，换钥对调用方完全无感。
- Token 内存缓存于 `NodeRuntime`（`tokenMu` 保护），跨热加载持久，过期前 1 分钟自动刷新。
- 换钥失败 → 代理错误（502 语义），不向客户端泄露密钥。

---

## 3. 如何新增一个非标协议

以「智谱 GLM4」或「任意自定义协议」为例，扩展点位于 `kernel/router.go` 的 `Node.Rewrite`（及 `ensureAccessToken` 同类方法）：

### 步骤 1：扩展协议枚举与校验

- 在 `config/` 的 `Upstream` 增加协议标识（沿用现有字符串约定，如 `protocol: xxx`）。
- 在 `config.Config.Validate` 的协议白名单中登记新协议。

### 步骤 2：在 `Node.Rewrite` 增加分支

```go
// kernel/router.go（示意）
func (n *Node) Rewrite(ctx *PluginContext, req *http.Request) error {
    // ... 公共的 URL/Host 重写 ...
    switch n.Upstream.AuthMode {
    case "token_exchange":
        // 已有百度流程
    case "my_custom":
        // 新协议：换取/计算凭证 → 注入 Header 或 URL 参数
    default: // direct
        req.Header.Set("Authorization", "Bearer "+u.ResolveAPIKey())
    }
    return nil
}
```

### 步骤 3：如有响应体差异，扩展 auditor 或新增适配插件

- 仅凭证/URL 差异 → 只改 `Rewrite`。
- 响应结构差异（非 OpenAI 形态）→ 需要额外的 ModifyResponse 适配插件做形态转换。

### 步骤 4：注册新上游并验证

```bash
# 1) config.yaml 增加 upstream（protocol 新值）
# 2) 加入 routers[].upstreams
# 3) 对应 vKey 的 model_whitelist 加模型
./bin/ai-gateway-core check   # Doctor 连通探测 + 干跑
```

---

## 4. 已内置适配对照

| 上游 | protocol | auth_mode | 凭证来源 | 备注 |
|---|---|---|---|---|
| DeepSeek | `openai` | `direct` | `DEEPSEEK_API_KEY` | 官方计费含 `cached_tokens` |
| 千问 Qwen | `openai` | `direct` | `QWEN_API_KEY` | 阿里云兼容模式端点 |
| 百度文心 | `baidu_wenxin` | `token_exchange` | `BAIDU_API_KEY` + `BAIDU_SECRET_KEY` | 唯一内置非标协议 |

> 新增协议时建议同步更新本表，并补充 `docs/config-reference.md` 的协议枚举说明。
