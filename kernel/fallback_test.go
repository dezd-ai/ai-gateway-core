package kernel

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DezDing/ai-gateway-core/config"
)

func TestClassifyFailure(t *testing.T) {
	cases := []struct {
		name string
		res  *http.Response
		err  error
		want failClass
	}{
		{"200 成功", &http.Response{StatusCode: 200}, nil, classSuccess},
		{"400 客户端错误", &http.Response{StatusCode: 400}, nil, classClientError},
		{"429 限流", &http.Response{StatusCode: 429}, nil, classRateLimit},
		{"500 服务端错误", &http.Response{StatusCode: 500}, nil, classServerError},
		{"超时", nil, &net.DNSError{IsTimeout: true}, classTimeout},
		{"连接失败", nil, errors.New("connection refused"), classConnect},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := classifyFailure(c.res, c.err); got != c.want {
				t.Fatalf("classifyFailure = %v, want %v", got, c.want)
			}
		})
	}
}

// TestRouteCandidatesOrder 验证候选链顺序：首选 = 策略选出节点，其余保持原顺序，轮询推进。
func TestRouteCandidatesOrder(t *testing.T) {
	nodes := []*Node{
		{Runtime: newNodeRuntime(config.CircuitBreakerConfig{})},
		{Runtime: newNodeRuntime(config.CircuitBreakerConfig{})},
		{Runtime: newNodeRuntime(config.CircuitBreakerConfig{})},
	}
	rule := &RouteRule{Path: "/v1/chat/completions", Strategy: roundRobinStrategy{}, Nodes: nodes}
	router := &Router{rules: []*RouteRule{rule}}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx := NewPluginContext()

	cs, err := router.RouteCandidates(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 3 {
		t.Fatalf("候选数 = %d, want 3", len(cs))
	}
	if cs[0] != nodes[0] {
		t.Fatal("首次首选应为 nodes[0]（轮询从 0 开始）")
	}
	if cs[1] != nodes[1] || cs[2] != nodes[2] {
		t.Fatal("其余候选应保持原顺序")
	}

	cs2, err := router.RouteCandidates(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if cs2[0] != nodes[1] {
		t.Fatal("第二次首选应为 nodes[1]（轮询推进）")
	}
}

// TestRouteAndRouteWithFallbackParity 验证 M1 承诺：RouteWithFallback（fail_fast 路径）
// 与 Route 历史行为完全一致（选同一节点、写同一 Context 键）。
func TestRouteAndRouteWithFallbackParity(t *testing.T) {
	mk := func() (*Router, *PluginContext, *http.Request) {
		nodes := []*Node{
			{Upstream: &config.Upstream{Name: "u0", BaseURL: "https://u0.example.com", EndpointPath: "/v1/chat/completions", AuthMode: "direct"}, Runtime: newNodeRuntime(config.CircuitBreakerConfig{})},
			{Upstream: &config.Upstream{Name: "u1", BaseURL: "https://u1.example.com", EndpointPath: "/v1/chat/completions", AuthMode: "direct"}, Runtime: newNodeRuntime(config.CircuitBreakerConfig{})},
		}
		rule := &RouteRule{Path: "/v1/chat/completions", Strategy: roundRobinStrategy{}, Nodes: nodes}
		r := &Router{rules: []*RouteRule{rule}}
		ctx := NewPluginContext()
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		return r, ctx, req
	}

	r1, c1, req1 := mk()
	if err := r1.Route(c1, req1); err != nil {
		t.Fatal(err)
	}
	r2, c2, req2 := mk()
	if err := r2.RouteWithFallback(c2, req2); err != nil {
		t.Fatal(err)
	}

	if got := c1.GetString(CtxUpstreamName); got != "u0" {
		t.Fatalf("Route 选中的上游 = %q, want u0", got)
	}
	if got := c2.GetString(CtxUpstreamName); got != "u0" {
		t.Fatalf("RouteWithFallback 选中的上游 = %q, want u0", got)
	}
}

// --- M2：fallbackPlan 循环尝试集成测试 ---

func makeUpstreamNode(name, baseURL string) *Node {
	return &Node{
		Upstream:  &config.Upstream{Name: name, BaseURL: baseURL, AuthMode: "direct", APIKey: "k"},
		Runtime:   newNodeRuntime(config.CircuitBreakerConfig{}),
		Transport: &http.Transport{},
	}
}

func makePlan(candidates []*Node, fb *config.Fallback) (*fallbackPlan, *PluginContext, *http.Request) {
	ctx := NewPluginContext()
	req := httptest.NewRequest(http.MethodPost, "http://gateway/v1/chat/completions", nil)
	cc := make([]fallbackCandidate, len(candidates))
	for i, n := range candidates {
		cc[i] = fallbackCandidate{node: n}
	}
	return &fallbackPlan{candidates: cc, fb: fb, origURL: *req.URL}, ctx, req
}

func TestFallbackPlanSwitchOn5xx(t *testing.T) {
	var hitsB atomic.Int64
	serverB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitsB.Add(1)
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	}))
	defer serverB.Close()
	serverA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer serverA.Close()

	plan, ctx, req := makePlan([]*Node{makeUpstreamNode("a", serverA.URL), makeUpstreamNode("b", serverB.URL)},
		&config.Fallback{Mode: "failover"})
	res, err := plan.RoundTrip(req, ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if hitsB.Load() != 1 {
		t.Fatalf("B 应被调用 1 次, got %d", hitsB.Load())
	}
	if got := ctx.GetInt64(CtxAttempts); got != 2 {
		t.Fatalf("attempts = %d, want 2", got)
	}
	chain := ctx.GetStrings(CtxFallbackChain)
	if len(chain) != 2 || chain[0] != "a" || chain[1] != "b" {
		t.Fatalf("chain = %v", chain)
	}
	if res.Header.Get("X-AI-Gateway-Fallback") != "" {
		t.Fatal("未开启透明头不应附加")
	}
}

func TestFallbackPlanTransparentHeader(t *testing.T) {
	var hitsB atomic.Int64
	serverB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitsB.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer serverB.Close()
	serverA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer serverA.Close()

	plan, ctx, req := makePlan([]*Node{makeUpstreamNode("a", serverA.URL), makeUpstreamNode("b", serverB.URL)},
		&config.Fallback{Mode: "failover", TransparentHeader: true})
	res, err := plan.RoundTrip(req, ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if got := res.Header.Get("X-AI-Gateway-Fallback"); got != "a→b" {
		t.Fatalf("Fallback 头 = %q, want a→b", got)
	}
	if got := res.Header.Get("X-AI-Gateway-Attempts"); got != "2" {
		t.Fatalf("Attempts 头 = %q, want 2", got)
	}
}

func TestFallbackPlan4xxNeverRetried(t *testing.T) {
	var hitsB atomic.Int64
	serverB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitsB.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer serverB.Close()
	serverA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer serverA.Close()

	plan, ctx, req := makePlan([]*Node{makeUpstreamNode("a", serverA.URL), makeUpstreamNode("b", serverB.URL)},
		&config.Fallback{Mode: "failover"})
	res, err := plan.RoundTrip(req, ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", res.StatusCode)
	}
	if hitsB.Load() != 0 {
		t.Fatal("4xx 不应切换")
	}
}

func TestFallbackPlanRetryOnFilter(t *testing.T) {
	var hitsB atomic.Int64
	serverB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitsB.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer serverB.Close()
	serverA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer serverA.Close()

	// retry_on 只声明 429：5xx 应作为最终响应返回，不切换
	plan, ctx, req := makePlan([]*Node{makeUpstreamNode("a", serverA.URL), makeUpstreamNode("b", serverB.URL)},
		&config.Fallback{Mode: "failover", RetryOn: []string{"429"}})
	res, err := plan.RoundTrip(req, ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500（5xx 未被 retry_on 声明时不切换）", res.StatusCode)
	}
	if hitsB.Load() != 0 {
		t.Fatal("不应切换到 B")
	}
}

func TestFallbackPlan429Switch(t *testing.T) {
	var hitsB atomic.Int64
	serverB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitsB.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer serverB.Close()
	serverA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer serverA.Close()

	plan, ctx, req := makePlan([]*Node{makeUpstreamNode("a", serverA.URL), makeUpstreamNode("b", serverB.URL)},
		&config.Fallback{Mode: "failover"})
	res, err := plan.RoundTrip(req, ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if hitsB.Load() != 1 {
		t.Fatal("429 应切换到 B")
	}
}

func TestFallbackPlanMaxAttemptsTruncation(t *testing.T) {
	var hitsB atomic.Int64
	serverB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitsB.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer serverB.Close()
	serverA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer serverA.Close()

	// max_attempts=1：只试首选，B 永不尝试（截断）
	plan, ctx, req := makePlan([]*Node{makeUpstreamNode("a", serverA.URL), makeUpstreamNode("b", serverB.URL)},
		&config.Fallback{Mode: "failover", MaxAttempts: 1})
	res, err := plan.RoundTrip(req, ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", res.StatusCode)
	}
	if hitsB.Load() != 0 {
		t.Fatal("max_attempts=1 时 B 不应被尝试")
	}
	if got := ctx.GetString(CtxStopReason); got != "exhausted" {
		t.Fatalf("stop_reason = %q, want exhausted", got)
	}
}

func TestFallbackPlanConnectRetry(t *testing.T) {
	// 关闭的服务器 = 连接被拒（classConnect）
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close()

	var hitsB atomic.Int64
	serverB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitsB.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer serverB.Close()

	// connect_retry=1：A 连接失败重试 1 次，仍失败才切 B。
	// max_attempts=3（默认 = 候选链长 2 时，connect 重试会占用 B 的额度，见设计文档 §5 语义）
	plan, ctx, req := makePlan([]*Node{makeUpstreamNode("a", deadURL), makeUpstreamNode("b", serverB.URL)},
		&config.Fallback{Mode: "failover", ConnectRetry: 1, MaxAttempts: 3})
	res, err := plan.RoundTrip(req, ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if got := ctx.GetInt64(CtxAttempts); got != 3 {
		t.Fatalf("attempts = %d, want 3（A 1 次 + A 重试 1 次 + B 1 次）", got)
	}
	if hitsB.Load() != 1 {
		t.Fatal("B 应被调用 1 次")
	}
}

func TestFallbackPlanTotalTimeout(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
	}))
	defer slow.Close()
	serverB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer serverB.Close()

	// total_timeout_ms=50：A 超时后预算耗尽，B 不再尝试
	plan, ctx, req := makePlan([]*Node{makeUpstreamNode("a", slow.URL), makeUpstreamNode("b", serverB.URL)},
		&config.Fallback{Mode: "failover", TotalTimeoutMS: 50})
	res, err := plan.RoundTrip(req, ctx)
	if err == nil {
		defer res.Body.Close()
		t.Fatal("总超时预算耗尽应返回错误")
	}
	if got := ctx.GetString(CtxStopReason); got != "timeout" {
		t.Fatalf("stop_reason = %q, want timeout", got)
	}
	if got := ctx.GetInt64(CtxAttempts); got != 1 {
		t.Fatalf("attempts = %d, want 1", got)
	}
}

func TestNodeRoundTrip429CountsFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	rt := newNodeRuntime(config.CircuitBreakerConfig{FailureThreshold: 1, OpenTimeoutSec: 30, HalfOpenMax: 3})
	node := &Node{
		Upstream:  &config.Upstream{Name: "n", BaseURL: server.URL, AuthMode: "direct"},
		Runtime:   rt,
		Transport: &http.Transport{},
	}
	req := httptest.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", nil)
	res, err := node.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d", res.StatusCode)
	}
	if state := rt.breaker.State(); state != "open" {
		t.Fatalf("429 应计入失败并熔断（阈值 1）, state = %s", state)
	}
}

// --- M3：model_map 降级 ---

func TestRewriteModelField(t *testing.T) {
	in := []byte(`{"model":"m1","messages":[{"role":"user","content":"hi"}]}`)
	out := rewriteModelField(in, "m2")
	if !bytes.Contains(out, []byte(`"model":"m2"`)) {
		t.Fatalf("model 应改写为 m2, got %s", out)
	}
	if got := rewriteModelField([]byte("not-json"), "m2"); string(got) != "not-json" {
		t.Fatalf("非 JSON 应原样返回, got %s", got)
	}
}

// TestFallbackPlanModelMapDowngrade 验证降级候选：切到 (backup, m2) 时
// 请求体 model 被改写为 m2 且 ContentLength 同步更新。
func TestFallbackPlanModelMapDowngrade(t *testing.T) {
	var gotBody atomic.Value
	serverB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody.Store(string(b))
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	}))
	defer serverB.Close()
	serverA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer serverA.Close()

	ctx := NewPluginContext()
	req := httptest.NewRequest(http.MethodPost, "http://gateway/v1/chat/completions",
		strings.NewReader(`{"model":"m1","messages":[{"role":"user","content":"hi"}]}`))
	plan := &fallbackPlan{
		candidates: []fallbackCandidate{
			{node: makeUpstreamNode("a", serverA.URL)},
			{node: makeUpstreamNode("b", serverB.URL), model: "m2"},
		},
		fb:       &config.Fallback{Mode: "failover"},
		origURL:  *req.URL,
		origBody: []byte(`{"model":"m1","messages":[{"role":"user","content":"hi"}]}`),
	}

	res, err := plan.RoundTrip(req, ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	body := gotBody.Load().(string)
	if !strings.Contains(body, `"model":"m2"`) {
		t.Fatalf("B 收到的 model 应改写为 m2, body=%s", body)
	}
	if got := ctx.GetInt64(CtxAttempts); got != 2 {
		t.Fatalf("attempts = %d, want 2", got)
	}
	if got := ctx.GetStrings(CtxFallbackChain); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("chain = %v", got)
	}
}

// TestRouteWithFallbackModelMapWhitelistSkip 验证降级目标过 vKey 白名单：
// 白名单不含 m2 时，model_map 降级候选被跳过，且不缓存 body。
func TestRouteWithFallbackModelMapWhitelistSkip(t *testing.T) {
	nodes := []*Node{
		{Upstream: &config.Upstream{Name: "a", BaseURL: "https://a.example.com", AuthMode: "direct", Models: []string{"m1"}}, Runtime: newNodeRuntime(config.CircuitBreakerConfig{}), Transport: &http.Transport{}},
		{Upstream: &config.Upstream{Name: "b", BaseURL: "https://b.example.com", AuthMode: "direct", Models: []string{"m2"}}, Runtime: newNodeRuntime(config.CircuitBreakerConfig{}), Transport: &http.Transport{}},
	}
	rule := &RouteRule{
		Path: "/v1/chat/completions", Strategy: roundRobinStrategy{}, Nodes: nodes,
		Fallback: &config.Fallback{Mode: "failover", ModelMap: []config.ModelMapEntry{
			{From: "m1", To: config.ModelMapTarget{Upstream: "b", Model: "m2"}},
		}},
	}
	router := &Router{rules: []*RouteRule{rule}, nodes: map[string]*Node{"a": nodes[0], "b": nodes[1]}}

	ctx := NewPluginContext()
	ctx.Set(CtxModel, "m1")
	ctx.Set(CtxAuthKey, &config.AuthKey{VKey: "k", ModelWhitelist: []string{"m1"}})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	if err := router.RouteWithFallback(ctx, req); err != nil {
		t.Fatal(err)
	}
	plan, ok := req.Context().Value(fallbackPlanKey).(*fallbackPlan)
	if !ok || plan == nil {
		t.Fatal("应生成 fallbackPlan")
	}
	if len(plan.candidates) != 1 || plan.candidates[0].node != nodes[0] {
		t.Fatalf("白名单不含 m2 时降级候选应被跳过, candidates=%+v", plan.candidates)
	}
	if plan.origBody != nil {
		t.Fatal("请求体为 NoBody 时不应缓存 body")
	}
}

// TestRouteWithFallbackAgentSkipsModelMap agent key 剔除 model_map 降级候选（docs/agent-mode.md）。
func TestRouteWithFallbackAgentSkipsModelMap(t *testing.T) {
	nodes := []*Node{
		{Upstream: &config.Upstream{Name: "a", BaseURL: "https://a.example.com", AuthMode: "direct", Models: []string{"m1"}}, Runtime: newNodeRuntime(config.CircuitBreakerConfig{}), Transport: &http.Transport{}},
		{Upstream: &config.Upstream{Name: "b", BaseURL: "https://b.example.com", AuthMode: "direct", Models: []string{"m2"}}, Runtime: newNodeRuntime(config.CircuitBreakerConfig{}), Transport: &http.Transport{}},
	}
	rule := &RouteRule{
		Path: "/v1/chat/completions", Strategy: roundRobinStrategy{}, Nodes: nodes,
		Fallback: &config.Fallback{Mode: "failover", ModelMap: []config.ModelMapEntry{
			{From: "m1", To: config.ModelMapTarget{Upstream: "b", Model: "m2"}},
		}},
	}
	router := &Router{rules: []*RouteRule{rule}, nodes: map[string]*Node{"a": nodes[0], "b": nodes[1]}}

	ctx := NewPluginContext()
	ctx.Set(CtxModel, "m1")
	ctx.Set(CtxAuthKey, &config.AuthKey{VKey: "sk-agent", AgentMode: true, ModelWhitelist: []string{"m1", "m2"}})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	if err := router.RouteWithFallback(ctx, req); err != nil {
		t.Fatal(err)
	}
	plan, ok := req.Context().Value(fallbackPlanKey).(*fallbackPlan)
	if !ok || plan == nil {
		t.Fatal("应生成 fallbackPlan")
	}
	if len(plan.candidates) != 1 {
		t.Fatalf("agent 应剔除 model_map 降级候选, got %d 个候选", len(plan.candidates))
	}
	if plan.candidates[0].model != "" {
		t.Fatalf("agent 候选不应带降级模型, got %q", plan.candidates[0].model)
	}
}
