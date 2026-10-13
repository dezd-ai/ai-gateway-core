package plugins

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DezDing/ai-gateway-core/config"
	"github.com/DezDing/ai-gateway-core/kernel"
)

// TestAbortRequestAudit 验证 backlog #4：被 PreRequest 拦截的 401/403 落审计，
// 记录携带 http_status / success=false / abort_reason。
func TestAbortRequestAudit(t *testing.T) {
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
			{Name: "prompt_injector", Enabled: true, Order: 2, FailStrategy: "bypass"},
			{Name: "security_guard", Enabled: true, Order: 3},
			{Name: "dynamic_router", Enabled: true, Order: 4},
			{Name: "auditor", Enabled: true, Order: 5},
		},
		Security:  config.Security{SensitiveWords: []string{"机密"}},
		Upstreams: []*config.Upstream{{Name: "u", BaseURL: "http://127.0.0.1:1", Protocol: "openai", AuthMode: "direct", APIKey: "k", Models: []string{"m1"}}},
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

	do := func(vkey, body string) int {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+vkey)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}

	if code := do("sk-wrong", `{"model":"m1","messages":[{"role":"user","content":"hi"}]}`); code != http.StatusUnauthorized {
		t.Fatalf("401 期望, got %d", code)
	}
	if code := do("sk-ok", `{"model":"m1","messages":[{"role":"user","content":"这里有机密数据"}]}`); code != http.StatusForbidden {
		t.Fatalf("403 期望, got %d", code)
	}

	// 轮询审计文件，等待两条拦截记录
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(logPath)
		if err == nil {
			s := string(data)
			if strings.Contains(s, `"http_status":401`) && strings.Contains(s, `"invalid_api_key"`) &&
				strings.Contains(s, `"http_status":403`) && strings.Contains(s, `"content_filter"`) {
				if !strings.Contains(s, `"success":false`) {
					t.Fatalf("拦截记录应标记 success=false, log=%s", s)
				}
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("审计文件未出现 401/403 拦截记录")
}

// TestRequestBodyTooLarge413 验证内存防线 层 1：Content-Length 超限直接 413 且落审计（backlog #7）。
func TestRequestBodyTooLarge413(t *testing.T) {
	kernel.SetAuditStdout(false)
	defer kernel.SetAuditStdout(true)

	logPath := filepath.Join(t.TempDir(), "uba.json")
	cfg := &config.Config{
		Server: config.Server{Host: "127.0.0.1", Port: 8080},
		Log:    config.Log{Path: logPath},
		Auth:   config.Auth{Keys: map[string]*config.AuthKey{"sk-ok": {VKey: "sk-ok"}}},
		Plugins: []config.Plugin{
			{Name: "auth_vkey", Enabled: true, Order: 1},
		},
		Upstreams: []*config.Upstream{{Name: "u", BaseURL: "http://127.0.0.1:1", Protocol: "openai", AuthMode: "direct", APIKey: "k", Models: []string{"m1"}}},
		Routers: []config.Router{{
			Path: "/v1/chat/completions", Strategy: "round_robin", Upstreams: []string{"u"},
		}},
		Transport:   config.Transport{ConnectTimeoutSec: 5, TTFTTimeoutSec: 10, IdleReadTimeoutSec: 30, MaxRequestBodyBytes: 100},
		EdgeStorage: config.EdgeStorage{Enabled: true, MaxDiskMB: 1},
	}

	g, err := kernel.NewGatewayFromConfig(cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	defer g.Shutdown()
	srv := httptest.NewServer(g)
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions", strings.NewReader(strings.Repeat("x", 200)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer sk-ok")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("超限应 413, got %d", res.StatusCode)
	}

	// 审计文件应含 413 拦截记录
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(logPath)
		if err == nil && strings.Contains(string(data), `"http_status":413`) &&
			strings.Contains(string(data), `"request_too_large"`) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("审计文件未出现 413 拦截记录")
}
