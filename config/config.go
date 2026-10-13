package config

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Config 是网关的完整声明式运行时快照（GitOps 单一事实来源）。
type Config struct {
	Server      Server
	Log         Log
	Auth        Auth
	Plugins     []Plugin
	Upstreams   []*Upstream
	Routers     []Router
	Security    Security
	Injector    Injector
	Transport   Transport
	EdgeStorage EdgeStorage
	Watchdog    Watchdog
	Capacity    Capacity
}

type Server struct {
	Host string
	Port int
}

type Log struct {
	Level string
	Path  string
}

type AuthKey struct {
	VKey                string
	Username            string
	Department          string
	ModelWhitelist      []string
	Tags                []string
	HardwareFingerprint string
	// vKey 生命周期与限流（roadmap R1，设计见 docs/backlog.md 1.6）
	RateLimit  int   // QPS 限流（每秒请求数），0 = 不限
	QuotaDaily int64 // 每日 token 配额，0 = 不限
	Disabled   bool  // 吊销：true = 禁用该 key（无需删条目，热加载生效）
	AgentMode  bool  // agent 宽松模式（docs/agent-mode.md）：治理保留、干预为零
}

type Auth struct {
	HALEnabled bool
	Keys       map[string]*AuthKey
}

type Plugin struct {
	Name         string
	Enabled      bool
	Order        int
	FailStrategy string // abort | bypass
	Config       map[string]any
}

type CircuitBreakerConfig struct {
	FailureThreshold int
	OpenTimeoutSec   int
	HalfOpenMax      int
}

type Upstream struct {
	Name         string
	EndpointPath string
	BaseURL      string
	APIKey       string
	APIKeyEnv    string
	SecretKey    string
	SecretKeyEnv string
	Protocol     string // openai | baidu_wenxin
	AuthMode     string // direct | token_exchange
	Models       []string
	Weight       int
	CostPer1K    float64
	Tags         []string
	Circuit      CircuitBreakerConfig
}

func (u *Upstream) ResolveAPIKey() string {
	if u.APIKeyEnv != "" {
		if v := os.Getenv(u.APIKeyEnv); v != "" {
			return v
		}
	}
	return u.APIKey
}

func (u *Upstream) ResolveSecretKey() string {
	if u.SecretKeyEnv != "" {
		if v := os.Getenv(u.SecretKeyEnv); v != "" {
			return v
		}
	}
	return u.SecretKey
}

type Router struct {
	Path         string
	Strategy     string
	Upstreams    []string
	DefaultModel string
	DomainTags   []string
	Fallback     *Fallback // 请求级失败转移配置（nil = 不启用，保持现状）
}

// Fallback 是路由级失败转移配置（设计见 docs/fallback-design.md）。
// 注意：手写 YAML 解析器不支持行内 flow map，model_map 的 to 必须用块式写法。
type Fallback struct {
	Mode              string          // fail_fast | failover（默认 fail_fast）
	MaxAttempts       int             // 整条链尝试上限（含首次）；0 = 默认候选链长
	TotalTimeoutMS    int             // 整条链总预算（毫秒）；0 = 不限制
	ConnectRetry      int             // 仅连接级失败的同节点重试次数（默认 0）
	RetryOn           []string        // 可重试失败类型：429 | 5xx | timeout | connect（4xx 永不重试）
	TransparentHeader bool            // failover 下附加 X-AI-Gateway-Fallback 头
	ModelMap          []ModelMapEntry // 跨供应商模型降级映射（显式声明）
}

type ModelMapEntry struct {
	From string
	To   ModelMapTarget
}

type ModelMapTarget struct {
	Upstream string
	Model    string
}

type Security struct {
	SensitiveWords    []string
	InjectionPatterns []string
	BlockResponse     map[string]any

	// 预编译正则（懒加载 + sync.Once，见 SensitiveRegexps/InjectionRegexps），
	// 避免安全插件在每次请求时重复 regexp.Compile（backlog #3）。
	sensitiveOnce sync.Once
	injectionOnce sync.Once
	sensitiveRes  []*regexp.Regexp
	injectionRes  []*regexp.Regexp
}

// SensitiveRegexps 返回敏感词的预编译正则（大小写不敏感、整词字面匹配），惰性编译一次。
func (s *Security) SensitiveRegexps() []*regexp.Regexp {
	s.sensitiveOnce.Do(func() {
		for _, w := range s.SensitiveWords {
			if w == "" {
				continue
			}
			s.sensitiveRes = append(s.sensitiveRes, regexp.MustCompile("(?i)"+regexp.QuoteMeta(w)))
		}
	})
	return s.sensitiveRes
}

// InjectionRegexps 返回防注入正则的预编译结果（大小写不敏感），非法正则静默跳过。
func (s *Security) InjectionRegexps() []*regexp.Regexp {
	s.injectionOnce.Do(func() {
		for _, p := range s.InjectionPatterns {
			if p == "" {
				continue
			}
			if re, err := regexp.Compile("(?i)" + p); err == nil {
				s.injectionRes = append(s.injectionRes, re)
			}
		}
	})
	return s.injectionRes
}

type InjectTemplate struct {
	Head string
	Tail string
}

type Injector struct {
	Templates map[string]InjectTemplate
}

type Transport struct {
	ConnectTimeoutSec  int
	TTFTTimeoutSec     int
	IdleReadTimeoutSec int
	MaxIdleConns       int
	MaxIdlePerHost     int
	// 内存防线（见 docs/backlog.md「1.5 内存防线」）：0 = 不限制
	MaxRequestBodyBytes int64 // 请求体上限（层 1）
	MaxAuditBodyBytes   int64 // 非流式响应审计缓冲上限（层 2），超限放弃解析 usage 降级 estimated
	// Agent 宽松模式参数（docs/agent-mode.md）
	AgentTTFTTimeoutSec      int   // agent 首字超时放大（秒），0 = 沿用 TTFTTimeoutSec
	AgentMaxRequestBodyBytes int64 // agent 请求体上限，0 = 沿用全局（非"不限"）
	AgentGatewayMeta         bool  // agent 模式是否注入 gateway_meta（默认 false = 不注入）
}

type EdgeStorage struct {
	Enabled   bool
	MaxDiskMB int64
	Strategy  string
	ReportURL string
}

type Watchdog struct {
	Enabled     bool
	Device      string
	IntervalSec int
}

// Capacity 是网关容量/内存防线配置（docs/backlog.md「1.5 内存防线」层 3/4）。
// 全 0 = 自动推导（启动时按系统内存/cgroup 计算），显式值优先。
type Capacity struct {
	MaxInflight       int // 并发准入上限，0 = 自动（每 5MB 内存 1 并发，clamp 16~4096）
	MemSoftPercent    int // 内存软阈值百分比，0 = 默认 80（触发降并发）
	MemHardPercent    int // 内存硬阈值百分比，0 = 默认 90（触发拒绝+GC）
	HysteresisSamples int // 连续 N 次采样超阈值才触发/恢复，0 = 默认 3（防抖动）
}

// Load 从路径解析并校验 config.yaml，返回类型化 Config。
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取配置失败: %w", err)
	}
	tree, err := ParseYAML(raw)
	if err != nil {
		return nil, fmt.Errorf("解析配置失败: %w", err)
	}
	cfg := buildConfig(tree)
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("配置校验失败: %w", err)
	}
	return cfg, nil
}

func buildConfig(root map[string]any) *Config {
	cfg := &Config{}

	if m := asMap(root["server"]); m != nil {
		cfg.Server.Host = getString(m, "host", "0.0.0.0")
		cfg.Server.Port = getInt(m, "port", 8080)
	}
	if m := asMap(root["log"]); m != nil {
		cfg.Log.Level = getString(m, "level", "info")
		cfg.Log.Path = getString(m, "path", "")
	}
	if m := asMap(root["auth"]); m != nil {
		cfg.Auth.HALEnabled = getBool(m, "hal_enabled", false)
		cfg.Auth.Keys = map[string]*AuthKey{}
		for _, item := range asList(m["keys"]) {
			km := asMap(item)
			if km == nil {
				continue
			}
			key := &AuthKey{
				VKey:                getString(km, "vkey", ""),
				Username:            getString(km, "username", ""),
				Department:          getString(km, "department", ""),
				ModelWhitelist:      getStringSlice(km, "model_whitelist"),
				Tags:                getStringSlice(km, "tags"),
				HardwareFingerprint: getString(km, "hardware_fingerprint", ""),
				RateLimit:           getInt(km, "rate_limit", 0),
				QuotaDaily:          getInt64(km, "quota_daily", 0),
				Disabled:            getBool(km, "disabled", false),
				AgentMode:           getBool(km, "agent_mode", false),
			}
			if key.VKey != "" {
				cfg.Auth.Keys[key.VKey] = key
			}
		}
	}

	for _, item := range asList(root["plugins"]) {
		pm := asMap(item)
		if pm == nil {
			continue
		}
		cfg.Plugins = append(cfg.Plugins, Plugin{
			Name:         getString(pm, "name", ""),
			Enabled:      getBool(pm, "enabled", true),
			Order:        getInt(pm, "order", 0),
			FailStrategy: getString(pm, "fail_strategy", "abort"),
			Config:       asMap(pm["config"]),
		})
	}

	for _, item := range asList(root["upstreams"]) {
		um := asMap(item)
		if um == nil {
			continue
		}
		u := &Upstream{
			Name:         getString(um, "name", ""),
			EndpointPath: getString(um, "endpoint_path", ""),
			BaseURL:      getString(um, "base_url", ""),
			APIKey:       getString(um, "api_key", ""),
			APIKeyEnv:    getString(um, "api_key_env", ""),
			SecretKey:    getString(um, "secret_key", ""),
			SecretKeyEnv: getString(um, "secret_key_env", ""),
			Protocol:     getString(um, "protocol", "openai"),
			AuthMode:     getString(um, "auth_mode", "direct"),
			Models:       getStringSlice(um, "models"),
			Weight:       getInt(um, "weight", 1),
			CostPer1K:    getFloat(um, "cost_per_1k", 0),
			Tags:         getStringSlice(um, "tags"),
		}
		if cm := asMap(um["circuit"]); cm != nil {
			u.Circuit.FailureThreshold = getInt(cm, "failure_threshold", 5)
			u.Circuit.OpenTimeoutSec = getInt(cm, "open_timeout_sec", 30)
			u.Circuit.HalfOpenMax = getInt(cm, "half_open_max", 3)
		}
		if u.Weight <= 0 {
			u.Weight = 1
		}
		cfg.Upstreams = append(cfg.Upstreams, u)
	}

	for _, item := range asList(root["routers"]) {
		rm := asMap(item)
		if rm == nil {
			continue
		}
		r := Router{
			Path:         getString(rm, "path", ""),
			Strategy:     getString(rm, "strategy", "round_robin"),
			Upstreams:    getStringSlice(rm, "upstreams"),
			DefaultModel: getString(rm, "default_model", ""),
			DomainTags:   getStringSlice(rm, "domain_tags"),
		}
		if fm := asMap(rm["fallback"]); fm != nil {
			fb := &Fallback{
				Mode:              getString(fm, "mode", "fail_fast"),
				MaxAttempts:       getInt(fm, "max_attempts", 0),
				TotalTimeoutMS:    getInt(fm, "total_timeout_ms", 0),
				ConnectRetry:      getInt(fm, "connect_retry", 0),
				RetryOn:           getStringSlice(fm, "retry_on"),
				TransparentHeader: getBool(fm, "transparent_header", false),
			}
			for _, mm := range asList(fm["model_map"]) {
				m := asMap(mm)
				if m == nil {
					continue
				}
				to := asMap(m["to"])
				if to == nil {
					continue
				}
				fb.ModelMap = append(fb.ModelMap, ModelMapEntry{
					From: getString(m, "from", ""),
					To: ModelMapTarget{
						Upstream: getString(to, "upstream", ""),
						Model:    getString(to, "model", ""),
					},
				})
			}
			r.Fallback = fb
		}
		cfg.Routers = append(cfg.Routers, r)
	}

	if m := asMap(root["security"]); m != nil {
		cfg.Security.SensitiveWords = getStringSlice(m, "sensitive_words")
		cfg.Security.InjectionPatterns = getStringSlice(m, "injection_patterns")
		cfg.Security.BlockResponse = asMap(m["block_response"])
	}

	if m := asMap(root["injector"]); m != nil {
		cfg.Injector.Templates = map[string]InjectTemplate{}
		for _, dept := range sortedKeys(asMap(m["templates"])) {
			tm := asMap(asMap(m["templates"])[dept])
			if tm == nil {
				continue
			}
			cfg.Injector.Templates[dept] = InjectTemplate{
				Head: getString(tm, "head", ""),
				Tail: getString(tm, "tail", ""),
			}
		}
	}

	if m := asMap(root["transport"]); m != nil {
		cfg.Transport.ConnectTimeoutSec = getInt(m, "connect_timeout_sec", 5)
		cfg.Transport.TTFTTimeoutSec = getInt(m, "ttft_timeout_sec", 10)
		cfg.Transport.IdleReadTimeoutSec = getInt(m, "idle_read_timeout_sec", 30)
		cfg.Transport.MaxIdleConns = getInt(m, "max_idle_conns", 100)
		cfg.Transport.MaxIdlePerHost = getInt(m, "max_idle_conns_per_host", 10)
		cfg.Transport.MaxRequestBodyBytes = getInt64(m, "max_request_body_bytes", 0)
		cfg.Transport.MaxAuditBodyBytes = getInt64(m, "max_audit_body_bytes", 0)
		cfg.Transport.AgentTTFTTimeoutSec = getInt(m, "agent_ttft_timeout_sec", 0)
		cfg.Transport.AgentMaxRequestBodyBytes = getInt64(m, "agent_max_request_body_bytes", 0)
		cfg.Transport.AgentGatewayMeta = getBool(m, "agent_gateway_meta", false)
	}

	if m := asMap(root["edge_storage"]); m != nil {
		cfg.EdgeStorage.Enabled = getBool(m, "enabled", false)
		cfg.EdgeStorage.MaxDiskMB = getInt64(m, "max_disk_mb", 512)
		cfg.EdgeStorage.Strategy = getString(m, "strategy", "rotate")
		cfg.EdgeStorage.ReportURL = getString(m, "report_url", "")
	}

	if m := asMap(root["watchdog"]); m != nil {
		cfg.Watchdog.Enabled = getBool(m, "enabled", true)
		cfg.Watchdog.Device = getString(m, "device", "/dev/watchdog")
		cfg.Watchdog.IntervalSec = getInt(m, "interval_sec", 30)
	}

	if m := asMap(root["capacity"]); m != nil {
		cfg.Capacity.MaxInflight = getInt(m, "max_inflight", 0)
		cfg.Capacity.MemSoftPercent = getInt(m, "mem_soft_percent", 0)
		cfg.Capacity.MemHardPercent = getInt(m, "mem_hard_percent", 0)
		cfg.Capacity.HysteresisSamples = getInt(m, "hysteresis_samples", 0)
	}

	return cfg
}

// Validate 执行配置静态审计。
func (c *Config) Validate() error {
	if c.Server.Port <= 0 || c.Server.Port > 65535 {
		return fmt.Errorf("server.port 非法: %d", c.Server.Port)
	}
	if len(c.Auth.Keys) == 0 {
		return fmt.Errorf("auth.keys 不能为空，至少需要一把 vKey")
	}
	for _, k := range c.Auth.Keys {
		if k.RateLimit < 0 || k.QuotaDaily < 0 {
			return fmt.Errorf("auth key %s 的 rate_limit/quota_daily 不能为负数", k.VKey)
		}
	}

	upstreamNames := map[string]bool{}
	upstreamByName := map[string]*Upstream{}
	for _, u := range c.Upstreams {
		if u.Name == "" {
			return fmt.Errorf("存在未命名的 upstream")
		}
		if upstreamNames[u.Name] {
			return fmt.Errorf("upstream 名称重复: %s", u.Name)
		}
		upstreamNames[u.Name] = true
		upstreamByName[u.Name] = u
		if u.BaseURL == "" {
			return fmt.Errorf("upstream %s 缺少 base_url", u.Name)
		}
		if u.Protocol != "openai" && u.Protocol != "baidu_wenxin" {
			return fmt.Errorf("upstream %s 的 protocol 非法: %s", u.Name, u.Protocol)
		}
		if u.AuthMode == "token_exchange" && u.Protocol != "baidu_wenxin" {
			return fmt.Errorf("upstream %s: token_exchange 仅支持 baidu_wenxin 协议", u.Name)
		}
	}

	seenPaths := map[string]bool{}
	for _, r := range c.Routers {
		if r.Path == "" || !strings.HasPrefix(r.Path, "/") {
			return fmt.Errorf("router 路径非法: %q", r.Path)
		}
		if seenPaths[r.Path] {
			return fmt.Errorf("router 路径重复绑定: %s", r.Path)
		}
		seenPaths[r.Path] = true
		if !validStrategy(r.Strategy) {
			return fmt.Errorf("router %s 的策略非法: %s", r.Path, r.Strategy)
		}
		for _, name := range r.Upstreams {
			if !upstreamNames[name] {
				return fmt.Errorf("router %s 引用了不存在的 upstream: %s", r.Path, name)
			}
		}
		if r.Fallback != nil {
			fb := r.Fallback
			if fb.Mode != "fail_fast" && fb.Mode != "failover" {
				return fmt.Errorf("router %s 的 fallback.mode 非法: %s", r.Path, fb.Mode)
			}
			if fb.MaxAttempts < 0 {
				return fmt.Errorf("router %s 的 fallback.max_attempts 非法: %d", r.Path, fb.MaxAttempts)
			}
			if fb.ConnectRetry < 0 {
				return fmt.Errorf("router %s 的 fallback.connect_retry 非法: %d", r.Path, fb.ConnectRetry)
			}
			if fb.TotalTimeoutMS < 0 {
				return fmt.Errorf("router %s 的 fallback.total_timeout_ms 非法: %d", r.Path, fb.TotalTimeoutMS)
			}
			for _, rt := range fb.RetryOn {
				switch rt {
				case "429", "5xx", "timeout", "connect":
				default:
					return fmt.Errorf("router %s 的 fallback.retry_on 非法: %s", r.Path, rt)
				}
			}
			for _, mm := range fb.ModelMap {
				if mm.From == "" || mm.To.Upstream == "" {
					return fmt.Errorf("router %s 的 fallback.model_map 存在空条目", r.Path)
				}
				target, ok := upstreamByName[mm.To.Upstream]
				if !ok {
					return fmt.Errorf("router %s 的 fallback.model_map 引用了不存在的 upstream: %s", r.Path, mm.To.Upstream)
				}
				// 目标上游若声明了 models，映射的模型必须被声明支持
				if mm.To.Model != "" && len(target.Models) > 0 && !contains(target.Models, mm.To.Model) {
					return fmt.Errorf("router %s 的 fallback.model_map 目标模型 %s 不在 upstream %s 的 models 中",
						r.Path, mm.To.Model, mm.To.Upstream)
				}
			}
		}
	}

	seenPluginNames := map[string]bool{}
	seenOrders := map[int]bool{}
	for _, p := range c.Plugins {
		if p.Name == "" {
			return fmt.Errorf("存在未命名的插件")
		}
		if seenPluginNames[p.Name] {
			return fmt.Errorf("插件重复挂载: %s", p.Name)
		}
		seenPluginNames[p.Name] = true
		if seenOrders[p.Order] {
			return fmt.Errorf("插件顺序冲突: order %d 被重复占用", p.Order)
		}
		seenOrders[p.Order] = true
		if p.FailStrategy != "abort" && p.FailStrategy != "bypass" {
			return fmt.Errorf("插件 %s 的 fail_strategy 非法: %s", p.Name, p.FailStrategy)
		}
	}

	if c.Transport.ConnectTimeoutSec <= 0 || c.Transport.TTFTTimeoutSec <= 0 || c.Transport.IdleReadTimeoutSec <= 0 {
		return fmt.Errorf("transport 超时配置必须为正数")
	}
	if c.Transport.MaxRequestBodyBytes < 0 || c.Transport.MaxAuditBodyBytes < 0 || c.Transport.AgentMaxRequestBodyBytes < 0 || c.Transport.AgentTTFTTimeoutSec < 0 {
		return fmt.Errorf("transport 内存/agent 上限配置不能为负数")
	}
	if c.Capacity.MaxInflight < 0 || c.Capacity.MemSoftPercent < 0 || c.Capacity.MemHardPercent < 0 || c.Capacity.HysteresisSamples < 0 {
		return fmt.Errorf("capacity 配置不能为负数")
	}
	if c.Capacity.MemSoftPercent > 100 || c.Capacity.MemHardPercent > 100 {
		return fmt.Errorf("capacity 内存阈值百分比不能超过 100")
	}
	return nil
}

func validStrategy(s string) bool {
	switch s {
	case "round_robin", "least_connection", "latency", "cost", "domain_tag":
		return true
	}
	return false
}

// --- 类型提取辅助函数 ---

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func asMap(v any) map[string]any {
	if v == nil {
		return nil
	}
	m, _ := v.(map[string]any)
	return m
}

func asList(v any) []any {
	if v == nil {
		return nil
	}
	switch t := v.(type) {
	case []any:
		return t
	}
	return nil
}

func getString(m map[string]any, key, def string) string {
	v, ok := m[key]
	if !ok || v == nil {
		return def
	}
	switch s := v.(type) {
	case string:
		return s
	case int64:
		return strconv.FormatInt(s, 10)
	case float64:
		return strconv.FormatFloat(s, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(s)
	}
	return def
}

func getStringSlice(m map[string]any, key string) []string {
	v, ok := m[key]
	if !ok || v == nil {
		return nil
	}
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func getInt(m map[string]any, key string, def int) int {
	v, ok := m[key]
	if !ok || v == nil {
		return def
	}
	switch n := v.(type) {
	case int64:
		return int(n)
	case int:
		return n
	case float64:
		return int(n)
	case string:
		if i, err := strconv.Atoi(n); err == nil {
			return i
		}
	}
	return def
}

func getInt64(m map[string]any, key string, def int64) int64 {
	v, ok := m[key]
	if !ok || v == nil {
		return def
	}
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	case string:
		if i, err := strconv.ParseInt(n, 10, 64); err == nil {
			return i
		}
	}
	return def
}

func getFloat(m map[string]any, key string, def float64) float64 {
	v, ok := m[key]
	if !ok || v == nil {
		return def
	}
	switch n := v.(type) {
	case float64:
		return n
	case int64:
		return float64(n)
	case int:
		return float64(n)
	case string:
		if f, err := strconv.ParseFloat(n, 64); err == nil {
			return f
		}
	}
	return def
}

func getBool(m map[string]any, key string, def bool) bool {
	v, ok := m[key]
	if !ok || v == nil {
		return def
	}
	switch b := v.(type) {
	case bool:
		return b
	case string:
		if p, err := strconv.ParseBool(b); err == nil {
			return p
		}
	}
	return def
}

func sortedKeys(m map[string]any) []string {
	if m == nil {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
