package plugins

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dezd-ai/ai-gateway-core/config"
	"github.com/dezd-ai/ai-gateway-core/kernel"
	"github.com/dezd-ai/ai-gateway-core/mockup"
)

// buildFaultGateway 构造带 failover fallback 的完整网关（auth + injector + router + auditor）。
// primaryModels/backupModels 控制两上游声明的模型（决定同模型候选链长度）；
// breakerThreshold 为两上游断路器失败阈值（0 = 默认 5）。
func buildFaultGateway(t *testing.T, primary, backup string, fb config.Fallback,
	primaryModels, backupModels []string, breakerThreshold int, logPath string) *kernel.Gateway {

	t.Helper()
	if breakerThreshold <= 0 {
		breakerThreshold = 5
	}
	cb := config.CircuitBreakerConfig{FailureThreshold: breakerThreshold, OpenTimeoutSec: 30, HalfOpenMax: 3}
	cfg := &config.Config{
		Server: config.Server{Host: "127.0.0.1", Port: 8080},
		Log:    config.Log{Path: logPath}, // 审计落盘路径（空 = 环境默认）
		Auth:   config.Auth{Keys: map[string]*config.AuthKey{"sk-test": {VKey: "sk-test", Username: "u", Department: "d", ModelWhitelist: []string{"m1", "m2"}}}},
		Plugins: []config.Plugin{
			{Name: "auth_vkey", Enabled: true, Order: 1},
			{Name: "prompt_injector", Enabled: true, Order: 2, FailStrategy: "bypass"},
			{Name: "dynamic_router", Enabled: true, Order: 3},
			{Name: "auditor", Enabled: true, Order: 4},
		},
		Upstreams: []*config.Upstream{
			{Name: "primary", BaseURL: primary, Protocol: "openai", AuthMode: "direct", APIKey: "k", Models: primaryModels, Circuit: cb},
			{Name: "backup", BaseURL: backup, Protocol: "openai", AuthMode: "direct", APIKey: "k", Models: backupModels, Circuit: cb},
		},
		Routers: []config.Router{{
			Path: "/v1/chat/completions", Strategy: "round_robin", Upstreams: []string{"primary", "backup"},
			Fallback: &fb,
		}},
		Transport:   config.Transport{ConnectTimeoutSec: 2, TTFTTimeoutSec: 1, IdleReadTimeoutSec: 30},
		EdgeStorage: config.EdgeStorage{Enabled: logPath != "", MaxDiskMB: 1, Strategy: "rotate"},
	}
	g, err := kernel.NewGatewayFromConfig(cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// doFaultRequest 走完整网关链路发一次对话请求（model=m1）。
func doFaultRequest(t *testing.T, g *kernel.Gateway) (*http.Response, string) {
	t.Helper()
	srv := httptest.NewServer(g)
	defer srv.Close()

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"m1","messages":[{"role":"user","content":"hi"}]}`))
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
	return res, string(body)
}

// countingBackup 返回可统计命中次数的正常 SSE 上游。
func countingBackup() (*httptest.Server, *atomic.Int64) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
	}))
	return srv, &hits
}

// TestFallbackFaultMatrix 故障注入矩阵：首字节前的各类失败都应切到 backup 成功。
func TestFallbackFaultMatrix(t *testing.T) {
	backup, hitsB := countingBackup()
	defer backup.Close()

	cases := []struct {
		name     string
		scenario mockup.FaultScenario
	}{
		{"5xx 服务端错误切换", mockup.Fault500},
		{"429 限流切换", mockup.Fault429},
		{"连接重置切换", mockup.FaultReset},
		{"TTFT 超时切换", mockup.FaultSlow},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			primary := mockup.NewFaultServer(c.scenario)
			defer primary.Close()
			hitsB.Store(0)

			fb := config.Fallback{Mode: "failover", TransparentHeader: true}
			g := buildFaultGateway(t, primary.URL, backup.URL, fb,
				[]string{"m1", "m2"}, []string{"m1", "m2"}, 0, "")
			defer g.Shutdown()

			res, body := doFaultRequest(t, g)
			if res.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200（应切到 backup）; body=%s", res.StatusCode, body)
			}
			if hitsB.Load() != 1 {
				t.Fatalf("backup 应被调用 1 次, got %d", hitsB.Load())
			}
			if got := res.Header.Get("X-AI-Gateway-Fallback"); got != "primary→backup" {
				t.Fatalf("透明头 = %q, want primary→backup", got)
			}
			if got := res.Header.Get("X-AI-Gateway-Attempts"); got != "2" {
				t.Fatalf("Attempts 头 = %q, want 2", got)
			}
			_ = body
		})
	}
}

// TestFallbackMidStreamNoSwitch 验证 mid-stream（首字节后）断流不切换：
// backup 不被调用，primary 断路器熔断（阈值 1），客户端收到补齐的截断流。
func TestFallbackMidStreamNoSwitch(t *testing.T) {
	primary := mockup.NewFaultServer(mockup.FaultTruncate)
	defer primary.Close()
	backup, hitsB := countingBackup()
	defer backup.Close()

	g := buildFaultGateway(t, primary.URL, backup.URL, config.Fallback{Mode: "failover"},
		[]string{"m1", "m2"}, []string{"m1", "m2"}, 1, "")
	defer g.Shutdown()

	res, body := doFaultRequest(t, g)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("mid-stream 场景 HTTP 仍为 200, got %d", res.StatusCode)
	}
	if !strings.Contains(body, "stream_truncated") || !strings.Contains(body, "[DONE]") {
		t.Fatalf("应补齐截断错误 chunk + [DONE], body=%s", body)
	}
	if hitsB.Load() != 0 {
		t.Fatal("mid-stream 断流不应切换上游")
	}
	if state := g.Router().Nodes()["primary"].Runtime.Breaker().State(); state != "open" {
		t.Fatalf("断流应使 primary 熔断（阈值 1）, state=%s", state)
	}
}

// TestFallbackAuditFields 验证审计记录携带 fallback 字段（fallback_chain/attempts/chain_truncated/stop_reason）。
func TestFallbackAuditFields(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "uba.json")
	primary := mockup.NewFaultServer(mockup.Fault500)
	defer primary.Close()
	backup, _ := countingBackup()
	defer backup.Close()

	g := buildFaultGateway(t, primary.URL, backup.URL, config.Fallback{Mode: "failover"},
		[]string{"m1", "m2"}, []string{"m1", "m2"}, 0, logPath)
	defer g.Shutdown()

	doFaultRequest(t, g)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(logPath)
		if err == nil {
			s := string(data)
			if strings.Contains(s, `"fallback_chain"`) {
				if !strings.Contains(s, `"primary"`) || !strings.Contains(s, `"backup"`) {
					t.Fatalf("fallback_chain 应含 primary/backup, log=%s", s)
				}
				if !strings.Contains(s, `"attempts":2`) {
					t.Fatalf("attempts 应为 2, log=%s", s)
				}
				if !strings.Contains(s, `"chain_truncated":false`) {
					t.Fatalf("chain_truncated 应为 false, log=%s", s)
				}
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("审计记录未出现 fallback 字段")
}
