# 性能基线（Benchmark）

> 本文档记录 `bench` 子命令在本地环境的实测数据与复现方法。
> 数据用于验证两个核心设计目标：**Buffer Pool 零拷贝（内存膨胀可控）** 与 **连接无泄露（Goroutine 回收干净）**。

---

## 1. 复现方法

```bash
# 构建
go build -o bin/ai-gateway-core .

# 基准压测：50 Workers × 5s（内存 Mock 上游，无需真实密钥）
./bin/ai-gateway-core bench -duration 5s -workers 50

# 高并发压力点：200 Workers × 5s
./bin/ai-gateway-core bench -duration 5s -workers 200
```

bench 引擎行为：

- 上游重定向到**内存 Mock 流式服务**（6 个 SSE chunk、每 chunk 2ms 间隔、含官方 `cached_tokens` usage），隔离真实服务商噪声；
- 关闭审计 stdout 回显与边缘落盘，聚焦代理 + 流式路径；
- 用例比 4:1：每 5 个请求 1 个违规用例（李四 + 敏感词 → 403）；
- 100ms 周期采样 `runtime.ReadMemStats` 与 Goroutine 数。

## 2. 测试环境

| 项 | 值 |
|---|---|
| 机器 | macOS（Apple Silicon） |
| Go | 1.23.11 |
| 内核 | 默认 transport 配置（建连 5s / TTFT 10s / 空闲读 30s） |
| Mock 上游 | 单实例内存 SSE 服务，每 chunk 2ms 间隔 |

## 3. 基线结果（50 Workers × 5s）

```
  总吞吐量 (QPS)        : 4944.20 req/s
  总请求数              : 24721 (5.0s)
  成功率 (200)          : 80.13% (19810)
  403 拦截率            : 19.87% (4911)     ← 与 4:1 用例比吻合
  失败请求数            : 0

  全链路时延分布:
    P50                 : 12.462 ms
    P90                 : 12.825 ms
    P99                 : 13.464 ms

  内存膨胀（验证 Buffer Pool 零拷贝）:
    基线 Alloc          : 0.41 MB
    峰值 Alloc          : 19.76 MB
    结束 Alloc          : 16.25 MB
    膨胀增量            : +19.35 MB

  连接回收残留（验证无网络泄露）:
    基线 Goroutines     : 6
    峰值 Goroutines     : 454
    结束 Goroutines     : 2
    残留增量            : -4              ← 无 Goroutine 泄露
```

**解读**：

- P50 ≈ 12.5ms 与 Mock 上游单流物理耗时（6 chunk × 2ms + 传输）吻合，说明网关本身引入的开销占比很小；
- 20% 的 403 来自违规用例（敏感词熔断），为设计内行为；
- 内存峰值约 19.8MB（50 并发流），相对 Go 运行时基线增量有限，验证 32KB 池化切片复用的有效性；
- 压测结束后 Goroutine 回落至基线以下（-4），连接/Goroutine 回收干净，无泄露。

## 4. 高并发压力点（200 Workers × 5s）

```
  总吞吐量 (QPS)        : 14572.80 req/s
  总请求数              : 72864 (5.0s)
  成功率 (200)          : 27.89% (20325)
  403 拦截率            : 19.67% (14333)
  失败请求数            : 38206            ← 出现超时/上游失败（饱和）

  全链路时延分布:
    P50                 : 4.540 ms
    P90                 : 29.940 ms
    P99                 : 117.872 ms

  内存膨胀:
    基线 Alloc          : 0.41 MB
    峰值 Alloc          : 54.04 MB
    结束 Alloc          : 10.07 MB
    膨胀增量            : +53.63 MB

  连接回收残留:
    基线 Goroutines     : 6
    峰值 Goroutines     : 1952
    结束 Goroutines     : 2
    残留增量            : -4
```

**解读**：200 并发超过单实例 Mock 上游的服务能力（每 chunk 串行 2ms sleep），上游响应变慢触发 TTFT/读超时，断路器随即开闸（默认阈值 5、开启 30s），期间大量请求快速失败。**成功率在该压点下随断路器开闸时机大幅波动**（同环境多次运行实测 12.9%–45.9%），因此本压点只能说明「饱和下的行为特征」，不能作为稳定指标。网关侧行为依然健康：内存峰值约 54MB、压测结束后 Goroutine 完全回收（无泄露）。要测网关自身上限，应使用无 sleep 或分布式的上游压测。

## 5. 注意事项

1. **bench 用例与仓库配置已对齐**：`bench` 的正常用例模型为 `deepseek-v4-flash`（与 `config.example.yaml` 的 `model_whitelist` / `upstreams[].models` 一致），违规用例为 `qwen-turbo` + 敏感词。**直接复制示例配置即可复现本基线，无需任何临时配置**。
2. 数值随机器/负载波动，本文档数据仅作量级参考，请在目标硬件上重新跑基准。
3. `bench` 内部会把所有上游重定向到内存 Mock 服务并关闭落盘，不产生真实计费与日志副作用。

## 6. 与文档的关联

- 设计目标：零拷贝/背压/超时/熔断详见 [architecture.md](./architecture.md)。
- 配置项：`transport` / `edge_storage` 详见 [config-reference.md](./config-reference.md)。
