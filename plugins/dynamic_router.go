package plugins

import (
	"fmt"
	"net/http"

	"github.com/dezd-ai/ai-gateway-core/kernel"
)

// DynamicRouterPlugin 在反向代理 Director 阶段读取路由策略，
// 动态挑选最佳节点并重写 req.URL / req.Host，安全替换 vKey 为真实凭证。
type DynamicRouterPlugin struct {
	kernel.BasePlugin
}

func init() {
	kernel.RegisterPlugin("dynamic_router", func() kernel.Plugin { return &DynamicRouterPlugin{} })
}

func (p *DynamicRouterPlugin) Name() string { return "dynamic_router" }

// Director 由内核 Pipeline 在 Director 阶段回调，执行多维智能路由（含请求级失败转移入口）。
func (p *DynamicRouterPlugin) Director(ctx *kernel.PluginContext, req *http.Request) error {
	router, ok := ctx.GetValue(kernel.CtxRouter).(*kernel.Router)
	if !ok || router == nil {
		return fmt.Errorf("router 未注入上下文")
	}
	return router.RouteWithFallback(ctx, req)
}
