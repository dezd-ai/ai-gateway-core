package kernel

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// 内核专属命名空间前缀，锁死避免与插件私有键冲突。
const (
	PrefixGW = "gw_"

	CtxTraceID         = PrefixGW + "trace_id"
	CtxStartTime       = PrefixGW + "start_time"
	CtxConfig          = PrefixGW + "config"
	CtxRouter          = PrefixGW + "router"
	CtxExecutedPlugins = PrefixGW + "executed_plugins"
	CtxOriginalPrompt  = PrefixGW + "original_prompt"
	CtxFinalPrompt     = PrefixGW + "final_prompt"
	CtxPromptInjected  = PrefixGW + "prompt_injected"
	CtxSecurityCheck   = PrefixGW + "security_check"
	CtxAuthKey         = PrefixGW + "auth_key"
	CtxModel           = PrefixGW + "model"
	CtxUpstreamName    = PrefixGW + "upstream_name"
	CtxPromptTokensEst = PrefixGW + "prompt_tokens_est"
	CtxAborted         = PrefixGW + "aborted"
	CtxAbortStatus     = PrefixGW + "abort_status"
	CtxAbortBody       = PrefixGW + "abort_body"
	CtxProxyError      = PrefixGW + "proxy_error"
	CtxStorage         = PrefixGW + "storage"
	CtxFallbackChain   = PrefixGW + "fallback_chain"  // []string，实际尝试过的 (上游,模型) 序列
	CtxAttempts        = PrefixGW + "attempts"        // int64，实际 RoundTrip 次数
	CtxStopReason      = PrefixGW + "stop_reason"     // string，exhausted | timeout
	CtxChainTruncated  = PrefixGW + "chain_truncated" // bool，max_attempts 截断（还有候选未尝试）
	CtxRateLimiter     = PrefixGW + "rate_limiter"    // *RateLimiter，vKey 级 QPS 限流
	CtxQuota           = PrefixGW + "quota"           // *QuotaTracker，vKey 级每日 token 配额
	CtxModelAlias      = PrefixGW + "model_alias"     // string，客户端传入的别名（翻译前），审计用
)

// PluginContext 是单个 HTTP 请求生命周期的线程安全 Scratchpad。
// 插件链串行读写，但审计协程可能在请求结束后异步读取，故用 RWMutex 保护。
type PluginContext struct {
	mu   sync.RWMutex
	Data map[string]interface{}
}

func NewPluginContext() *PluginContext {
	return &PluginContext{Data: make(map[string]interface{}, 32)}
}

func (c *PluginContext) Get(key string) (interface{}, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	v, ok := c.Data[key]
	return v, ok
}

// GetValue 返回键对应值（不存在时为 nil），配合逗号-ok 类型断言安全使用。
func (c *PluginContext) GetValue(key string) interface{} {
	v, _ := c.Get(key)
	return v
}

func (c *PluginContext) Set(key string, val interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Data[key] = val
}

func (c *PluginContext) GetString(key string) string {
	v, ok := c.Get(key)
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}

func (c *PluginContext) GetStrings(key string) []string {
	v, ok := c.Get(key)
	if !ok {
		return nil
	}
	switch t := v.(type) {
	case []string:
		return t
	case []interface{}:
		out := make([]string, 0, len(t))
		for _, item := range t {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func (c *PluginContext) GetBool(key string) bool {
	v, ok := c.Get(key)
	if !ok {
		return false
	}
	b, _ := v.(bool)
	return b
}

func (c *PluginContext) GetInt64(key string) int64 {
	v, ok := c.Get(key)
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	}
	return 0
}

// RecordPlugin 记录已执行插件（去重），供响应头与审计日志使用。
func (c *PluginContext) RecordPlugin(name string) {
	executed, _ := c.Get(CtxExecutedPlugins)
	list, _ := executed.([]string)
	for _, n := range list {
		if n == name {
			return
		}
	}
	list = append(list, name)
	c.Set(CtxExecutedPlugins, list)
}

func (c *PluginContext) ExecutedPlugins() []string {
	return c.GetStrings(CtxExecutedPlugins)
}

func (c *PluginContext) StartTime() time.Time {
	if t, ok := c.Get(CtxStartTime); ok {
		if ts, ok := t.(time.Time); ok {
			return ts
		}
	}
	return time.Time{}
}

// Abort 标记流水线中断并携带要返回给客户端的伪装标准错误体。
func (c *PluginContext) Abort(status int, body []byte) {
	c.Set(CtxAborted, true)
	c.Set(CtxAbortStatus, status)
	c.Set(CtxAbortBody, body)
}

func (c *PluginContext) IsAborted() (bool, int, []byte) {
	if !c.GetBool(CtxAborted) {
		return false, 0, nil
	}
	status := 403
	if v, ok := c.Get(CtxAbortStatus); ok {
		if n, ok := v.(int); ok {
			status = n
		}
	}
	body, _ := c.Get(CtxAbortBody)
	b, _ := body.([]byte)
	return true, status, b
}

// NewTraceID 生成 128bit 全局唯一 TraceID（hex 32 字符）。
func NewTraceID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand 失败几乎不可能，退化用时间戳兜底
		return hex.EncodeToString([]byte(time.Now().Format("20060102150405.000000000")))
	}
	return hex.EncodeToString(b[:])
}
