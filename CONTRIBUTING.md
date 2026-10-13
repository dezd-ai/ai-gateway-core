# 贡献指南

感谢你对 AI-Gateway-Core 的关注。这是一个纯 Go 标准库（零外部依赖）打造的 AI 网关内核。

## 快速开始

```bash
git clone <repo-url>
cd ai-gateway-core
cp config.example.yaml config.yaml   # 并设置上游密钥（环境变量或 config.yaml）
go build -o bin/ai-gateway-core .
./bin/ai-gateway-core check           # 诊断
./bin/ai-gateway-core serve -mock     # Mock 闭环运行
```

## 提交前请确保

```bash
go build ./...
go vet ./...
go test ./...
cd obs && go build ./... && go vet ./...
```

## 项目结构

- `kernel/` —— 内核（插件接口、管线、路由、断路器、反向代理、边缘存储），**不依赖** `plugins/`。
- `plugins/` —— 标准拦截插件（auth_vkey / injector / security_guard / dynamic_router / auditor）。
- `config/` —— 零依赖 YAML 解析与配置加载。
- `doctor/`、`bench/` —— 诊断与压测子命令。
- `obs/` —— 只读观测台（独立 go.mod）。

## 如何扩展

- **加插件**：在 `plugins/` 新建文件，实现 `kernel.Plugin` 接口，`init()` 里 `kernel.RegisterPlugin("名字", 工厂)`，再在 `config.example.yaml` 的 `plugins[]` 挂载。
- **加路由策略**：在 `kernel/router.go` 实现 `RoutingStrategy` 接口，注册进 `strategyRegistry`。
- **加非标协议适配**：参考 `plugins/dynamic_router.go` 的百度 `token_exchange`，扩展节点凭证替换逻辑。

## 约定

- 保持零外部依赖：新增功能优先使用 Go 标准库。
- 并发安全：共享状态用 `sync.RWMutex` / `atomic`，热加载只交换不可变快照。
- 提交信息建议使用清晰的中文或英文描述改动意图。

## 安全

- **禁止**在代码或提交的 `config.yaml` 中出现真实密钥，统一走 `config.example.yaml` + 环境变量（`*_env` 字段）。
- 若曾误提交密钥，请立即吊销并重新生成，不要只删除文件。
