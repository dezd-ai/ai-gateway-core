# 截图与演示素材（Screenshots）

存放 README 与文档引用的图片（观测台看板、CLI 输出等）。

## 当前素材

| 文件名 | 内容 | 引用位置 |
|---|---|---|
| `dashboard.png` | 观测台看板总览（汇总卡 + 成本图 + 审计表，含拦截 / failover / agent 标记） | `README.md`、`README.en.md` 的「演示」小节 |
| `dashboard-chain.png` | 调用链详情弹窗（插件执行时间线） | 同上 |

## 采集方式（无需运行网关，用仓库内示例数据）

```bash
cd obs && go build -o bin/ai-gateway-obs .
./bin/ai-gateway-obs -log ../examples/uba.json
# 浏览器打开 http://localhost:9090
```

示例数据包含正常请求、4 类拦截（红标）、failover（紫标）、agent 请求（蓝标），看板效果最完整。

**整页截图方法**（页面比视口长时必需）：

| 浏览器 | 操作 |
|---|---|
| Chrome / Edge | `⌥⌘I` 开 DevTools → `⌘⇧P` → 输入 `Capture full size screenshot` → 回车 |
| Firefox | 页面右键 → Take Screenshot → Save full page |
| Safari | `⌘-` 缩放到 67% 后 `⌘⇧4`（按空格）点击窗口 |

## 可选素材

| 文件名 | 内容 | 采集方式 |
|---|---|---|
| `bench.png` | `bench` 压测报告（终端文本） | 终端跑 `./bin/ai-gateway-core bench -duration 5s -workers 50` 后截窗口 |

> README 的「性能（实测）」小节已用**文本**呈现压测数据（可复制、可搜索）；
> 终端截图更适合社交媒体传播，非必需。

## README 引用方式

```markdown
![观测台看板](docs/images/dashboard.png)
```
