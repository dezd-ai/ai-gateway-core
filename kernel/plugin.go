package kernel

import (
	"net/http"
)

// Plugin 是标准向后兼容插件接口（API Versioning）。
// 未来接口升级统一通过组合继承 BasePlugin 空结构体防止 Breaking Changes。
type Plugin interface {
	Name() string
	ApiVersion() string
	Init(configJSON string) error
	PreRequest(ctx *PluginContext, req *http.Request) error
	ModifyResponse(ctx *PluginContext, res *http.Response) error
}

// BasePlugin 为插件提供默认空实现，后续新增方法时继承方无需同步修改。
type BasePlugin struct{}

func (BasePlugin) ApiVersion() string                                  { return "v1" }
func (BasePlugin) Init(string) error                                   { return nil }
func (BasePlugin) PreRequest(*PluginContext, *http.Request) error      { return nil }
func (BasePlugin) ModifyResponse(*PluginContext, *http.Response) error { return nil }

// DirectorHook 是可选扩展接口：实现它的插件会在反向代理 Director 阶段被调用，
// 用于承载 DynamicRouterPlugin 等路由级插件的分流逻辑。
type DirectorHook interface {
	Director(ctx *PluginContext, req *http.Request) error
}

// FailStrategy 决定插件异常时的内核响应。
type FailStrategy int

const (
	FailAbort  FailStrategy = iota // 直接拦截熔断
	FailBypass                     // 跳过故障插件继续转发
)
