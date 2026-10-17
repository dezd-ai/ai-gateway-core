# syntax=docker/dockerfile:1
# AI-Gateway-Core 多阶段构建：纯 Go 标准库 → 静态二进制 → 精简运行时镜像
FROM golang:1.23-alpine AS builder

WORKDIR /src

# 零外部依赖：仅需 go.mod，无 go.sum
COPY go.mod ./
COPY config/ ./config/
COPY kernel/ ./kernel/
COPY plugins/ ./plugins/
COPY doctor/ ./doctor/
COPY bench/ ./bench/
COPY mockup/ ./mockup/
# main 包包含 main.go + keygen.go（两者缺一不可）
COPY main.go keygen.go ./

# 静态链接（CGO_ENABLED=0）+ 裁剪符号，产物为单文件二进制
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ai-gateway-core .

FROM alpine:3.20

# CA 证书（TLS 访问上游必需）+ 时区数据
RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app
COPY --from=builder /out/ai-gateway-core /usr/local/bin/ai-gateway-core
# 内置示例配置：未挂载 config.yaml 时 serve 自动回退到它并打印 WARN，保证开箱可跑
COPY config.example.yaml /app/config.example.yaml

# 审计日志目录（建议通过卷挂载持久化）
RUN mkdir -p /app/logs

EXPOSE 8080

ENTRYPOINT ["ai-gateway-core"]
CMD ["serve"]
