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

// buildKeyGateway 用自定义 auth key 构造完整网关（happy 上游）。
func buildKeyGateway(t *testing.T, key *config.AuthKey, backup *httptest.Server, logPath string) *kernel.Gateway {
	t.Helper()
	cfg := &config.Config{
		Server: config.Server{Host: "127.0.0.1", Port: 8080},
		Log:    config.Log{Path: logPath},
		Auth:   config.Auth{Keys: map[string]*config.AuthKey{key.VKey: key}},
		Plugins: []config.Plugin{
			{Name: "auth_vkey", Enabled: true, Order: 1},
			{Name: "prompt_injector", Enabled: true, Order: 2, FailStrategy: "bypass"},
			{Name: "dynamic_router", Enabled: true, Order: 3},
			{Name: "auditor", Enabled: true, Order: 4},
		},
		Upstreams: []*config.Upstream{{Name: "u", BaseURL: backup.URL, Protocol: "openai", AuthMode: "direct", APIKey: "k", Models: []string{"m1"}}},
		Routers: []config.Router{{
			Path: "/v1/chat/completions", Strategy: "round_robin", Upstreams: []string{"u"},
		}},
		Transport:   config.Transport{ConnectTimeoutSec: 5, TTFTTimeoutSec: 10, IdleReadTimeoutSec: 30},
		EdgeStorage: config.EdgeStorage{Enabled: logPath != "", MaxDiskMB: 1},
	}
	g, err := kernel.NewGatewayFromConfig(cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func doKeyRequest(t *testing.T, srv *httptest.Server, vkey string) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"m1","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+vkey)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res.StatusCode
}

func TestDisabledKey(t *testing.T) {
	kernel.SetAuditStdout(false)
	defer kernel.SetAuditStdout(true)
	backup, _ := countingBackup()
	defer backup.Close()

	key := &config.AuthKey{VKey: "sk-d", Username: "u", ModelWhitelist: []string{"m1"}, Disabled: true}
	g := buildKeyGateway(t, key, backup, "")
	defer g.Shutdown()
	srv := httptest.NewServer(g)
	defer srv.Close()

	if code := doKeyRequest(t, srv, "sk-d"); code != http.StatusForbidden {
		t.Fatalf("disabled 应 403, got %d", code)
	}
}

func TestRateLimit(t *testing.T) {
	kernel.SetAuditStdout(false)
	defer kernel.SetAuditStdout(true)
	backup, _ := countingBackup()
	defer backup.Close()

	key := &config.AuthKey{VKey: "sk-r", Username: "u", ModelWhitelist: []string{"m1"}, RateLimit: 1}
	g := buildKeyGateway(t, key, backup, "")
	defer g.Shutdown()
	srv := httptest.NewServer(g)
	defer srv.Close()

	if code := doKeyRequest(t, srv, "sk-r"); code != http.StatusOK {
		t.Fatalf("第 1 个应 200, got %d", code)
	}
	if code := doKeyRequest(t, srv, "sk-r"); code != http.StatusTooManyRequests {
		t.Fatalf("第 2 个应 429（限流）, got %d", code)
	}
}

func TestQuotaDaily(t *testing.T) {
	kernel.SetAuditStdout(false)
	defer kernel.SetAuditStdout(true)
	backup, _ := countingBackup()
	defer backup.Close()

	dir := t.TempDir()
	logPath := filepath.Join(dir, "uba.json")
	key := &config.AuthKey{VKey: "sk-q", Username: "u", ModelWhitelist: []string{"m1"}, QuotaDaily: 1}
	g := buildKeyGateway(t, key, backup, logPath)
	defer g.Shutdown()
	srv := httptest.NewServer(g)
	defer srv.Close()

	if code := doKeyRequest(t, srv, "sk-q"); code != http.StatusOK {
		t.Fatalf("第 1 个应 200, got %d", code)
	}

	// 轮询配额文件，等待异步审计累加已用额度
	quotaPath := filepath.Join(dir, "quota.json")
	deadline := time.Now().Add(2 * time.Second)
	consumed := false
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(quotaPath); err == nil && strings.Contains(string(data), `"sk-q"`) {
			consumed = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !consumed {
		t.Fatal("配额文件未记录已用额度")
	}

	if code := doKeyRequest(t, srv, "sk-q"); code != http.StatusTooManyRequests {
		t.Fatalf("配额用尽后应 429, got %d", code)
	}
}
