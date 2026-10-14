package plugins

import (
	"net/http/httptest"
	"testing"

	"github.com/dezd-ai/ai-gateway-core/config"
	"github.com/dezd-ai/ai-gateway-core/kernel"
)

func newModelRouting(t *testing.T, cheap, reasoning string) *ModelRoutingPlugin {
	t.Helper()
	p := &ModelRoutingPlugin{}
	if err := p.Init(`{"cheap_model":"` + cheap + `","reasoning_model":"` + reasoning + `","cheap_token_threshold":10,"reasoning_keywords":["分析","代码","review"]}`); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestModelRoutingComplexity(t *testing.T) {
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	p := newModelRouting(t, "qwen-turbo", "deepseek-r1")

	t.Run("简单请求 → 便宜档", func(t *testing.T) {
		ctx := kernel.NewPluginContext()
		ctx.Set(kernel.CtxModel, "deepseek-v4-flash")
		ctx.Set(kernel.CtxOriginalPrompt, "今天天气怎么样")
		if err := p.PreRequest(ctx, req); err != nil {
			t.Fatal(err)
		}
		if got := ctx.GetString(kernel.CtxModel); got != "qwen-turbo" {
			t.Fatalf("简单请求应定便宜档, got %q", got)
		}
	})

	t.Run("命中关键词 → 推理档", func(t *testing.T) {
		ctx := kernel.NewPluginContext()
		ctx.Set(kernel.CtxModel, "deepseek-v4-flash")
		ctx.Set(kernel.CtxOriginalPrompt, "帮我分析这个并发问题并给出优化")
		if err := p.PreRequest(ctx, req); err != nil {
			t.Fatal(err)
		}
		if got := ctx.GetString(kernel.CtxModel); got != "deepseek-r1" {
			t.Fatalf("命中关键词应定推理档, got %q", got)
		}
	})

	t.Run("token 超阈值 → 推理档", func(t *testing.T) {
		ctx := kernel.NewPluginContext()
		ctx.Set(kernel.CtxModel, "deepseek-v4-flash")
		ctx.Set(kernel.CtxPromptTokensEst, 50) // >= threshold 10
		if err := p.PreRequest(ctx, req); err != nil {
			t.Fatal(err)
		}
		if got := ctx.GetString(kernel.CtxModel); got != "deepseek-r1" {
			t.Fatalf("token 超阈值应定推理档, got %q", got)
		}
	})

	t.Run("非对话请求跳过", func(t *testing.T) {
		ctx := kernel.NewPluginContext() // 无 CtxModel
		if err := p.PreRequest(ctx, req); err != nil {
			t.Fatal(err)
		}
		if ctx.GetString(kernel.CtxModel) != "" {
			t.Fatal("非对话请求不应改写模型")
		}
	})
}

func TestModelRoutingWhitelistGuarantee(t *testing.T) {
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	p := newModelRouting(t, "qwen-turbo", "deepseek-r1")

	t.Run("意图档位在个人白名单 → 直接使用", func(t *testing.T) {
		ctx := kernel.NewPluginContext()
		ctx.Set(kernel.CtxModel, "deepseek-v4-flash")
		ctx.Set(kernel.CtxOriginalPrompt, "帮我分析代码")
		ctx.Set(kernel.CtxAuthKey, &config.AuthKey{ModelWhitelist: []string{"qwen-turbo", "deepseek-r1"}})
		if err := p.PreRequest(ctx, req); err != nil {
			t.Fatal(err)
		}
		if got := ctx.GetString(kernel.CtxModel); got != "deepseek-r1" {
			t.Fatalf("白名单含推理档时应直接用, got %q", got)
		}
	})

	t.Run("意图不在白名单 → 回退白名单内档位（不越权）", func(t *testing.T) {
		ctx := kernel.NewPluginContext()
		ctx.Set(kernel.CtxModel, "deepseek-v4-flash")
		ctx.Set(kernel.CtxOriginalPrompt, "帮我分析代码")
		ctx.Set(kernel.CtxAuthKey, &config.AuthKey{ModelWhitelist: []string{"qwen-turbo"}})
		if err := p.PreRequest(ctx, req); err != nil {
			t.Fatal(err)
		}
		if got := ctx.GetString(kernel.CtxModel); got != "qwen-turbo" {
			t.Fatalf("推理档不在白名单应回退便宜档, got %q", got)
		}
	})

	t.Run("白名单不含任何档位 → 不改写", func(t *testing.T) {
		ctx := kernel.NewPluginContext()
		ctx.Set(kernel.CtxModel, "deepseek-v4-flash")
		ctx.Set(kernel.CtxOriginalPrompt, "帮我分析代码")
		ctx.Set(kernel.CtxAuthKey, &config.AuthKey{ModelWhitelist: []string{"some-other-model"}})
		if err := p.PreRequest(ctx, req); err != nil {
			t.Fatal(err)
		}
		if got := ctx.GetString(kernel.CtxModel); got != "deepseek-v4-flash" {
			t.Fatalf("白名单不含档位应保持原模型, got %q", got)
		}
	})

	t.Run("白名单为空 → 用意图档位", func(t *testing.T) {
		ctx := kernel.NewPluginContext()
		ctx.Set(kernel.CtxModel, "deepseek-v4-flash")
		ctx.Set(kernel.CtxOriginalPrompt, "帮我分析代码")
		ctx.Set(kernel.CtxAuthKey, &config.AuthKey{}) // 无白名单
		if err := p.PreRequest(ctx, req); err != nil {
			t.Fatal(err)
		}
		if got := ctx.GetString(kernel.CtxModel); got != "deepseek-r1" {
			t.Fatalf("白名单为空应用意图档位, got %q", got)
		}
	})
}
