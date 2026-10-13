# AI-Gateway-Core 常用命令
# 零外部依赖，仅需 Go ≥ 1.23

BINARY   := bin/ai-gateway-core
GO       ?= go
VERSION  ?= v0.1.0
LDFLAGS  := -s -w -X main.version=$(VERSION)

.PHONY: build vet test check bench serve mock docker fmt tidy clean

## build: 编译主网关二进制
build:
	$(GO) build -trimpath -ldflags="$(LDFLAGS)" -o $(BINARY) .

## vet: 静态检查（主模块 + obs）
vet:
	$(GO) vet ./...
	cd obs && $(GO) vet ./...

## test: 单元测试
test:
	$(GO) test ./...

## check: 配置审计 + 上游探测 + 干跑（需 config.yaml）
check: build
	./$(BINARY) check

## bench: 高并发压测（默认 5s × 50 workers，内存 Mock 上游）
bench: build
	./$(BINARY) bench -duration 5s -workers 50

## serve: 真实模式启动（需 config.yaml 与真实密钥）
serve: build
	./$(BINARY) serve

## mock: Mock 闭环模式启动（无需真实密钥）
mock: build
	./$(BINARY) serve -mock

## docker: 构建镜像
docker:
	docker build -t ai-gateway-core:$(VERSION) .

## fmt: 格式化
fmt:
	$(GO) fmt ./...
	cd obs && $(GO) fmt ./...

## tidy: 整理依赖
tidy:
	$(GO) mod tidy

## clean: 清理构建产物
clean:
	rm -rf bin
