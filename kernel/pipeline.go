package kernel

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"sync"

	"github.com/dezd-ai/ai-gateway-core/config"
)

// PluginFactory 是插件构造器，供各插件包通过 RegisterPlugin 注册。
type PluginFactory func() Plugin

var pluginRegistry = struct {
	sync.RWMutex
	factories map[string]PluginFactory
}{factories: map[string]PluginFactory{}}

func RegisterPlugin(name string, f PluginFactory) {
	pluginRegistry.Lock()
	defer pluginRegistry.Unlock()
	pluginRegistry.factories[name] = f
}

func LookupPlugin(name string) (PluginFactory, bool) {
	pluginRegistry.RLock()
	defer pluginRegistry.RUnlock()
	f, ok := pluginRegistry.factories[name]
	return f, ok
}

func RegisteredPlugins() []string {
	pluginRegistry.RLock()
	defer pluginRegistry.RUnlock()
	out := make([]string, 0, len(pluginRegistry.factories))
	for name := range pluginRegistry.factories {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

type boundPlugin struct {
	name         string
	plugin       Plugin
	failStrategy FailStrategy
}

// Pipeline 是插件拦截链的非阻塞执行器，持有有序插件切片；
// RegisterDynamicPlugin 为未来 Wasm 插件市场预留动态挂载槽位。
type Pipeline struct {
	mu      sync.RWMutex
	plugins []*boundPlugin
}

func NewPipeline() *Pipeline {
	return &Pipeline{}
}

func (p *Pipeline) AddPlugin(plg Plugin, fail FailStrategy, name string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.plugins = append(p.plugins, &boundPlugin{name: name, plugin: plg, failStrategy: fail})
}

// RegisterDynamicPlugin 动态挂载黑盒插件（Wasm 运行时无需重启即可注入拦截链）。
func (p *Pipeline) RegisterDynamicPlugin(plg Plugin) {
	p.AddPlugin(plg, FailBypass, plg.Name())
}

func (p *Pipeline) snapshot() []*boundPlugin {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]*boundPlugin, len(p.plugins))
	copy(out, p.plugins)
	return out
}

func (p *Pipeline) Len() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.plugins)
}

// ExecutePreRequest 顺序执行 PreRequest 钩子；支持 abort / bypass 容错与熔断中断。
func (p *Pipeline) ExecutePreRequest(ctx *PluginContext, req *http.Request) error {
	for _, bp := range p.snapshot() {
		if err := bp.plugin.PreRequest(ctx, req); err != nil {
			if bp.failStrategy == FailAbort {
				ctx.Abort(http.StatusBadGateway, []byte(fmt.Sprintf(
					`{"error":{"message":"plugin %s failed","type":"plugin_error","code":"plugin_aborted"}}`, bp.name)))
				return err
			}
			continue
		}
		ctx.RecordPlugin(bp.name)
		if aborted, _, _ := ctx.IsAborted(); aborted {
			return nil
		}
	}
	return nil
}

// ExecuteDirector 调用实现了 DirectorHook 的插件，承载路由级分流。
func (p *Pipeline) ExecuteDirector(ctx *PluginContext, req *http.Request) error {
	for _, bp := range p.snapshot() {
		if dh, ok := bp.plugin.(DirectorHook); ok {
			if err := dh.Director(ctx, req); err != nil {
				if bp.failStrategy == FailAbort {
					return err
				}
				continue
			}
		}
	}
	return nil
}

// ExecuteModifyResponse 顺序执行 ModifyResponse 钩子（auditor 置尾以保证最外层包装）。
func (p *Pipeline) ExecuteModifyResponse(ctx *PluginContext, res *http.Response) error {
	for _, bp := range p.snapshot() {
		if err := bp.plugin.ModifyResponse(ctx, res); err != nil {
			if bp.failStrategy == FailAbort {
				return err
			}
			continue
		}
		ctx.RecordPlugin(bp.name)
	}
	return nil
}

// BuildPipeline 依据 config.yaml 的插件声明（按 order 排序）构造拦截链。
func BuildPipeline(cfgPlugins []config.Plugin) (*Pipeline, error) {
	declared := make([]config.Plugin, 0, len(cfgPlugins))
	for _, pc := range cfgPlugins {
		if pc.Enabled {
			declared = append(declared, pc)
		}
	}
	sort.SliceStable(declared, func(i, j int) bool { return declared[i].Order < declared[j].Order })

	p := NewPipeline()
	for _, pc := range declared {
		factory, ok := LookupPlugin(pc.Name)
		if !ok {
			return nil, fmt.Errorf("插件未注册: %s（请确认 plugins 包已导入并完成 init 注册）", pc.Name)
		}
		plg := factory()
		cfgJSON := "{}"
		if pc.Config != nil {
			if b, err := json.Marshal(pc.Config); err == nil {
				cfgJSON = string(b)
			}
		}
		if err := plg.Init(cfgJSON); err != nil {
			return nil, fmt.Errorf("插件 %s 初始化失败: %w", pc.Name, err)
		}
		fail := FailAbort
		if pc.FailStrategy == "bypass" {
			fail = FailBypass
		}
		p.AddPlugin(plg, fail, pc.Name)
	}
	return p, nil
}
