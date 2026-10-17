# 部署指南（Deployment）

> 网关是**单个静态二进制**（纯 Go 标准库，零外部依赖），部署形态极其简单：一个进程 + 一份 `config.yaml`。
> 本文档覆盖本地运行、Docker 部署、配置挂载与生产建议。

---

## 1. 本地运行

### 1.1 构建

```bash
go build -o bin/ai-gateway-core .
```

环境要求：Go ≥ 1.23（macOS 26 上 Go 1.22 二进制会 dyld `missing LC_UUID` 崩溃；`go.mod` 已声明 `toolchain go1.23.11` 自动选择）。

### 1.2 配置准备

```bash
cp config.example.yaml config.yaml
export DEEPSEEK_API_KEY=sk-xxxx      # 按需设置上游密钥
export QWEN_API_KEY=sk-xxxx
export BAIDU_API_KEY=xxxx
export BAIDU_SECRET_KEY=xxxx
```

### 1.3 启动

```bash
# 真实模式（需配置真实上游密钥）
./bin/ai-gateway-core serve

# Mock 闭环模式（上游重定向到内置内存 Mock，无需真实密钥）
./bin/ai-gateway-core serve -mock

# 指定配置文件
./bin/ai-gateway-core serve -config /path/to/config.yaml
```

### 1.4 验证

```bash
./bin/ai-gateway-core check    # Doctor 诊断：配置审计 + 上游探测 + 干跑 + 拓扑
```

---

## 2. Docker 部署

仓库提供多阶段构建 `Dockerfile`（静态链接、无 CGO，产物镜像约 16MB）：

```bash
# 首次使用：准备配置（不复制则容器回退到示例配置并打印 WARN）
cp config.example.yaml config.yaml

docker build -t ai-gateway-core .
docker run -d --name ai-gateway \
  -p 8080:8080 \
  -v "$(pwd)/config.yaml:/app/config.yaml:ro" \
  -v "$(pwd)/logs:/app/logs" \
  -e DEEPSEEK_API_KEY=sk-xxxx \
  -e QWEN_API_KEY=sk-xxxx \
  -e BAIDU_API_KEY=xxxx \
  -e BAIDU_SECRET_KEY=xxxx \
  ai-gateway-core
```

无密钥快速试跑（内置 Mock 上游，仅验证链路）：

```bash
docker run --rm -p 8080:8080 ai-gateway-core serve -mock
# 另一个终端：curl -H "Authorization: Bearer sk-zhangsan-dev-0001" \
#   -H "Content-Type: application/json" \
#   -d '{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}],"stream":true}' \
#   http://localhost:8080/v1/chat/completions
```

docker-compose 方式见仓库根目录 `docker-compose.yml`（已内置 healthcheck，探针为公开端点 `/metrics`；网关不提供 `/healthz`）：

```bash
docker compose up -d
```

> 审计日志目录 `logs/` 通过卷挂载持久化，防止容器重建丢失 `uba.json`。

---

## 3. 配置挂载与热加载

- 配置以只读卷挂载进容器；网关内置**热加载**（默认 2s 轮询文件 mtime），修改宿主机上的 `config.yaml` 后无需重启容器，网关会在下一个周期无中断刷新内存快照。
- 热加载失败（如配置非法）时**保持旧快照继续服务**，并在控制台打印 WARN，不会中断活跃连接。

---

## 4. 生产部署建议

### 4.1 密钥管理

- 严禁把真实密钥写进 `config.yaml` 提交仓库；统一使用 `api_key_env` / `secret_key_env` 环境变量注入（K8s Secret、Vault 等）。
- `config.yaml` 已加入 `.gitignore`，仓库仅发布 `config.example.yaml`。

### 4.2 进程管理

| 场景 | 建议 |
|---|---|
| 单机 | systemd unit（`Restart=on-failure`）+ 日志重定向 |
| 容器 | 镜像以 `serve` 为默认命令；配置 `restart: unless-stopped` |
| 高可用 | 多实例 + 上层负载均衡（注意：路由/断路器状态为进程内存态，多实例间不共享） |

### 4.3 观测

- 审计日志（`logs/uba.json`）用卷持久化，可配合 `obs/` 观测台或自建采集管道消费；
- `edge_storage.report_url` 可配置异步上报中心化 UBA 平台；上报失败有 JitterBackoff 重试（6 次），队列满时丢弃远端上报但本地落盘保真。

### 4.4 资源与容量

- 单实例内存占用低（基线压测见 [benchmark.md](./benchmark.md)：50 并发流峰值约 20MB）；
- 连接池与超时参数在 `transport` 区块调整；
- 熔断参数按上游稳定性在 `upstreams[].circuit` 单独配置。

### 4.5 安全加固

- 网关前置反向代理（Nginx/ALB）做 TLS 终止与访问控制；
- 上游密钥最小权限原则：每个上游使用独立 Key，便于吊销与计费归属；
- 审计日志含用户请求元数据，注意存储与传输的隐私合规（见 `SECURITY.md`）。

---

## 5. 目录清单（部署相关文件）

```
Dockerfile              # 多阶段构建（静态二进制）
docker-compose.yml      # 一键编排（含 logs 卷与密钥环境变量示例）
.dockerignore           # 排除本地配置/日志/二进制进镜像
docs/deployment.md      # 本文档
```
