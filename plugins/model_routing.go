package plugins

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/dezd-ai/ai-gateway-core/config"
	"github.com/dezd-ai/ai-gateway-core/kernel"
)

// ModelRoutingPlugin 内容感知模型路由（docs/content-routing.md 阶段 1）：
// 按请求复杂度（token 数 + 关键词）选择便宜档/推理档，改写 CtxModel 让路由自动适配。
// 白名单保证：**选择时**只从「意图档位 ∩ 用户可用模型」里挑（见 content-routing.md 第 4 节约束 3），
// 不做事后校验回退；用户可用集当前来自 vKey model_whitelist，未来按 backlog 1.8 换成角色档位集。
type ModelRoutingPlugin struct {
	kernel.BasePlugin
	cfg modelRoutingConfig
}

type modelRoutingConfig struct {
	CheapModel          string   `json:"cheap_model"`
	ReasoningModel      string   `json:"reasoning_model"`
	CheapTokenThreshold int64    `json:"cheap_token_threshold"`
	ReasoningKeywords   []string `json:"reasoning_keywords"`
}

func init() {
	kernel.RegisterPlugin("model_routing", func() kernel.Plugin { return &ModelRoutingPlugin{} })
}

func (p *ModelRoutingPlugin) Name() string { return "model_routing" }

// Init 解析插件私有配置（config.yaml plugins[].config），缺省值兜底。
func (p *ModelRoutingPlugin) Init(configJSON string) error {
	if configJSON == "" || configJSON == "{}" {
		return nil
	}
	if err := json.Unmarshal([]byte(configJSON), &p.cfg); err != nil {
		return err
	}
	if p.cfg.CheapTokenThreshold <= 0 {
		p.cfg.CheapTokenThreshold = 200
	}
	return nil
}

// PreRequest 评估复杂度并改写 CtxModel（须挂载于 injector 之后、dynamic_router 之前）。
func (p *ModelRoutingPlugin) PreRequest(ctx *kernel.PluginContext, req *http.Request) error {
	// 非对话请求（injector 未写 CtxModel）不参与定档
	if ctx.GetString(kernel.CtxModel) == "" {
		return nil
	}
	// agent 宽松模式：模型由 agent 完全决定，不改写（docs/agent-mode.md）
	if key, ok := ctx.GetValue(kernel.CtxAuthKey).(*config.AuthKey); ok && key.AgentMode {
		return nil
	}
	if p.cfg.CheapModel == "" || p.cfg.ReasoningModel == "" {
		return nil
	}

	prompt := ctx.GetString(kernel.CtxFinalPrompt)
	if prompt == "" {
		prompt = ctx.GetString(kernel.CtxOriginalPrompt)
	}

	// 评估：token 超阈值或命中关键词 → 推理档
	complex := ctx.GetInt64(kernel.CtxPromptTokensEst) >= p.cfg.CheapTokenThreshold
	if !complex && prompt != "" {
		lower := strings.ToLower(prompt)
		for _, kw := range p.cfg.ReasoningKeywords {
			if kw != "" && strings.Contains(lower, strings.ToLower(kw)) {
				complex = true
				break
			}
		}
	}

	intent := p.cfg.CheapModel
	if complex {
		intent = p.cfg.ReasoningModel
	}

	// 白名单保证：选择域 = 意图档位 ∩ 用户可用模型
	selected := intent
	if key, ok := ctx.GetValue(kernel.CtxAuthKey).(*config.AuthKey); ok && len(key.ModelWhitelist) > 0 {
		if !containsStr(key.ModelWhitelist, intent) {
			chosen := ""
			for _, c := range []string{p.cfg.CheapModel, p.cfg.ReasoningModel} {
				if containsStr(key.ModelWhitelist, c) {
					chosen = c
					break
				}
			}
			if chosen == "" {
				return nil // 白名单不含任何档位：不改写，保持原模型
			}
			selected = chosen
		}
	}

	ctx.Set(kernel.CtxModel, selected)
	return nil
}
