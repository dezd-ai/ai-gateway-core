package kernel

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/DezDing/ai-gateway-core/config"
)

// runtimeSnapshot 是网关运行时的不可变快照：配置 + 插件链 + 路由表 + 反向代理。
// 热加载时整体重建并原子交换，读侧无锁共享旧快照。
type runtimeSnapshot struct {
	cfg      *config.Config
	pipeline *Pipeline
	router   *Router
	proxy    http.Handler
}

// Gateway 是网关内核顶层编排器，用全局 RWMutex 保护运行时快照。
type Gateway struct {
	mu          sync.RWMutex
	rt          *runtimeSnapshot
	configPath  string
	runtimes    map[string]*NodeRuntime // 跨热加载持久化节点状态（断路器/EMA/连接计数）
	storage     *EdgeStorage
	ratelimiter *RateLimiter  // vKey 级 QPS 限流（跨热加载保留）
	quota       *QuotaTracker // vKey 级每日 token 配额（跨热加载 + 落盘保留）
	memguard    *MemGuard     // 内存防线层 3+4：并发准入 + 水位熔断

	stopCh  chan struct{}
	lastMod time.Time
}

func NewGateway(cfgPath string) (*Gateway, error) {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, err
	}
	g, err := NewGatewayFromConfig(cfg, cfgPath)
	if err != nil {
		return nil, err
	}
	if st, err := os.Stat(cfgPath); err == nil {
		g.lastMod = st.ModTime()
	}
	return g, nil
}

// NewGatewayFromConfig 从内存配置构建网关，供 Doctor 干跑与 Bench 压测复用。
func NewGatewayFromConfig(cfg *config.Config, configPath string) (*Gateway, error) {
	g := &Gateway{
		configPath: configPath,
		runtimes:   map[string]*NodeRuntime{},
		stopCh:     make(chan struct{}),
	}
	if err := g.rebuild(cfg); err != nil {
		return nil, err
	}
	g.storage = NewEdgeStorage(cfg.EdgeStorage, ResolveLogPath(cfg.Log))
	g.ratelimiter = NewRateLimiter()
	quotaPath := filepath.Join(filepath.Dir(ResolveLogPath(cfg.Log)), "quota.json")
	g.quota = NewQuotaTracker(quotaPath)
	g.memguard = NewMemGuard(cfg.Capacity)
	g.memguard.Start()
	return g, nil
}

// rebuild 依据配置整体重建插件链、路由表与反向代理，并在写锁内交换快照。
func (g *Gateway) rebuild(cfg *config.Config) error {
	pipeline, err := BuildPipeline(cfg.Plugins)
	if err != nil {
		return err
	}
	router, err := NewRouter(cfg, g.runtimes, func(u *config.Upstream) *http.Transport {
		return NewTransport(cfg.Transport)
	})
	if err != nil {
		return err
	}
	proxy := NewReverseProxy(pipeline)

	g.mu.Lock()
	g.rt = &runtimeSnapshot{cfg: cfg, pipeline: pipeline, router: router, proxy: proxy}
	g.mu.Unlock()
	return nil
}

func (g *Gateway) snapshot() *runtimeSnapshot {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.rt
}

func (g *Gateway) Config() *config.Config { return g.snapshot().cfg }
func (g *Gateway) Router() *Router        { return g.snapshot().router }
func (g *Gateway) Storage() *EdgeStorage  { return g.storage }

// ServeHTTP 是网关统一入口：生成 TraceID → PreRequest 插件链 → 反向代理（Director 路由 + ModifyResponse 审计）。
func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 层 5：指标端点（绕过准入与鉴权）
	if r.URL.Path == "/metrics" {
		g.writeMetrics(w)
		return
	}

	rt := g.snapshot()
	ctx := NewPluginContext()
	ctx.Set(CtxTraceID, NewTraceID())
	ctx.Set(CtxStartTime, time.Now())
	ctx.Set(CtxConfig, rt.cfg)
	ctx.Set(CtxRouter, rt.router)
	ctx.Set(CtxStorage, g.storage)
	ctx.Set(CtxRateLimiter, g.ratelimiter)
	ctx.Set(CtxQuota, g.quota)

	// 内存防线 层 3+4：并发准入 + 水位熔断（满则 429 快速失败，不排队）
	if reason, ok := g.memguard.TryAcquire(); !ok {
		g.writeAbort(w, ctx, http.StatusTooManyRequests,
			[]byte(fmt.Sprintf(`{"error":{"message":"gateway busy","type":"server_busy","code":%q}}`, reason)), nil)
		return
	}
	defer g.memguard.Release()

	// 请求体上限检查已移至 auth_vkey（agent 感知，见 docs/agent-mode.md）：
	// 网关入口无 key 身份，无法区分 agent 放大，故在鉴权后判定。

	if err := rt.pipeline.ExecutePreRequest(ctx, r); err != nil {
		g.writeAbort(w, ctx, http.StatusBadGateway, nil, err)
		return
	}
	if aborted, status, body := ctx.IsAborted(); aborted {
		g.writeAbort(w, ctx, status, body, nil)
		return
	}

	r = WithPluginContext(r, ctx)
	rt.proxy.ServeHTTP(w, r)
}

func (g *Gateway) writeAbort(w http.ResponseWriter, ctx *PluginContext, status int, body []byte, err error) {
	if body == nil {
		msg := "gateway aborted"
		if err != nil {
			msg = err.Error()
		}
		body = []byte(fmt.Sprintf(`{"error":{"message":%q,"type":"gateway_abort","code":"%d"}}`, msg, status))
	}
	// 被 PreRequest 拦截的请求也落一条审计（backlog #4），安全事件可统计可追溯
	EmitAbortAudit(ctx, status, abortReason(status, body, err))

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-AI-Gateway-Trace-ID", ctx.GetString(CtxTraceID))
	w.Header().Set("X-AI-Gateway-Executed-Plugins", strings.Join(ctx.ExecutedPlugins(), ","))
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// abortReason 从拦截上下文提炼拦截原因：插件错误优先，其次错误 JSON 的 code，最后退化为状态码。
func abortReason(status int, body []byte, err error) string {
	if err != nil {
		return "plugin_error: " + err.Error()
	}
	var parsed struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &parsed) == nil && parsed.Error.Code != "" {
		return parsed.Error.Code
	}
	return fmt.Sprintf("status_%d", status)
}

// HotReload 启动后台 Goroutine 监听 config.yaml 变动，写锁保护安全刷新，绝不中断活跃连接。
func (g *Gateway) HotReload(interval time.Duration) {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-g.stopCh:
				return
			case <-ticker.C:
				st, err := os.Stat(g.configPath)
				if err != nil {
					continue
				}
				if !st.ModTime().After(g.lastMod) {
					continue
				}
				g.lastMod = st.ModTime()
				if err := g.Reload(); err != nil {
					fmt.Printf("[WARN] 配置热加载失败，保持旧快照: %v\n", err)
				} else {
					fmt.Println("[INFO] 配置热加载成功，已无中断刷新内存快照")
				}
			}
		}
	}()
}

// Reload 重新加载配置并重建运行时。
func (g *Gateway) Reload() error {
	cfg, err := config.Load(g.configPath)
	if err != nil {
		return err
	}
	return g.rebuild(cfg)
}

func (g *Gateway) Shutdown() {
	close(g.stopCh)
	g.CloseIdleConnections()
	if g.storage != nil {
		g.storage.Close()
	}
	if g.quota != nil {
		g.quota.Save()
	}
	if g.memguard != nil {
		g.memguard.Stop()
	}
}

// MemStats 暴露容量快照（层 5：启动日志 / 观测）。
func (g *Gateway) MemStats() map[string]any {
	if g.memguard == nil {
		return map[string]any{}
	}
	return g.memguard.Stats()
}

// writeMetrics 输出纯文本 Prometheus 风格指标（层 5）。
func (g *Gateway) writeMetrics(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	st := g.MemStats()
	fmt.Fprintf(w, "ai_gateway_max_inflight %v\n", st["max_inflight"])
	fmt.Fprintf(w, "ai_gateway_effective_inflight %v\n", st["effective_inflight"])
	fmt.Fprintf(w, "ai_gateway_in_flight %v\n", st["in_flight"])
	soft, hard := 0, 0
	if b, ok := st["soft_mode"].(bool); ok && b {
		soft = 1
	}
	if b, ok := st["hard_mode"].(bool); ok && b {
		hard = 1
	}
	fmt.Fprintf(w, "ai_gateway_soft_mode %d\n", soft)
	fmt.Fprintf(w, "ai_gateway_hard_mode %d\n", hard)
	fmt.Fprintf(w, "ai_gateway_soft_bytes %v\n", st["soft_bytes"])
	fmt.Fprintf(w, "ai_gateway_hard_bytes %v\n", st["hard_bytes"])
}

// CloseIdleConnections 关闭所有上游节点连接池中的空闲连接，释放底层 Goroutine。
func (g *Gateway) CloseIdleConnections() {
	g.mu.RLock()
	rt := g.rt
	g.mu.RUnlock()
	if rt == nil {
		return
	}
	for _, n := range rt.router.Nodes() {
		n.Transport.CloseIdleConnections()
	}
}
