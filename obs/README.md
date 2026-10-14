# AI-Gateway-Obs 观测台

[English](README.en.md) | **中文**

AI-Gateway-Core 的**只读观测工具**（本仓库 `obs/` 子目录），用本地网页查看网关的审计日志、调用链与成本。

- **不是管理端**：不做配置、不控制网关——纯只读观察者（网关本身无 Web 管理端，配置只走 `config.yaml`）。
- **零改动 ai-gateway-core**：只读它的 `logs/uba.json` 和 `config.yaml`。
- **零外部依赖**：纯 Go 标准库，前端单文件 HTML（无任何 CDN）。
- **纯观察者**：不写网关、不写日志、不反向控制。

---

## 功能

| 模块 | 说明 |
|---|---|
| 汇总卡 | 总请求数 / 总 Token / 总成本 / 缓存命中率 / 平均时延 |
| 成本分布图 | 按用户（或模型/上游）的成本柱状图 |
| 审计表格 | 每条请求的时间/用户/模型/上游/Token/成本/计费精度/安全/**状态**/时延，可按用户/模型/上游筛选 |
| **拦截标记** | 被网关拦截的请求（401/403/429/413）显示红色「拦截」标签 + 原因码 |
| **Fallback 标记** | 发生失败转移的请求显示紫色 `↪ 上游A→上游B` 标签（明细见调用链详情） |
| **Agent 标记** | `agent_mode: true` 的 key 请求在用户列显示蓝色 `agent` 标签（成本归属） |
| **调用链** | 点击行或「调用链」按钮，弹出垂直时间线：按插件执行顺序展示每步「做了什么 + 产出了什么数据」 |
| 自动刷新 | 每 3 秒重读日志 |

> 消费的审计字段见 [../docs/audit-log.md](../docs/audit-log.md)（含 `agent_mode` / `fallback_chain` / `http_status` / `abort_reason` 等）。

---

## 快速开始

```bash
cd obs
go build -o bin/ai-gateway-obs .   # 或 go run .
./bin/ai-gateway-obs
# 浏览器打开 http://localhost:9090
```

**先看效果（无需运行网关）**：仓库内附示例审计数据 `examples/uba.json`，直接指向它即可看到完整看板（含正常/拦截/fallback/agent 各类记录）：

```bash
./bin/ai-gateway-obs -log ../examples/uba.json
```

默认读取（相对路径，需在 `obs/` 目录下运行）：

- 审计日志：`../logs/uba.json`
- 网关配置：`../config.yaml`（取各上游 `cost_per_1k` 算成本）；**该文件不存在时自动回退 `../config.example.yaml`**，保证 clone 后即可看到成本列。

可用参数覆盖：

```bash
./bin/ai-gateway-obs -listen :9090 -log /path/to/uba.json -config /path/to/config.yaml
```

---

## 目录结构

```
ai-gateway-obs/
├── go.mod            # module github.com/dezd-ai/ai-gateway-core/obs（独立模块）
├── main.go           # HTTP 服务 + /api/* 接口 + go:embed 网页
├── store.go          # uba.json 读取/过滤/聚合（RWMutex + 2s 周期刷新）
├── cost.go           # 从 config.yaml 提取各上游 cost_per_1k
└── web/index.html    # 单文件看板（原生 HTML/CSS/JS）
```

---

## API

| 接口 | 说明 |
|---|---|
| `GET /` | 看板页面 |
| `GET /api/records?limit=&username=&model=&upstream=` | 审计记录（最新在前，含 `cost`、`rate_known`） |
| `GET /api/stats` | 汇总 + 按用户/模型/上游分组 |
| `GET /api/trace?id=<trace_id>` | 单条调用链详情 |

---

## 成本口径

`cost = total_tokens / 1000 × cost_per_1k`，费率从 `config.yaml` 的 `upstreams[].cost_per_1k` 读取。未配置费率的上游，`rate_known=false`，前端显示 `-`。

---

## 已知限制

1. **上游失败与正常成功在「正常记录」里仍不易区分**：拦截/拒绝类记录已带 `http_status`/`success`/`abort_reason`，但成功的代理请求（上游可能返回 5xx）尚未记录上游状态码——完全治本需网关侧给 auditor 补上游状态字段（见 ai-gateway-core README 待办 1）。
2. ~~**403 拦截的请求不在此日志**~~ ✅ 已修复：被 PreRequest 拦截的 401/403/502 现也落 `uba.json`（含 `http_status`/`abort_reason`），看板以「拦截」红色标记展示。
3. **混入 Mock/失败历史记录**：日志里保留着开发期的 Mock 测试与失败记录，想只看真实计费请清空 `ai-gateway-core/logs/uba.json`。
