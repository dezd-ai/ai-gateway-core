package mockup

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"time"
)

// NewLLMServer 返回一个内存级 OpenAI 兼容流式服务：
// 输出若干 content chunk → 带 cached_tokens 的官方 usage chunk → [DONE]。
// 供 Doctor 干跑演练与 Bench 压测使用，避免依赖真实服务商。
func NewLLMServer() *httptest.Server {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

		chunks := []string{
			`data: {"id":"chatcmpl-mock","object":"chat.completion.chunk","model":"mock","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
			`data: {"id":"chatcmpl-mock","object":"chat.completion.chunk","model":"mock","choices":[{"index":0,"delta":{"content":"零拷贝"},"finish_reason":null}]}`,
			`data: {"id":"chatcmpl-mock","object":"chat.completion.chunk","model":"mock","choices":[{"index":0,"delta":{"content":"与"},"finish_reason":null}]}`,
			`data: {"id":"chatcmpl-mock","object":"chat.completion.chunk","model":"mock","choices":[{"index":0,"delta":{"content":"背压"},"finish_reason":null}]}`,
			`data: {"id":"chatcmpl-mock","object":"chat.completion.chunk","model":"mock","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":42,"completion_tokens":6,"total_tokens":48,"prompt_tokens_details":{"cached_tokens":18}}}`,
			`data: [DONE]`,
		}
		for _, c := range chunks {
			if _, err := fmt.Fprintf(w, "%s\n\n", c); err != nil {
				return
			}
			flusher.Flush()
			time.Sleep(2 * time.Millisecond)
		}
	})
	return httptest.NewServer(h)
}

// FaultScenario 是故障注入上游的可选场景（fallback 测试矩阵用，见 docs/fallback-design.md 第 11 节）。
type FaultScenario int

const (
	FaultOK       FaultScenario = iota
	Fault500                    // 返回 500（服务端错误）
	Fault429                    // 返回 429（限流）
	FaultReset                  // 建立连接后立即断开（连接级失败，未返回任何响应）
	FaultSlow                   // 挂起不响应，直到请求被取消（TTFT 超时）
	FaultTruncate               // SSE 200 后流中途断（mid-stream，未见 [DONE]）
)

// NewFaultServer 返回按场景注入故障的内存上游，供 fallback 故障注入测试使用。
func NewFaultServer(s FaultScenario) *httptest.Server {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch s {
		case Fault500:
			w.WriteHeader(http.StatusInternalServerError)
		case Fault429:
			w.WriteHeader(http.StatusTooManyRequests)
		case FaultReset:
			// 直接断开连接，不写任何响应
			if hj, ok := w.(http.Hijacker); ok {
				conn, _, err := hj.Hijack()
				if err == nil {
					_ = conn.Close()
				}
			}
		case FaultSlow:
			// 挂起直到客户端取消（TTFT 超时）；5s 上限兜底，避免 Close 阻塞
			select {
			case <-r.Context().Done():
			case <-time.After(5 * time.Second):
			}
		case FaultTruncate:
			w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
			if fl, ok := w.(http.Flusher); ok {
				fl.Flush()
			}
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"半截\"}}]}\n\n")
			if fl, ok := w.(http.Flusher); ok {
				fl.Flush()
			}
			// 不写 [DONE] 直接返回 → mid-stream 断流
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "ok")
		}
	})
	return httptest.NewServer(h)
}
