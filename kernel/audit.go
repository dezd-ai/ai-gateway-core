package kernel

import (
	"encoding/json"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/DezDing/ai-gateway-core/config"
)

// auditStdout 控制审计日志是否同步回显到标准输出；压测/测试时关闭以隔离噪声。
// 由内核统一持有，auditor 与拦截审计共用同一开关（plugins.SetAuditStdout 委托到此）。
var auditStdout atomic.Bool

func init() { auditStdout.Store(true) }

// SetAuditStdout 切换审计日志的 stdout 回显。
func SetAuditStdout(enabled bool) { auditStdout.Store(enabled) }

// AuditStdoutEnabled 返回当前是否回显审计到 stdout。
func AuditStdoutEnabled() bool { return auditStdout.Load() }

// EmitAbortAudit 为被 PreRequest 拦截（401/403/502）的请求落一条审计记录（backlog #4）。
// 与 auditor 的正常审计记录同构，额外携带 http_status / success / abort_reason，
// 使安全事件（越权尝试、敏感词命中、插件熔断）可统计、可追溯。
func EmitAbortAudit(ctx *PluginContext, status int, reason string) {
	storage, _ := ctx.GetValue(CtxStorage).(*EdgeStorage)
	if storage == nil && !auditStdout.Load() {
		return
	}

	username, department := "", ""
	agent := false
	if key, ok := ctx.GetValue(CtxAuthKey).(*config.AuthKey); ok {
		username, department = key.Username, key.Department
		agent = key.AgentMode
	}

	start := ctx.StartTime()
	dur := time.Since(start).Milliseconds()
	if dur < 0 {
		dur = 0
	}

	rec := map[string]any{
		"trace_id":          ctx.GetString(CtxTraceID),
		"timestamp":         time.Now().Format(time.RFC3339Nano),
		"username":          username,
		"department":        department,
		"agent_mode":        agent,
		"model":             ctx.GetString(CtxModel),
		"upstream":          "",
		"http_status":       status,
		"success":           false,
		"abort_reason":      reason,
		"prompt_injected":   ctx.GetBool(CtxPromptInjected),
		"security_check":    ctx.GetString(CtxSecurityCheck),
		"prompt_tokens":     int64(0),
		"completion_tokens": int64(0),
		"total_tokens":      int64(0),
		"cached_tokens":     int64(0),
		"precision":         "",
		"executed_plugins":  ctx.ExecutedPlugins(),
		"duration_ms":       dur,
		"fallback_chain":    []string{},
		"attempts":          int64(0),
		"chain_truncated":   false,
		"stop_reason":       "",
	}

	b, err := json.Marshal(rec)
	if err != nil {
		return
	}
	if auditStdout.Load() {
		fmt.Println(string(b))
	}
	if storage != nil {
		storage.Append(b)
	}
}
