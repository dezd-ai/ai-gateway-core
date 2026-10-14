# 截图与演示素材（Screenshots）

存放 README 与文档引用的图片（观测台看板、CLI 输出等）。

## 建议添加的截图

| 文件名 | 内容 | 采集方式 |
|---|---|---|
| `dashboard.png` | 观测台看板总览（汇总卡 + 成本图 + 审计表） | 见下方 |
| `dashboard-chain.png` | 调用链详情弹窗（插件执行时间线） | 点击看板任意行的「调用链」 |
| `bench.png` | `bench` 压测报告（QPS / P99 / 内存 / 连接残留） | `./bin/ai-gateway-core bench -duration 5s -workers 50` |
| `keygen.png` | `keygen` 生成的 key 片段 | `./bin/ai-gateway-core keygen -username alice -department engineering` |

## 采集观测台截图（无需运行网关，用仓库内示例数据）

```bash
cd obs && go build -o bin/ai-gateway-obs .
./bin/ai-gateway-obs -log ../examples/uba.json
# 浏览器打开 http://localhost:9090 → 截图
```

示例数据包含正常请求、4 类拦截（红标）、failover（紫标）、agent 请求（蓝标），
看板效果最完整——适合作为项目首屏展示图。

## README 引用方式

```markdown
![观测台看板](docs/images/dashboard.png)
```

（中文 `README.md` 与英文 `README.en.md` 的预留位置已用注释标出，放入图片后取消注释即可。）
