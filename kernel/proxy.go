package kernel

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/dezd-ai/ai-gateway-core/config"
)

// NewTransport 构造一个上游连接池 Transport，锁死连接/首字/空闲读超时。
func NewTransport(cfg config.Transport) *http.Transport {
	connectTimeout := time.Duration(cfg.ConnectTimeoutSec) * time.Second
	return &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   connectTimeout,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   connectTimeout,
		ResponseHeaderTimeout: time.Duration(cfg.TTFTTimeoutSec) * time.Second, // TTFT 首字超时
		ExpectContinueTimeout: time.Second,
		MaxIdleConns:          cfg.MaxIdleConns,
		MaxIdleConnsPerHost:   cfg.MaxIdlePerHost,
		IdleConnTimeout:       90 * time.Second,
		ForceAttemptHTTP2:     true,
	}
}

// --- 请求 Context 携带 PluginContext 的通道 ---

type pluginCtxKeyType struct{}

var pluginCtxKey = pluginCtxKeyType{}

func WithPluginContext(req *http.Request, ctx *PluginContext) *http.Request {
	return req.WithContext(context.WithValue(req.Context(), pluginCtxKey, ctx))
}

func PluginContextFromRequest(req *http.Request) *PluginContext {
	c, _ := req.Context().Value(pluginCtxKey).(*PluginContext)
	return c
}

// routerRoundTripper 依据请求 Context 中挂载的节点分发到其独立连接池。
// 存在 fallbackPlan（failover 模式）时交由候选链循环尝试。
type routerRoundTripper struct{}

func (routerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx := PluginContextFromRequest(req)
	if ctx != nil {
		if v, ok := ctx.Get(CtxProxyError); ok {
			if err, ok := v.(error); ok {
				return nil, err
			}
		}
	}
	if plan, ok := req.Context().Value(fallbackPlanKey).(*fallbackPlan); ok && plan != nil {
		return plan.RoundTrip(req, ctx)
	}
	node, ok := NodeFromContext(req.Context())
	if !ok {
		return nil, fmt.Errorf("请求未绑定上游节点")
	}
	// agent 宽松模式：放大首字超时（懒克隆 Transport，不动共享连接池）
	return node.RoundTripWithTTFT(req, agentTTFT(ctx))
}

// NewReverseProxy 构造内核反向代理：Director 阶段执行路由插件，
// ModifyResponse 阶段执行审计插件，BufferPool 实现零拷贝中转，
// FlushInterval=-1 保证 SSE 流式立即刷出（同步背压不缓冲）。
func NewReverseProxy(pipeline *Pipeline) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			ctx := PluginContextFromRequest(req)
			if ctx == nil {
				return
			}
			if err := pipeline.ExecuteDirector(ctx, req); err != nil {
				ctx.Set(CtxProxyError, err)
				req.URL = &url.URL{Scheme: "invalid", Host: "invalid"}
			}
		},
		Transport:  routerRoundTripper{},
		BufferPool: globalPool,
		ModifyResponse: func(res *http.Response) error {
			ctx := PluginContextFromRequest(res.Request)
			if ctx == nil {
				return nil
			}
			if err := pipeline.ExecuteModifyResponse(ctx, res); err != nil {
				return err
			}
			res.Header.Set("X-AI-Gateway-Trace-ID", ctx.GetString(CtxTraceID))
			res.Header.Set("X-AI-Gateway-Executed-Plugins", strings.Join(ctx.ExecutedPlugins(), ","))
			return nil
		},
		ErrorHandler:  defaultErrorHandler,
		FlushInterval: -1,
	}
}

func defaultErrorHandler(w http.ResponseWriter, r *http.Request, err error) {
	status := http.StatusBadGateway
	msg := "gateway proxy error"
	if err != nil {
		msg = err.Error()
		var nsErr *NoModelSupportError
		switch {
		case errors.Is(err, ErrCircuitOpen):
			status = http.StatusServiceUnavailable
		case errors.Is(err, ErrNoRoute):
			status = http.StatusNotFound
		case errors.Is(err, ErrBodyTooLarge):
			status = http.StatusRequestEntityTooLarge
		case errors.As(err, &nsErr):
			status = http.StatusBadRequest
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(fmt.Sprintf(`{"error":{"message":%q,"type":"gateway_proxy_error","code":"%d"}}`, msg, status)))
}
