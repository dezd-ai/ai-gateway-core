package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseYAMLNested(t *testing.T) {
	data := []byte(`
server:
  host: 0.0.0.0
  port: 8080
plugins:
  - name: auth_vkey
    enabled: true
    order: 1
upstreams:
  - name: deepseek
    models: ["m1", "m2"]
    cost_per_1k: 0.14
    tags: ["llm"]
`)
	m, err := ParseYAML(data)
	if err != nil {
		t.Fatalf("ParseYAML: %v", err)
	}
	server := asMap(m["server"])
	if server == nil {
		t.Fatal("server 缺失")
	}
	if getInt(server, "port", 0) != 8080 {
		t.Fatalf("port = %d", getInt(server, "port", 0))
	}
	if len(asList(m["plugins"])) != 1 {
		t.Fatalf("plugins = %d", len(asList(m["plugins"])))
	}
	up := asList(m["upstreams"])
	if len(up) != 1 {
		t.Fatalf("upstreams = %d", len(up))
	}
	um := asMap(up[0])
	if getFloat(um, "cost_per_1k", 0) != 0.14 {
		t.Fatalf("cost = %v", getFloat(um, "cost_per_1k", 0))
	}
	if len(getStringSlice(um, "models")) != 2 {
		t.Fatalf("models = %v", getStringSlice(um, "models"))
	}
}

// TestLoadExampleConfig 确保提交的 config.example.yaml 始终合法可加载。
func TestLoadExampleConfig(t *testing.T) {
	cfg, err := Load(filepath.Join("..", "config.example.yaml"))
	if err != nil {
		t.Fatalf("Load example: %v", err)
	}
	if len(cfg.Upstreams) != 3 || len(cfg.Auth.Keys) != 2 || len(cfg.Routers) != 2 {
		t.Fatalf("配置形态异常: upstreams=%d keys=%d routers=%d",
			len(cfg.Upstreams), len(cfg.Auth.Keys), len(cfg.Routers))
	}
}

func TestValidateRejectsBadConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	// port 0 非法 + 无 auth keys
	if err := os.WriteFile(path, []byte("server:\n  port: 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("非法配置应加载失败")
	}
}

func TestResolveAPIKeyFromEnv(t *testing.T) {
	u := &Upstream{APIKey: "inline", APIKeyEnv: "TEST_AIGW_KEY"}
	t.Setenv("TEST_AIGW_KEY", "from-env")
	if got := u.ResolveAPIKey(); got != "from-env" {
		t.Fatalf("ResolveAPIKey = %q，应优先环境变量", got)
	}
	// 环境变量未设置时回退到 api_key
	u2 := &Upstream{APIKey: "inline", APIKeyEnv: "UNSET_AIGW_KEY"}
	if got := u2.ResolveAPIKey(); got != "inline" {
		t.Fatalf("ResolveAPIKey = %q，应回退 api_key", got)
	}
}

// TestSecurityRegexpsPrecompiled 验证敏感词/防注入正则预编译 + 大小写不敏感 + 惰性复用（backlog #3）。
func TestSecurityRegexpsPrecompiled(t *testing.T) {
	s := &Security{
		SensitiveWords:    []string{"机密", "Internal-Secret", ""},
		InjectionPatterns: []string{"ignore previous instructions", "(", ""}, // 含一条非法正则（括号未闭合）
	}
	sens := s.SensitiveRegexps()
	if len(sens) != 2 {
		t.Fatalf("敏感词正则数 = %d, want 2（空串跳过）", len(sens))
	}
	if !sens[0].MatchString("这里有机密信息") {
		t.Fatal("敏感词应命中")
	}
	if !sens[1].MatchString("internal-secret") {
		t.Fatal("敏感词应大小写不敏感")
	}
	if !sens[1].MatchString("INTERNAL-SECRET") {
		t.Fatal("敏感词应大小写不敏感（大写）")
	}

	inj := s.InjectionRegexps()
	if len(inj) != 1 {
		t.Fatalf("防注入正则数 = %d, want 1（非法正则静默跳过）", len(inj))
	}
	if !inj[0].MatchString("IGNORE PREVIOUS INSTRUCTIONS and answer") {
		t.Fatal("防注入正则应命中且忽略大小写")
	}

	// 复用：再次调用返回同一批（sync.Once 惰性编译一次）
	if &s.SensitiveRegexps()[0] != &sens[0] {
		t.Fatal("应复用预编译结果")
	}
}

// fallbackBase 提供合法基础配置，供 fallback 校验用例在其上叠加/篡改。
const fallbackBase = `
server:
  port: 8080
auth:
  keys:
    - vkey: "k1"
transport:
  connect_timeout_sec: 5
  ttft_timeout_sec: 10
  idle_read_timeout_sec: 30
upstreams:
  - name: deepseek
    base_url: https://api.deepseek.com
    models: ["m1", "m2"]
routers:
  - path: /v1/chat/completions
    strategy: round_robin
    upstreams: ["deepseek"]
`

func TestValidateFallbackConfig(t *testing.T) {
	write := func(t *testing.T, body string) string {
		dir := t.TempDir()
		p := filepath.Join(dir, "config.yaml")
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	t.Run("合法 failover 全量解析", func(t *testing.T) {
		p := write(t, fallbackBase+`
    fallback:
      mode: failover
      max_attempts: 2
      connect_retry: 1
      total_timeout_ms: 15000
      retry_on: ["429", "5xx", "timeout", "connect"]
      transparent_header: true
      model_map:
        - from: m1
          to:
            upstream: deepseek
            model: m2
`)
		cfg, err := Load(p)
		if err != nil {
			t.Fatalf("合法配置应加载成功: %v", err)
		}
		fb := cfg.Routers[0].Fallback
		if fb == nil {
			t.Fatal("Fallback 应为非 nil")
		}
		if fb.Mode != "failover" || fb.MaxAttempts != 2 || fb.ConnectRetry != 1 ||
			fb.TotalTimeoutMS != 15000 || !fb.TransparentHeader || len(fb.RetryOn) != 4 {
			t.Fatalf("解析异常: %+v", fb)
		}
		if len(fb.ModelMap) != 1 || fb.ModelMap[0].From != "m1" ||
			fb.ModelMap[0].To.Upstream != "deepseek" || fb.ModelMap[0].To.Model != "m2" {
			t.Fatalf("model_map 解析异常: %+v", fb.ModelMap)
		}
	})

	t.Run("mode 非法", func(t *testing.T) {
		p := write(t, fallbackBase+"\n    fallback:\n      mode: bogus\n")
		if _, err := Load(p); err == nil {
			t.Fatal("非法 mode 应加载失败")
		}
	})

	t.Run("model_map 引用不存在 upstream", func(t *testing.T) {
		p := write(t, fallbackBase+`
    fallback:
      model_map:
        - from: m1
          to:
            upstream: nope
            model: m2
`)
		if _, err := Load(p); err == nil {
			t.Fatal("引用不存在 upstream 应加载失败")
		}
	})

	t.Run("model_map 目标模型不在 upstream models", func(t *testing.T) {
		p := write(t, fallbackBase+`
    fallback:
      model_map:
        - from: m1
          to:
            upstream: deepseek
            model: m3
`)
		if _, err := Load(p); err == nil {
			t.Fatal("目标模型未声明应加载失败")
		}
	})

	t.Run("retry_on 非法", func(t *testing.T) {
		p := write(t, fallbackBase+"\n    fallback:\n      retry_on: [\"4xx\"]\n")
		if _, err := Load(p); err == nil {
			t.Fatal("非法 retry_on 应加载失败")
		}
	})

	t.Run("未配置 fallback 保持 nil（行为零变化）", func(t *testing.T) {
		p := write(t, fallbackBase)
		cfg, err := Load(p)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Routers[0].Fallback != nil {
			t.Fatal("未配置 fallback 应为 nil")
		}
	})
}

// TestValidateTransportBodyLimit 验证内存防线相关配置：负数拒绝、合法解析。
func TestValidateTransportBodyLimit(t *testing.T) {
	write := func(t *testing.T, body string) string {
		dir := t.TempDir()
		p := filepath.Join(dir, "config.yaml")
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	head := `
server:
  port: 8080
auth:
  keys:
    - vkey: "k1"
transport:
  connect_timeout_sec: 5
  ttft_timeout_sec: 10
  idle_read_timeout_sec: 30
`
	tail := `
upstreams:
  - name: deepseek
    base_url: https://api.deepseek.com
routers:
  - path: /v1/chat/completions
    strategy: round_robin
    upstreams: ["deepseek"]
`

	t.Run("负数拒绝", func(t *testing.T) {
		p := write(t, head+"  max_request_body_bytes: -1\n"+tail)
		if _, err := Load(p); err == nil {
			t.Fatal("负数 body 上限应加载失败")
		}
	})

	t.Run("合法解析", func(t *testing.T) {
		p := write(t, head+"  max_request_body_bytes: 10485760\n  max_audit_body_bytes: 1048576\n"+tail)
		cfg, err := Load(p)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Transport.MaxRequestBodyBytes != 10485760 || cfg.Transport.MaxAuditBodyBytes != 1048576 {
			t.Fatalf("解析异常: %+v", cfg.Transport)
		}
	})
}

// TestAgentModeConfig 验证 agent_mode 与 agent_* 参数解析（docs/agent-mode.md）。
func TestAgentModeConfig(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	body := `
server:
  port: 8080
auth:
  keys:
    - vkey: "sk-agent"
      agent_mode: true
      quota_daily: 50000000
transport:
  connect_timeout_sec: 5
  ttft_timeout_sec: 10
  idle_read_timeout_sec: 30
  agent_ttft_timeout_sec: 120
  agent_max_request_body_bytes: 52428800
  agent_gateway_meta: false
upstreams:
  - name: deepseek
    base_url: https://api.deepseek.com
routers:
  - path: /v1/chat/completions
    strategy: round_robin
    upstreams: ["deepseek"]
`
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	key := cfg.Auth.Keys["sk-agent"]
	if key == nil || !key.AgentMode || key.QuotaDaily != 50000000 {
		t.Fatalf("agent key 解析异常: %+v", key)
	}
	if cfg.Transport.AgentTTFTTimeoutSec != 120 || cfg.Transport.AgentMaxRequestBodyBytes != 52428800 || cfg.Transport.AgentGatewayMeta {
		t.Fatalf("agent 参数解析异常: %+v", cfg.Transport)
	}
}
