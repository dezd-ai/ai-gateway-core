package plugins

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dezd-ai/ai-gateway-core/config"
	"github.com/dezd-ai/ai-gateway-core/kernel"
)

// TestProxyPhaseErrorAudit 验证 backlog #9：代理阶段错误也落审计。
// 覆盖：404 无路由（no_route）、502 上游不可达（proxy_error）、503 全上游熔断（circuit_open），
// 记录携带 http_status / success=false / abort_reason。
func TestProxyPhaseErrorAudit(t *testing.T) {
	kernel.SetAuditStdout(false) // 隔离测试噪声
	defer kernel.SetAuditStdout(true)

	logPath := filepath.Join(t.TempDir(), "uba.json")
	cfg := &config.Config{
		Server: config.Server{Host: "127.0.0.1", Port: 8080},
		Log:    config.Log{Path: logPath},
		Auth: config.Auth{Keys: map[string]*config.AuthKey{
			"sk-ok": {VKey: "sk-ok", Username: "u", Department: "d", ModelWhitelist: []string{"m1"}},
		}},
		Plugins: []config.Plugin{
			{Name: "auth_vkey", Enabled: true, Order: 1},
			{Name: "dynamic_router", Enabled: true, Order: 4},
			{Name: "auditor", Enabled: true, Order: 5},
		},
		Upstreams: []*config.Upstream{{
			Name:     "u",
			BaseURL:  "http://127.0.0.1:1", // 关闭的端口：连接必失败
			Protocol: "openai",
			AuthMode: "direct",
			APIKey:   "k",
			Models:   []string{"m1"},
			Circuit:  config.CircuitBreakerConfig{FailureThreshold: 1, OpenTimeoutSec: 30, HalfOpenMax: 1},
		}},
		Routers: []config.Router{{
			Path: "/v1/chat/completions", Strategy: "round_robin", Upstreams: []string{"u"},
		}},
		Transport:   config.Transport{ConnectTimeoutSec: 5, TTFTTimeoutSec: 10, IdleReadTimeoutSec: 30},
		EdgeStorage: config.EdgeStorage{Enabled: true, MaxDiskMB: 1},
	}

	g, err := kernel.NewGatewayFromConfig(cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	defer g.Shutdown()
	srv := httptest.NewServer(g)
	defer srv.Close()

	do := func(path, body string) int {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer sk-ok")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}

	// 1) 未知路径 → 404 no_route
	if code := do("/v1/unknown", `{"model":"m1"}`); code != http.StatusNotFound {
		t.Fatalf("无路由应 404, got %d", code)
	}
	// 2) 上游不可达 → 502 proxy_error
	if code := do("/v1/chat/completions", `{"model":"m1","messages":[{"role":"user","content":"hi"}]}`); code != http.StatusBadGateway {
		t.Fatalf("上游失败应 502, got %d", code)
	}
	// 3) 熔断阈值 1：同上游再次请求 → 503 circuit_open
	if code := do("/v1/chat/completions", `{"model":"m1","messages":[{"role":"user","content":"hi"}]}`); code != http.StatusServiceUnavailable {
		t.Fatalf("全上游熔断应 503, got %d", code)
	}

	// 轮询审计文件，等待三条代理阶段错误记录
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(logPath)
		if err == nil {
			s := string(data)
			if strings.Contains(s, `"http_status":404`) && strings.Contains(s, `"no_route"`) &&
				strings.Contains(s, `"http_status":502`) && strings.Contains(s, `"proxy_error"`) &&
				strings.Contains(s, `"http_status":503`) && strings.Contains(s, `"circuit_open"`) {
				if !strings.Contains(s, `"success":false`) {
					t.Fatalf("代理阶段错误记录应标记 success=false, log=%s", s)
				}
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("审计文件未出现 404/502/503 代理阶段错误记录")
}
