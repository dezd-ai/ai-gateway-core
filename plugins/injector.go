package plugins

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/dezd-ai/ai-gateway-core/config"
	"github.com/dezd-ai/ai-gateway-core/kernel"
)

// PromptInjectorPlugin 解析 OpenAI JSON Body，按部门动态注入企业准则模板，
// 使用 io.NopCloser 重写 Body，并将原始/最终 Prompt 记入 Context。
type PromptInjectorPlugin struct {
	kernel.BasePlugin
}

func init() {
	kernel.RegisterPlugin("prompt_injector", func() kernel.Plugin { return &PromptInjectorPlugin{} })
}

func (p *PromptInjectorPlugin) Name() string { return "prompt_injector" }

func (p *PromptInjectorPlugin) PreRequest(ctx *kernel.PluginContext, req *http.Request) error {
	cfg, _ := ctx.GetValue(kernel.CtxConfig).(*config.Config)

	// 仅处理 JSON 对话体；非 OpenAI 工业算法路径平滑绕过。
	ct := req.Header.Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		ctx.Set(kernel.CtxPromptInjected, false)
		return nil
	}

	body, err := kernel.ReadBody(req.Body, effectiveBodyLimit(cfg, ctx))
	if err != nil {
		if errors.Is(err, kernel.ErrBodyTooLarge) {
			ctx.Abort(http.StatusRequestEntityTooLarge, gateErrorJSON("invalid_request_error", "request body too large", "request_too_large"))
			return nil
		}
		return err
	}
	req.Body = io.NopCloser(bytes.NewReader(body))

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		// 非法 JSON：非对话类请求，不注入也不阻断（交由上游处理）
		ctx.Set(kernel.CtxPromptInjected, false)
		return nil
	}

	model, _ := payload["model"].(string)

	// 模型别名翻译（roadmap R3 / docs/model-alias.md）：逻辑名 → 真实上游模型名。
	// 翻译放在白名单校验之前，保证白名单 / 路由 / 审计全链路看到真实名；
	// 未命中别名原样透传（向后兼容），agent 流量同样保留翻译（翻译不是换模型）。
	if cfg != nil && model != "" {
		if real, ok := cfg.ModelAliases[model]; ok && real != "" && real != model {
			ctx.Set(kernel.CtxModelAlias, model)
			model = real
			payload["model"] = real
			if nb, err := json.Marshal(payload); err == nil {
				req.Body = io.NopCloser(bytes.NewReader(nb))
				req.ContentLength = int64(len(nb))
			}
		}
	}
	ctx.Set(kernel.CtxModel, model)

	original := extractPromptText(payload["messages"])
	ctx.Set(kernel.CtxOriginalPrompt, original)
	ctx.Set(kernel.CtxPromptTokensEst, estimateTokens(original))

	// 模型白名单强制
	if key, ok := ctx.GetValue(kernel.CtxAuthKey).(*config.AuthKey); ok && model != "" {
		if len(key.ModelWhitelist) > 0 && !containsStr(key.ModelWhitelist, model) {
			ctx.Abort(http.StatusForbidden, gateErrorJSON("permission_error", "模型不在该 vKey 白名单内", "model_not_allowed"))
			return nil
		}
	}

	head, tail := "", ""
	if key, ok := ctx.GetValue(kernel.CtxAuthKey).(*config.AuthKey); ok {
		// agent 宽松模式：跳过模板注入（agent 上下文不容污染），但仍做白名单与 model 提取
		if !key.AgentMode {
			if tmpl, ok := cfg.Injector.Templates[key.Department]; ok {
				head, tail = tmpl.Head, tmpl.Tail
			}
		}
	}

	if head != "" || tail != "" {
		payload = injectTemplate(payload, head, tail)
		newBody, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		req.Body = io.NopCloser(bytes.NewReader(newBody))
		req.ContentLength = int64(len(newBody))
		ctx.Set(kernel.CtxFinalPrompt, head+original+tail)
		ctx.Set(kernel.CtxPromptInjected, true)
	} else {
		ctx.Set(kernel.CtxFinalPrompt, original)
		ctx.Set(kernel.CtxPromptInjected, false)
	}
	return nil
}
