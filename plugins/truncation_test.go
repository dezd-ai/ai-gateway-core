package plugins

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DezDing/ai-gateway-core/config"
	"github.com/DezDing/ai-gateway-core/kernel"
)

// TestMidStreamTruncationOpensBreaker 验证 fallback-design.md 第 8 节：
// 上游返回 200 后流中途断（未见 [DONE]）计入该节点断路器失败（阈值 1 → open），
// 且网关补齐错误 chunk + [DONE]，客户端不悬挂。
func TestMidStreamTruncationOpensBreaker(t *testing.T) {
	trunc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"半截\"}}]}\n\n")
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
		// 不写 [DONE] 直接返回 → 上游流中途断
	}))
	defer trunc.Close()

	cfg := &config.Config{
		Server: config.Server{Host: "127.0.0.1", Port: 8080},
		Auth:   config.Auth{Keys: map[string]*config.AuthKey{"sk-test": {VKey: "sk-test", Username: "u", Department: "d"}}},
		Plugins: []config.Plugin{
			{Name: "dynamic_router", Enabled: true, Order: 1},
			{Name: "auditor", Enabled: true, Order: 2},
		},
		Upstreams: []*config.Upstream{{
			Name: "mock", BaseURL: trunc.URL, Protocol: "openai", AuthMode: "direct", APIKey: "k",
			Circuit: config.CircuitBreakerConfig{FailureThreshold: 1, OpenTimeoutSec: 30, HalfOpenMax: 3},
		}},
		Routers: []config.Router{{
			Path: "/v1/chat/completions", Strategy: "round_robin", Upstreams: []string{"mock"},
		}},
		Transport:   config.Transport{ConnectTimeoutSec: 5, TTFTTimeoutSec: 10, IdleReadTimeoutSec: 30},
		EdgeStorage: config.EdgeStorage{Enabled: false},
	}

	g, err := kernel.NewGatewayFromConfig(cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	defer g.Shutdown()

	srv := httptest.NewServer(g)
	defer srv.Close()

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"x","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer sk-test")

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()

	// 网关应补齐错误 chunk + [DONE]，客户端不悬挂
	if !strings.Contains(string(body), "[DONE]") {
		t.Fatalf("应补齐 [DONE]，实际 body: %s", body)
	}
	if !strings.Contains(string(body), "stream_truncated") {
		t.Fatalf("应含截断错误 chunk，实际 body: %s", body)
	}

	// 断流应计入断路器失败（阈值 1 → open）
	state := g.Router().Nodes()["mock"].Runtime.Breaker().State()
	if state != "open" {
		t.Fatalf("mid-stream 断流后断路器应为 open, got %s", state)
	}
}
