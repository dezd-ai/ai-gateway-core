package kernel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dezd-ai/ai-gateway-core/config"
)

// 节点运行时状态，跨配置热加载持久保留（断路器/EMA/连接计数不因刷新归零）。
type NodeRuntime struct {
	connCount atomic.Int64

	mu         sync.RWMutex
	latencyEMA float64
	ttftEMA    float64

	breaker *CircuitBreaker

	tokenMu     sync.Mutex
	accessToken string
	tokenExpiry time.Time
}

func newNodeRuntime(cb config.CircuitBreakerConfig) *NodeRuntime {
	return &NodeRuntime{
		breaker: NewCircuitBreaker(cb.FailureThreshold, cb.OpenTimeoutSec, cb.HalfOpenMax),
	}
}

func (rt *NodeRuntime) IncConn() { rt.connCount.Add(1) }
func (rt *NodeRuntime) DecConn() { rt.connCount.Add(-1) }
func (rt *NodeRuntime) ConnCount() int64 {
	return rt.connCount.Load()
}

const emaAlpha = 0.3

func (rt *NodeRuntime) RecordLatency(d time.Duration) {
	rt.mu.Lock()
	if rt.latencyEMA == 0 {
		rt.latencyEMA = float64(d)
	} else {
		rt.latencyEMA = emaAlpha*float64(d) + (1-emaAlpha)*rt.latencyEMA
	}
	rt.mu.Unlock()
}

func (rt *NodeRuntime) RecordTTFT(d time.Duration) {
	rt.mu.Lock()
	if rt.ttftEMA == 0 {
		rt.ttftEMA = float64(d)
	} else {
		rt.ttftEMA = emaAlpha*float64(d) + (1-emaAlpha)*rt.ttftEMA
	}
	rt.mu.Unlock()
}

func (rt *NodeRuntime) LatencyEMA() time.Duration {
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	return time.Duration(rt.latencyEMA)
}

func (rt *NodeRuntime) TTFTEMA() time.Duration {
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	return time.Duration(rt.ttftEMA)
}

func (rt *NodeRuntime) Breaker() *CircuitBreaker { return rt.breaker }

// Node 绑定一个上游配置 + 其独立连接池 Transport + 持久运行时状态。
type Node struct {
	Upstream  *config.Upstream
	Runtime   *NodeRuntime
	Transport *http.Transport

	agentMu     sync.Mutex
	agentTransp *http.Transport // 懒克隆：agent 放大 TTFT 专用（独立连接池）
}

// transportForTTFT 返回适合本次请求的 Transport：agent 且放大 TTFT 时用懒克隆实例
// （ResponseHeaderTimeout 放大以等待推理长思考），否则共享 Transport（docs/agent-mode.md）。
func (n *Node) transportForTTFT(ttft time.Duration) *http.Transport {
	if ttft <= 0 {
		return n.Transport
	}
	n.agentMu.Lock()
	defer n.agentMu.Unlock()
	if n.agentTransp == nil {
		t := n.Transport.Clone()
		t.ResponseHeaderTimeout = ttft
		n.agentTransp = t
	}
	return n.agentTransp
}

func (n *Node) RoundTrip(req *http.Request) (*http.Response, error) {
	return n.roundTrip(req, n.Transport)
}

// RoundTripWithTTFT 以放大后的首字超时执行（agent 宽松模式）。
func (n *Node) RoundTripWithTTFT(req *http.Request, ttft time.Duration) (*http.Response, error) {
	return n.roundTrip(req, n.transportForTTFT(ttft))
}

func (n *Node) roundTrip(req *http.Request, transport *http.Transport) (*http.Response, error) {
	if !n.Runtime.breaker.Allow() {
		return nil, ErrCircuitOpen
	}
	n.Runtime.IncConn()
	start := time.Now()
	res, err := transport.RoundTrip(req)
	if err != nil {
		n.Runtime.DecConn()
		n.Runtime.breaker.RecordFailure()
		return nil, err
	}
	n.Runtime.RecordTTFT(time.Since(start))
	// 5xx 与 429 均计入失败（429 限流同样需要断路器保护，见 fallback-design.md 第 8 节）
	if res.StatusCode >= 500 || res.StatusCode == http.StatusTooManyRequests {
		n.Runtime.breaker.RecordFailure()
	} else {
		n.Runtime.breaker.RecordSuccess()
	}
	res.Body = &nodeBody{ReadCloser: res.Body, rt: n.Runtime, start: start}
	return res, nil
}

// nodeBody 在流关闭时回收连接计数并累计端到端时延 EMA。
type nodeBody struct {
	ReadCloser ioReadCloser
	rt         *NodeRuntime
	start      time.Time
	once       sync.Once
}

type ioReadCloser interface {
	Read(p []byte) (int, error)
	Close() error
}

func (b *nodeBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil {
		b.finish()
	}
	return n, err
}

func (b *nodeBody) Close() error {
	b.finish()
	return b.ReadCloser.Close()
}

func (b *nodeBody) finish() {
	b.once.Do(func() {
		b.rt.DecConn()
		b.rt.RecordLatency(time.Since(b.start))
	})
}

// Rewrite 重写 req.URL / req.Host 并安全替换 vKey 为真实上游凭证。
func (n *Node) Rewrite(ctx *PluginContext, req *http.Request) error {
	u := n.Upstream
	base, err := url.Parse(u.BaseURL)
	if err != nil {
		return fmt.Errorf("upstream %s base_url 非法: %w", u.Name, err)
	}
	req.URL.Scheme = base.Scheme
	req.URL.Host = base.Host
	req.Host = base.Host
	if u.EndpointPath != "" {
		req.URL.Path = u.EndpointPath
	}

	switch u.AuthMode {
	case "token_exchange":
		token, err := n.ensureAccessToken()
		if err != nil {
			return fmt.Errorf("upstream %s 令牌换取失败: %w", u.Name, err)
		}
		q := req.URL.Query()
		q.Set("access_token", token)
		req.URL.RawQuery = q.Encode()
		req.Header.Del("Authorization")
	default:
		req.Header.Set("Authorization", "Bearer "+u.ResolveAPIKey())
	}
	return nil
}

// ensureAccessToken 向百度千帆换取临时 access_token 并内存缓存。
func (n *Node) ensureAccessToken() (string, error) {
	rt := n.Runtime
	rt.tokenMu.Lock()
	defer rt.tokenMu.Unlock()

	if rt.accessToken != "" && time.Now().Before(rt.tokenExpiry.Add(-time.Minute)) {
		return rt.accessToken, nil
	}

	tokenURL := fmt.Sprintf(
		"https://aip.baidubce.com/oauth/2.0/token?grant_type=client_credentials&client_id=%s&client_secret=%s",
		url.QueryEscape(n.Upstream.ResolveAPIKey()),
		url.QueryEscape(n.Upstream.ResolveSecretKey()),
	)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(tokenURL, "application/json", nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var parsed struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", err
	}
	if parsed.AccessToken == "" {
		return "", fmt.Errorf("千帆未返回 access_token (HTTP %d)", resp.StatusCode)
	}
	rt.accessToken = parsed.AccessToken
	if parsed.ExpiresIn <= 0 {
		parsed.ExpiresIn = 30 * 24 * 3600
	}
	rt.tokenExpiry = time.Now().Add(time.Duration(parsed.ExpiresIn) * time.Second)
	return rt.accessToken, nil
}

// --- 路由策略工厂 ---

type RoutingStrategy interface {
	Name() string
	// Select 从候选节点（已按断路器与模型过滤）中挑选最佳节点。
	Select(ctx *PluginContext, rule *RouteRule, nodes []*Node) (*Node, error)
}

type RouteRule struct {
	Path         string
	Strategy     RoutingStrategy
	Nodes        []*Node
	DefaultModel string
	DomainTags   []string
	Fallback     *config.Fallback // 请求级失败转移配置（nil = 不启用）
	counter      atomic.Uint64
}

var (
	ErrCircuitOpen  = fmt.Errorf("circuit breaker open")
	ErrNoRoute      = fmt.Errorf("no matching route")
	errAllNodesOpen = fmt.Errorf("all upstream nodes circuit-open")
)

// NoModelSupportError 表示请求模型在该路由无任何上游支持。
type NoModelSupportError struct {
	Model string
}

func (e *NoModelSupportError) Error() string {
	return fmt.Sprintf("no upstream supports model %q", e.Model)
}

var strategyRegistry = map[string]RoutingStrategy{
	"round_robin":      roundRobinStrategy{},
	"least_connection": leastConnStrategy{},
	"latency":          latencyStrategy{},
	"cost":             costStrategy{},
	"domain_tag":       domainTagStrategy{},
}

func StrategyNames() []string {
	out := make([]string, 0, len(strategyRegistry))
	for name := range strategyRegistry {
		out = append(out, name)
	}
	return out
}

func LookupStrategy(name string) (RoutingStrategy, bool) {
	s, ok := strategyRegistry[name]
	return s, ok
}

// circuitAllowed 过滤掉处于熔断开启态的节点。
func circuitAllowed(nodes []*Node) []*Node {
	out := make([]*Node, 0, len(nodes))
	for _, n := range nodes {
		if n.Runtime.breaker.Allow() {
			out = append(out, n)
		}
	}
	return out
}

// supportsModel 判断上游是否声明支持指定模型；未声明 models 视为不限。
func supportsModel(u *config.Upstream, model string) bool {
	if model == "" || len(u.Models) == 0 {
		return true
	}
	for _, m := range u.Models {
		if m == model {
			return true
		}
	}
	return false
}

// modelAllowed 按模型白名单过滤节点。
func modelAllowed(nodes []*Node, model string) []*Node {
	out := make([]*Node, 0, len(nodes))
	for _, n := range nodes {
		if supportsModel(n.Upstream, model) {
			out = append(out, n)
		}
	}
	return out
}

type roundRobinStrategy struct{}

func (roundRobinStrategy) Name() string { return "round_robin" }
func (roundRobinStrategy) Select(_ *PluginContext, rule *RouteRule, nodes []*Node) (*Node, error) {
	if len(nodes) == 0 {
		return nil, errAllNodesOpen
	}
	i := rule.counter.Add(1) - 1
	return nodes[int(i)%len(nodes)], nil
}

type leastConnStrategy struct{}

func (leastConnStrategy) Name() string { return "least_connection" }
func (leastConnStrategy) Select(_ *PluginContext, rule *RouteRule, nodes []*Node) (*Node, error) {
	if len(nodes) == 0 {
		return nil, errAllNodesOpen
	}
	best := nodes[0]
	bestConn := best.Runtime.ConnCount()
	tie := []*Node{best}
	for _, n := range nodes[1:] {
		c := n.Runtime.ConnCount()
		if c < bestConn {
			bestConn = c
			tie = []*Node{n}
		} else if c == bestConn {
			tie = append(tie, n)
		}
	}
	i := rule.counter.Add(1) - 1
	return tie[int(i)%len(tie)], nil
}

type latencyStrategy struct{}

func (latencyStrategy) Name() string { return "latency" }
func (latencyStrategy) Select(_ *PluginContext, rule *RouteRule, nodes []*Node) (*Node, error) {
	if len(nodes) == 0 {
		return nil, errAllNodesOpen
	}
	// 仅在已有实测延迟（EMA > 0）的节点中选最小；未测量节点不得作为"最快"候选，
	// 避免冷启动时把未经考验的节点当成首选导致雪崩。
	var measured []*Node
	for _, n := range nodes {
		if n.Runtime.LatencyEMA() > 0 {
			measured = append(measured, n)
		}
	}
	if len(measured) == 0 {
		i := rule.counter.Add(1) - 1
		return nodes[int(i)%len(nodes)], nil
	}
	best := measured[0]
	bestLat := best.Runtime.LatencyEMA()
	tie := []*Node{best}
	for _, n := range measured[1:] {
		l := n.Runtime.LatencyEMA()
		if l < bestLat {
			bestLat = l
			tie = []*Node{n}
		} else if l == bestLat {
			tie = append(tie, n)
		}
	}
	i := rule.counter.Add(1) - 1
	return tie[int(i)%len(tie)], nil
}

type costStrategy struct{}

func (costStrategy) Name() string { return "cost" }
func (costStrategy) Select(_ *PluginContext, rule *RouteRule, nodes []*Node) (*Node, error) {
	if len(nodes) == 0 {
		return nil, errAllNodesOpen
	}
	best := nodes[0]
	bestCost := best.Upstream.CostPer1K
	tie := []*Node{best}
	for _, n := range nodes[1:] {
		c := n.Upstream.CostPer1K
		if c < bestCost {
			bestCost = c
			tie = []*Node{n}
		} else if c == bestCost {
			tie = append(tie, n)
		}
	}
	i := rule.counter.Add(1) - 1
	return tie[int(i)%len(tie)], nil
}

type domainTagStrategy struct{}

func (domainTagStrategy) Name() string { return "domain_tag" }
func (domainTagStrategy) Select(ctx *PluginContext, rule *RouteRule, nodes []*Node) (*Node, error) {
	if len(nodes) == 0 {
		return nil, errAllNodesOpen
	}
	intent := requestIntentTags(ctx)
	bestScore := -1
	var best []*Node
	for _, n := range nodes {
		score := tagOverlap(intent, n.Upstream.Tags)
		if score > bestScore {
			bestScore = score
			best = []*Node{n}
		} else if score == bestScore {
			best = append(best, n)
		}
	}
	if bestScore <= 0 {
		i := rule.counter.Add(1) - 1
		return nodes[int(i)%len(nodes)], nil
	}
	i := rule.counter.Add(1) - 1
	return best[int(i)%len(best)], nil
}

func requestIntentTags(ctx *PluginContext) map[string]bool {
	out := map[string]bool{}
	if v, ok := ctx.Get(CtxAuthKey); ok {
		if ak, ok := v.(*config.AuthKey); ok {
			for _, t := range ak.Tags {
				out[t] = true
			}
		}
	}
	return out
}

func tagOverlap(intent map[string]bool, tags []string) int {
	n := 0
	for _, t := range tags {
		if intent[t] {
			n++
		}
	}
	return n
}

// --- 路由器 ---

type Router struct {
	rules []*RouteRule
	nodes map[string]*Node
}

// NewRouter 依据配置构建路由表；runtimes 为跨热加载持久化的节点状态。
func NewRouter(cfg *config.Config, runtimes map[string]*NodeRuntime, transportFactory func(*config.Upstream) *http.Transport) (*Router, error) {
	nodes := map[string]*Node{}
	for _, u := range cfg.Upstreams {
		rt, ok := runtimes[u.Name]
		if !ok {
			rt = newNodeRuntime(u.Circuit)
			runtimes[u.Name] = rt
		}
		nodes[u.Name] = &Node{
			Upstream:  u,
			Runtime:   rt,
			Transport: transportFactory(u),
		}
	}

	r := &Router{nodes: nodes}
	for _, rc := range cfg.Routers {
		strategy, ok := LookupStrategy(rc.Strategy)
		if !ok {
			return nil, fmt.Errorf("router %s 策略未注册: %s", rc.Path, rc.Strategy)
		}
		rule := &RouteRule{
			Path:         rc.Path,
			Strategy:     strategy,
			DefaultModel: rc.DefaultModel,
			DomainTags:   rc.DomainTags,
			Fallback:     rc.Fallback,
		}
		for _, name := range rc.Upstreams {
			if n, ok := nodes[name]; ok {
				rule.Nodes = append(rule.Nodes, n)
			}
		}
		if len(rule.Nodes) == 0 {
			return nil, fmt.Errorf("router %s 无可绑定上游", rc.Path)
		}
		r.rules = append(r.rules, rule)
	}
	if len(r.rules) == 0 {
		return nil, fmt.Errorf("未配置任何路由规则")
	}
	return r, nil
}

func (r *Router) match(path string) *RouteRule {
	for _, rule := range r.rules {
		if rule.Path == path {
			return rule
		}
	}
	return nil
}

type nodeCtxKeyType struct{}

var nodeCtxKey = nodeCtxKeyType{}

// RouteCandidates 返回有序候选节点链（docs/fallback-design.md 第 4 节）。
// 首选 = 策略选出的最优节点（与 Route 的历史选择完全一致，保证 fail_fast 零变化），
// 其余 = 同模型候选（保持过滤后原顺序）。
// 降级候选（model_map）由 fallback 流程在 M3 阶段追加。
func (r *Router) RouteCandidates(ctx *PluginContext, req *http.Request) ([]*Node, error) {
	rule := r.match(req.URL.Path)
	if rule == nil {
		return nil, ErrNoRoute
	}

	nodes := circuitAllowed(rule.Nodes)
	if len(nodes) == 0 {
		return nil, errAllNodesOpen
	}

	model := ctx.GetString(CtxModel)
	if model != "" {
		supported := modelAllowed(nodes, model)
		if len(supported) == 0 {
			return nil, &NoModelSupportError{Model: model}
		}
		nodes = supported
	}

	primary, err := rule.Strategy.Select(ctx, rule, nodes)
	if err != nil {
		return nil, err
	}

	candidates := make([]*Node, 0, len(nodes))
	candidates = append(candidates, primary)
	for _, n := range nodes {
		if n != primary {
			candidates = append(candidates, n)
		}
	}
	return candidates, nil
}

// attach 将选中节点挂入请求 Context（重写 URL/Host/凭证），供 RoundTripper 使用。
func (r *Router) attach(ctx *PluginContext, req *http.Request, node *Node) error {
	if err := node.Rewrite(ctx, req); err != nil {
		return err
	}
	nctx := context.WithValue(req.Context(), nodeCtxKey, node)
	*req = *req.WithContext(nctx)
	ctx.Set(CtxUpstreamName, node.Upstream.Name)
	return nil
}

// Route 选择最佳节点并重写请求（历史行为，等价于 fail_fast 路径）。
func (r *Router) Route(ctx *PluginContext, req *http.Request) error {
	candidates, err := r.RouteCandidates(ctx, req)
	if err != nil {
		return err
	}
	return r.attach(ctx, req, candidates[0])
}

// RouteWithFallback 是请求级失败转移入口（docs/fallback-design.md）。
// fail_fast（默认）：取候选链首选，行为与 Route 完全一致（回归零变化）；
// failover：构建候选链（同模型 + model_map 降级）与 fallbackPlan 挂入请求 Context，
// 由 routerRoundTripper 循环尝试。
func (r *Router) RouteWithFallback(ctx *PluginContext, req *http.Request) error {
	same, err := r.RouteCandidates(ctx, req)
	if err != nil {
		return err
	}
	rule := r.match(req.URL.Path)
	if rule == nil || rule.Fallback == nil || rule.Fallback.Mode != "failover" {
		return r.attach(ctx, req, same[0])
	}
	fb := rule.Fallback

	// 候选链 = 同模型候选（model=""）+ 降级候选（model_map，改写 body model）
	candidates := make([]fallbackCandidate, 0, len(same)+len(fb.ModelMap))
	for _, n := range same {
		candidates = append(candidates, fallbackCandidate{node: n})
	}
	curModel := ctx.GetString(CtxModel)
	// agent 宽松模式：禁用 model_map 降档（不悄悄换模型），同模型候选保留（docs/agent-mode.md）
	agent := false
	if key, ok := ctx.GetValue(CtxAuthKey).(*config.AuthKey); ok && key.AgentMode {
		agent = true
	}
	for _, mm := range fb.ModelMap {
		if agent {
			break
		}
		if mm.To.Model == "" || mm.To.Model == curModel {
			continue
		}
		target, ok := r.nodes[mm.To.Upstream]
		if !ok {
			continue
		}
		// 降级目标必须过 vKey model_whitelist（运行期校验，否则降级成为越权通道）
		if !modelAllowedForKey(ctx, mm.To.Model) {
			continue
		}
		candidates = append(candidates, fallbackCandidate{node: target, model: mm.To.Model})
	}
	if len(candidates) == 0 {
		return r.attach(ctx, req, same[0])
	}

	origURL := *req.URL
	// failover 每次尝试都需重放原始 body（首次 RoundTrip 会耗尽 reader，见 fallback-design.md 第 7 节）
	var origBody []byte
	if req.Body != nil && req.Body != http.NoBody {
		cfg, _ := ctx.GetValue(CtxConfig).(*config.Config)
		maxBytes := int64(0)
		if cfg != nil {
			maxBytes = cfg.Transport.MaxRequestBodyBytes
		}
		b, err := ReadBody(req.Body, maxBytes)
		if err != nil {
			if errors.Is(err, ErrBodyTooLarge) {
				return err // 由 ErrorHandler 映射 413
			}
			// 其它读取错误：不缓存 body，降级为无重放（与历史行为一致）
		} else {
			origBody = b
			req.Body = io.NopCloser(bytes.NewReader(b))
			req.ContentLength = int64(len(b))
		}
	}

	// 先挂首选节点（供 NodeFromContext / ModifyResponse 使用），再挂 fallback 计划
	nctx := context.WithValue(req.Context(), nodeCtxKey, candidates[0].node)
	*req = *req.WithContext(nctx)
	plan := &fallbackPlan{candidates: candidates, fb: fb, origURL: origURL, origBody: origBody}
	pctx := context.WithValue(req.Context(), fallbackPlanKey, plan)
	*req = *req.WithContext(pctx)
	return nil
}

// modelAllowedForKey 判断降级目标模型是否在 vKey 白名单内（白名单为空视为不限）。
func modelAllowedForKey(ctx *PluginContext, model string) bool {
	v, ok := ctx.Get(CtxAuthKey)
	if !ok {
		return true
	}
	ak, ok := v.(*config.AuthKey)
	if !ok || len(ak.ModelWhitelist) == 0 {
		return true
	}
	for _, m := range ak.ModelWhitelist {
		if m == model {
			return true
		}
	}
	return false
}

func NodeFromContext(ctx context.Context) (*Node, bool) {
	n, ok := ctx.Value(nodeCtxKey).(*Node)
	return n, ok
}

// Nodes 暴露节点快照，供 Doctor 诊断与压测报告使用。
func (r *Router) Nodes() map[string]*Node { return r.nodes }

func (r *Router) Rules() []*RouteRule { return r.rules }
