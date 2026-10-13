package plugins

import (
	"encoding/json"
	"net/http"

	"github.com/DezDing/ai-gateway-core/config"
	"github.com/DezDing/ai-gateway-core/kernel"
)

// SecurityGuardPlugin 深度扫描最终 Prompt，作为敏感词 DLP 拦截点与
// 防提示词注入攻击防御点；命中即熔断并返回伪装标准 403。
type SecurityGuardPlugin struct {
	kernel.BasePlugin
}

func init() {
	kernel.RegisterPlugin("security_guard", func() kernel.Plugin { return &SecurityGuardPlugin{} })
}

func (p *SecurityGuardPlugin) Name() string { return "security_guard" }

func (p *SecurityGuardPlugin) PreRequest(ctx *kernel.PluginContext, req *http.Request) error {
	cfg, _ := ctx.GetValue(kernel.CtxConfig).(*config.Config)

	prompt := ctx.GetString(kernel.CtxFinalPrompt)
	if prompt == "" {
		prompt = ctx.GetString(kernel.CtxOriginalPrompt)
	}
	if prompt == "" {
		ctx.Set(kernel.CtxSecurityCheck, "PASSED")
		return nil
	}

	// 正则已在 config 层预编译（sync.Once），此处零重复编译（backlog #3）
	for _, re := range cfg.Security.SensitiveRegexps() {
		if re.MatchString(prompt) {
			ctx.Set(kernel.CtxSecurityCheck, "BLOCKED_SENSITIVE")
			ctx.Abort(http.StatusForbidden, buildBlockResponse(cfg))
			return nil
		}
	}

	// agent 宽松模式：仅敏感词 DLP，跳过注入正则（避免误伤工具调用内容，见 docs/agent-mode.md）
	agent := false
	if key, ok := ctx.GetValue(kernel.CtxAuthKey).(*config.AuthKey); ok && key.AgentMode {
		agent = true
	}
	if !agent {
		for _, re := range cfg.Security.InjectionRegexps() {
			if re.MatchString(prompt) {
				ctx.Set(kernel.CtxSecurityCheck, "BLOCKED_INJECTION")
				ctx.Abort(http.StatusForbidden, buildBlockResponse(cfg))
				return nil
			}
		}
	}

	ctx.Set(kernel.CtxSecurityCheck, "PASSED")
	return nil
}

func buildBlockResponse(cfg *config.Config) []byte {
	if errBlock, ok := cfg.Security.BlockResponse["error"].(map[string]any); ok {
		b, merr := json.Marshal(map[string]any{"error": errBlock})
		if merr == nil {
			return b
		}
	}
	return gateErrorJSON("content_filter", "请求被安全策略拦截", "content_filter")
}
