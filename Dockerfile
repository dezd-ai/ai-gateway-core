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
COPY main.go ./

# 静态链接 + 裁剪符号，产物约几 MB 的单文件二进制
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ai-gateway-core .

FROM alpine:3.20

# CA 证书（TLS 访问上游必需）+ 时区数据
RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app
COPY --from=builder /out/ai-gateway-core /usr/local/bin/ai-gateway-core

# 审计日志默认路径（Mac 自适应在容器内不生效，建议通过 log.path 或卷挂载指定）
RUN mkdir -p /app/logs

EXPOSE 8080

ENTRYPOINT ["ai-gateway-core"]
CMD ["serve"]
