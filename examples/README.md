# 示例文件（Examples）

| 文件 | 用途 |
|---|---|
| `uba.json` | 示例审计数据（12 条：正常 / 拦截 / failover / agent），供观测台（obs）直接展示看板，无需运行网关 |
| `chat-request.json` | 流式对话请求体示例（`model` 使用别名 `chat`），终端演示与 curl 复现用 |

## 用法

```bash
# 观测台（无需网关）
cd obs && go build -o bin/ai-gateway-obs . && ./bin/ai-gateway-obs -log ../examples/uba.json

# 流式对话复现（先 serve -mock 启动网关）
curl -sN -H "Authorization: Bearer sk-zhangsan-dev-0001" \
  -H "Content-Type: application/json" \
  --data @examples/chat-request.json \
  http://127.0.0.1:8080/v1/chat/completions
```
