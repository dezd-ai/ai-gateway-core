package plugins

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/dezd-ai/ai-gateway-core/config"
	"github.com/dezd-ai/ai-gateway-core/kernel"
)

// newAliasGateway 构造带 mock 上游的网关，返回网关与上游捕获的请求体。
func newAliasGateway(t *testing.T, aliases map[string]string, whitelist []string) (*kernel.Gateway, func() string) {
	t.Helper()
	kernel.SetAuditStdout(false)
	t.Cleanup(func() { kernel.SetAuditStdout(true) })

	var mu sync.Mutex
	got := ""
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = string(b)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","object":"chat.completion","model":"deepseek-v4-flash","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`))
	}))
	t.Cleanup(up.Close)

	cfg := &config.Config{
		Server: config.Server{Host: "127.0.0.1", Port: 8080},
		Auth: config.Auth{Keys: map[string]*config.AuthKey{
			"sk-ok": {VKey: "sk-ok", Username: "u", ModelWhitelist: whitelist},
		}},
		Plugins: []config.Plugin{
			{Name: "auth_vkey", Enabled: true, Order: 1},
			{Name: "prompt_injector", Enabled: true, Order: 2},
			{Name: "dynamic_router", Enabled: true, Order: 4},
			{Name: "auditor", Enabled: true, Order: 5},
		},
		Upstreams: []*config.Upstream{{
			Name: "u", BaseURL: up.URL, Protocol: "openai", AuthMode: "direct", APIKey: "k",
			Models: []string{"deepseek-v4-flash", "qwen-turbo"},
		}},
		Routers: []config.Router{{
			Path: "/v1/chat/completions", Strategy: "round_robin", Upstreams: []string{"u"},
		}},
		Transport:    config.Transport{ConnectTimeoutSec: 5, TTFTTimeoutSec: 10, IdleReadTimeoutSec: 30},
		EdgeStorage:  config.EdgeStorage{Enabled: true, MaxDiskMB: 1},
		ModelAliases: aliases,
	}
	g, err := kernel.NewGatewayFromConfig(cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.Shutdown)
	return g, func() string {
		mu.Lock()
		defer mu.Unlock()
		return got
	}
}

// TestModelAliasTranslation 验证 roadmap R3：客户端传别名，上游收到真实名；
// 白名单校验的是翻译后的真实名（设计稿 §4）。
func TestModelAliasTranslation(t *testing.T) {
	g, gotBody := newAliasGateway(t, map[string]string{"chat": "deepseek-v4-flash"}, []string{"deepseek-v4-flash"})
	srv := httptest.NewServer(g)
	defer srv.Close()

	post := func(model string) int {
		body := `{"model":"` + model + `","messages":[{"role":"user","content":"hi"}]}`
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer sk-ok")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}

	// 别名 chat → 真实名 deepseek-v4-flash：200，上游 body 的 model 已翻译
	if code := post("chat"); code != http.StatusOK {
		t.Fatalf("别名请求应 200, got %d", code)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(gotBody()), &body); err != nil {
		t.Fatal(err)
	}
	if body["model"] != "deepseek-v4-flash" {
		t.Fatalf("上游应收到真实模型名, got %v", body["model"])
	}

	// 真实名直传：向后兼容
	if code := post("deepseek-v4-flash"); code != http.StatusOK {
		t.Fatalf("真实名请求应 200, got %d", code)
	}
}

// TestModelAliasWhitelistValidatesRealName 验证白名单校验的是翻译后的真实名：
// 白名单写别名 "chat" 时，翻译后 deepseek-v4-flash 不在白名单 → 403（设计稿 §4 唯一真相源）。
func TestModelAliasWhitelistValidatesRealName(t *testing.T) {
	g, _ := newAliasGateway(t, map[string]string{"chat": "deepseek-v4-flash"}, []string{"chat"})
	srv := httptest.NewServer(g)
	defer srv.Close()

	body := `{"model":"chat","messages":[{"role":"user","content":"hi"}]}`
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer sk-ok")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("白名单写别名应 403（校验真实名）, got %d", res.StatusCode)
	}
}

// TestModelsEndpoint 验证 GET /v1/models：按 vKey 白名单过滤别名；未配置别名时回退上游模型并集。
func TestModelsEndpoint(t *testing.T) {
	// 1) 配置了别名：白名单只含 qwen-turbo → 仅返回 cheap
	g, _ := newAliasGateway(t, map[string]string{"chat": "deepseek-v4-flash", "cheap": "qwen-turbo"}, []string{"qwen-turbo"})
	srv := httptest.NewServer(g)
	defer srv.Close()

	get := func(srvURL string) []string {
		req, _ := http.NewRequest(http.MethodGet, srvURL+"/v1/models", nil)
		req.Header.Set("Authorization", "Bearer sk-ok")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("GET /v1/models 应 200, got %d", res.StatusCode)
		}
		var out struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		ids := make([]string, 0, len(out.Data))
		for _, d := range out.Data {
			ids = append(ids, d.ID)
		}
		return ids
	}

	if ids := get(srv.URL); len(ids) != 1 || ids[0] != "cheap" {
		t.Fatalf("应按白名单过滤为 [cheap], got %v", ids)
	}

	// 2) 无别名配置：回退上游声明模型并集（白名单为空 → 全部）
	g2, _ := newAliasGateway(t, nil, nil)
	srv2 := httptest.NewServer(g2)
	defer srv2.Close()
	if ids := get(srv2.URL); len(ids) != 2 {
		t.Fatalf("无别名应返回上游模型并集 2 个, got %v", ids)
	}

	// 3) 未鉴权 → 401（auth_vkey 先拦截）
	req, _ := http.NewRequest(http.MethodGet, srv2.URL+"/v1/models", nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("无 vKey 应 401, got %d", res.StatusCode)
	}
}
