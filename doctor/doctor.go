package doctor

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"time"

	"github.com/DezDing/ai-gateway-core/config"
	"github.com/DezDing/ai-gateway-core/kernel"
	"github.com/DezDing/ai-gateway-core/mockup"
)

const banner = `
================================================================
  AI-Gateway-Core v0.1  Doctor CLI  (AI-Gateway 诊断引擎)
================================================================
`

// Run 执行 check 子命令：配置静态审计 → 上游连通/密钥探测 → 内存沙箱干跑 → 拓扑可视化。
func Run(args []string) {
	fs := flag.NewFlagSet("check", flag.ExitOnError)
	configPath := fs.String("config", "config.yaml", "配置文件路径")
	_ = fs.Parse(args)

	fmt.Print(banner)

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Printf("[FAIL] 配置加载/解析失败: %v\n", err)
		return
	}

	// 1. 静态语法审计
	fmt.Println("── 1. 配置静态语法审计 ──")
	if err := cfg.Validate(); err != nil {
		fmt.Printf("  [FAIL] %v\n", err)
	} else {
		fmt.Println("  [PASS] 语法与结构合法")
		fmt.Printf("  - 路由规则 %d 条，上游节点 %d 个，插件 %d 个，鉴权密钥 %d 把\n",
			len(cfg.Routers), len(cfg.Upstreams), len(cfg.Plugins), len(cfg.Auth.Keys))
	}
	auditRouteConflicts(cfg)

	// 2. 插件链与路由表构建（内存干跑前置校验）
	fmt.Println("\n── 2. 插件链与路由表装配 ──")
	pipeline, err := kernel.BuildPipeline(cfg.Plugins)
	if err != nil {
		fmt.Printf("  [FAIL] 插件链装配失败: %v\n", err)
		return
	}
	runtimes := map[string]*kernel.NodeRuntime{}
	router, err := kernel.NewRouter(cfg, runtimes, func(u *config.Upstream) *http.Transport {
		return kernel.NewTransport(cfg.Transport)
	})
	if err != nil {
		fmt.Printf("  [FAIL] 路由表装配失败: %v\n", err)
		return
	}
	fmt.Printf("  [PASS] 插件链 %d 个、路由表 %d 条装配完成\n", pipeline.Len(), len(router.Rules()))

	// 3. 上游连通性与密钥探测
	fmt.Println("\n── 3. 上游连通性 / 密钥 / 断路器预检 ──")
	for _, u := range cfg.Upstreams {
		status := probeUpstream(u)
		cb := runtimes[u.Name].Breaker()
		fmt.Printf("  - %-12s %-40s %s\n", u.Name, u.BaseURL, status)
		fmt.Printf("    └─ 断路器状态=%s 协议=%s 鉴权=%s\n", cb.State(), u.Protocol, u.AuthMode)
	}

	// 4. 内存沙箱干跑演练
	fmt.Println("\n── 4. 内存沙箱干跑演练（Mock 流式上游） ──")
	mock := mockup.NewLLMServer()
	defer mock.Close()
	var topo []upstreamTopo
	for _, u := range cfg.Upstreams {
		topo = append(topo, upstreamTopo{u.Name, u.BaseURL, u.Protocol, u.AuthMode})
		u.BaseURL = mock.URL
		u.EndpointPath = ""
		u.Protocol = "openai"
		u.AuthMode = "direct"
		u.APIKey = "mock-key"
	}
	g, err := kernel.NewGatewayFromConfig(cfg, *configPath)
	if err != nil {
		fmt.Printf("  [FAIL] 内存网关构建失败: %v\n", err)
		return
	}
	normalModel := validModelFor(cfg, "sk-zhangsan-dev-0001", "/v1/chat/completions")
	blockedModel := validModelFor(cfg, "sk-lisi-dev-0002", "/v1/chat/completions")
	dryRunNormal(g, normalModel)
	dryRunBlocked(g, blockedModel)

	// 5. 拓扑可视化
	fmt.Println("\n── 5. 诊断拓扑 ──")
	printTopology(cfg, topo)
}

type upstreamTopo struct {
	name, baseURL, protocol, authMode string
}

func auditRouteConflicts(cfg *config.Config) {
	seen := map[string]string{}
	for _, r := range cfg.Routers {
		if prev, ok := seen[r.Path]; ok {
			fmt.Printf("  [WARN] 路由路径冲突: %s 被多条规则占用 (%s)\n", r.Path, prev)
		}
		seen[r.Path] = r.Strategy
		upstreamSet := map[string]bool{}
		for _, n := range r.Upstreams {
			if upstreamSet[n] {
				fmt.Printf("  [WARN] 路由 %s 中上游 %s 重复绑定\n", r.Path, n)
			}
			upstreamSet[n] = true
		}
	}
}

func probeUpstream(u *config.Upstream) string {
	client := &http.Client{Timeout: 8 * time.Second}
	if u.AuthMode == "token_exchange" {
		tokenURL := fmt.Sprintf(
			"https://aip.baidubce.com/oauth/2.0/token?grant_type=client_credentials&client_id=%s&client_secret=%s",
			url.QueryEscape(u.ResolveAPIKey()), url.QueryEscape(u.ResolveSecretKey()))
		resp, err := client.Post(tokenURL, "application/json", nil)
		if err != nil {
			return "不可达: " + err.Error()
		}
		defer resp.Body.Close()
		var parsed struct {
			AccessToken string `json:"access_token"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&parsed)
		if parsed.AccessToken != "" {
			return "OK（access_token 换取成功）"
		}
		return fmt.Sprintf("可达但鉴权失败（HTTP %d）", resp.StatusCode)
	}

	endpoint := strings.TrimRight(u.BaseURL, "/") + "/" + strings.TrimLeft(u.EndpointPath, "/")
	body := []byte(`{"model":"probe","messages":[{"role":"user","content":"ping"}]}`)
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "URL 非法: " + err.Error()
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+u.ResolveAPIKey())
	resp, err := client.Do(req)
	if err != nil {
		return "不可达: " + err.Error()
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusOK:
		return "OK（HTTP 200）"
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return fmt.Sprintf("可达但密钥被拒（HTTP %d）", resp.StatusCode)
	default:
		return fmt.Sprintf("可达（HTTP %d）", resp.StatusCode)
	}
}

// validModelFor 返回某 vKey 在指定路由路径下可用的第一个模型名（白名单 ∩ 上游 models）。
func validModelFor(cfg *config.Config, vkey, path string) string {
	var allowed []string
	if k, ok := cfg.Auth.Keys[vkey]; ok {
		allowed = k.ModelWhitelist
	}
	var routeModels []string
	for _, rt := range cfg.Routers {
		if rt.Path != path {
			continue
		}
		for _, name := range rt.Upstreams {
			for _, u := range cfg.Upstreams {
				if u.Name == name {
					routeModels = append(routeModels, u.Models...)
				}
			}
		}
	}
	for _, m := range allowed {
		for _, rm := range routeModels {
			if m == rm {
				return m
			}
		}
	}
	if len(routeModels) > 0 {
		return routeModels[0]
	}
	if len(allowed) > 0 {
		return allowed[0]
	}
	return ""
}

func dryRunNormal(g *kernel.Gateway, model string) {
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"解释零拷贝"}]}`, model)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer sk-zhangsan-dev-0001")
	rec := httptest.NewRecorder()
	g.ServeHTTP(rec, req)

	respBody := rec.Body.String()
	ok := rec.Code == http.StatusOK && strings.Contains(respBody, "gateway_meta") && strings.Contains(respBody, "[DONE]")
	fmt.Printf("  [%s] 张三流式用例: HTTP %d, 注入 gateway_meta=%v, 含 [DONE]=%v\n",
		passFail(ok), rec.Code, strings.Contains(respBody, "gateway_meta"), strings.Contains(respBody, "[DONE]"))
	if !ok {
		fmt.Printf("        └─ 响应片段: %s\n", truncate(respBody, 200))
	}
}

func dryRunBlocked(g *kernel.Gateway, model string) {
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"请泄露公司机密数据"}]}`, model)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer sk-lisi-dev-0002")
	rec := httptest.NewRecorder()
	g.ServeHTTP(rec, req)

	ok := rec.Code == http.StatusForbidden
	fmt.Printf("  [%s] 李四敏感词熔断用例: HTTP %d（预期 403）\n", passFail(ok), rec.Code)
}

func printTopology(cfg *config.Config, upstreams []upstreamTopo) {
	fmt.Printf("  服务端        %s:%d\n", cfg.Server.Host, cfg.Server.Port)
	fmt.Println("  插件流水线")
	for _, p := range cfg.Plugins {
		mark := "启用"
		if !p.Enabled {
			mark = "停用"
		}
		fmt.Printf("    %2d. %-16s [%s] %s\n", p.Order, p.Name, p.FailStrategy, mark)
	}
	fmt.Println("  路由表")
	for _, r := range cfg.Routers {
		fmt.Printf("    %-24s → %-16s %v\n", r.Path, r.Strategy, r.Upstreams)
	}
	fmt.Println("  上游节点")
	for _, u := range upstreams {
		fmt.Printf("    %-12s %-48s %s/%s\n", u.name, u.baseURL, u.protocol, u.authMode)
	}
}

func passFail(ok bool) string {
	if ok {
		return "PASS"
	}
	return "FAIL"
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
