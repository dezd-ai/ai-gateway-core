package plugins

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dezd-ai/ai-gateway-core/config"
	"github.com/dezd-ai/ai-gateway-core/kernel"
)

// usageInfo 捕获服务商原生 usage 计费（含国产模型 Prompt Cache 明细）。
type usageInfo struct {
	PromptTokens        int64 `json:"prompt_tokens"`
	CompletionTokens    int64 `json:"completion_tokens"`
	TotalTokens         int64 `json:"total_tokens"`
	PromptTokensDetails struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}

// AuditObserverPlugin 在 ModifyResponse 阶段非阻塞拦截流式响应，绝不一次性读入内存。
type AuditObserverPlugin struct {
	kernel.BasePlugin
}

func init() {
	kernel.RegisterPlugin("auditor", func() kernel.Plugin { return &AuditObserverPlugin{} })
}

func (p *AuditObserverPlugin) Name() string { return "auditor" }

func (p *AuditObserverPlugin) ModifyResponse(ctx *kernel.PluginContext, res *http.Response) error {
	cfg, _ := ctx.GetValue(kernel.CtxConfig).(*config.Config)
	storage, _ := ctx.GetValue(kernel.CtxStorage).(*kernel.EdgeStorage)

	ct := res.Header.Get("Content-Type")
	if strings.Contains(ct, "text/event-stream") {
		idle := time.Duration(cfg.Transport.IdleReadTimeoutSec) * time.Second
		if idle <= 0 {
			idle = 30 * time.Second
		}
		// 挂载真正服务的节点：mid-stream 断流时计入该节点断路器失败（fallback-design.md 第 8 节）
		node, _ := kernel.NodeFromContext(res.Request.Context())
		res.Body = newStreamInterceptor(res.Body, ctx, storage, idle, node)
	} else {
		res.Body = newJSONAuditBody(res.Body, ctx, storage, cfg.Transport.MaxAuditBodyBytes)
	}
	return nil
}

// --- 流式 SSE 拦截器 ---

type streamInterceptor struct {
	src         io.ReadCloser
	ctx         *kernel.PluginContext
	storage     *kernel.EdgeStorage
	node        *kernel.Node // 真正服务本次流的上游节点（断流熔断用）
	idleTimeout time.Duration
	idleTimer   *time.Timer

	lineBuf []byte
	pending []byte

	sawDONE       bool
	injectedMeta  bool
	appendedFinal bool
	finalized     bool
	readTriggered bool // 是否由上游读取错误触发 finalize（客户端断开时为 false）
	idleHit       atomic.Bool

	estCompletion int64
	official      *usageInfo

	once sync.Once
}

func newStreamInterceptor(src io.ReadCloser, ctx *kernel.PluginContext, storage *kernel.EdgeStorage, idle time.Duration, node *kernel.Node) *streamInterceptor {
	s := &streamInterceptor{src: src, ctx: ctx, storage: storage, idleTimeout: idle, node: node}
	if idle > 0 {
		s.idleTimer = time.AfterFunc(idle, func() {
			s.idleHit.Store(true)
			_ = s.src.Close()
		})
	}
	return s
}

func (s *streamInterceptor) Read(p []byte) (int, error) {
	for {
		if len(s.pending) > 0 {
			n := copy(p, s.pending)
			s.pending = s.pending[n:]
			return n, nil
		}
		if s.finalized {
			return 0, io.EOF
		}
		buf := kernel.GetBuffer()
		n, err := s.src.Read(buf)
		if n > 0 {
			s.lineBuf = append(s.lineBuf, buf[:n]...)
			s.processLines()
		}
		kernel.PutBuffer(buf)
		if err != nil {
			s.readTriggered = true // 上游读取失败（网络断/空闲超时触发 close）；客户端断开走 Close 不置位
			s.finalize()
			s.finalized = true
			continue
		}
	}
}

func (s *streamInterceptor) processLines() {
	for {
		idx := bytes.IndexByte(s.lineBuf, '\n')
		if idx < 0 {
			break
		}
		line := s.lineBuf[:idx]
		s.lineBuf = s.lineBuf[idx+1:]
		s.handleLine(line)
	}
}

func (s *streamInterceptor) handleLine(line []byte) {
	trimmed := bytes.TrimRight(line, "\r")
	if bytes.Contains(trimmed, []byte("[DONE]")) {
		s.sawDONE = true
		if !s.injectedMeta && s.shouldInjectMeta() {
			s.injectMeta()
		}
		s.appendRaw(trimmed)
		s.appendRaw(nil)
		return
	}
	if bytes.HasPrefix(trimmed, []byte("data:")) {
		s.parseChunk(bytes.TrimSpace(trimmed[len("data:"):]))
	}
	s.appendRaw(trimmed)
}

// shouldInjectMeta agent 宽松模式默认不注入 gateway_meta（严格解析器兼容，docs/agent-mode.md）。
func (s *streamInterceptor) shouldInjectMeta() bool {
	if s.ctx != nil {
		if key, ok := s.ctx.GetValue(kernel.CtxAuthKey).(*config.AuthKey); ok && key.AgentMode {
			if cfg, ok := s.ctx.GetValue(kernel.CtxConfig).(*config.Config); ok && !cfg.Transport.AgentGatewayMeta {
				return false
			}
		}
	}
	return true
}

// parseChunk 流式解析单个 chunk：增量累加 Token，并并发捕获官方 usage 计费。
func (s *streamInterceptor) parseChunk(payload []byte) {
	if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
		return
	}
	var chunk struct {
		Choices []struct {
			Delta struct {
				Content string `json:"content"`
			} `json:"delta"`
		} `json:"choices"`
		Usage *usageInfo `json:"usage"`
	}
	if err := json.Unmarshal(payload, &chunk); err != nil {
		return
	}
	for _, c := range chunk.Choices {
		if c.Delta.Content != "" {
			s.estCompletion += estimateTokens(c.Delta.Content)
		}
	}
	if chunk.Usage != nil {
		s.official = chunk.Usage
	}
}

func (s *streamInterceptor) appendRaw(line []byte) {
	s.pending = append(s.pending, line...)
	s.pending = append(s.pending, '\n')
}

// injectMeta 在 [DONE] 之前强制注入网关审计 Chunk。
func (s *streamInterceptor) injectMeta() {
	s.injectedMeta = true
	_, _, total, _, _ := s.billing()
	meta, _ := json.Marshal(map[string]any{
		"gateway_meta": map[string]any{
			"trace_id":         s.ctx.GetString(kernel.CtxTraceID),
			"executed_plugins": s.ctx.ExecutedPlugins(),
			"total_tokens":     total,
			"prompt_injected":  s.ctx.GetBool(kernel.CtxPromptInjected),
		},
	})
	s.appendRaw(append([]byte("data: "), meta...))
	s.appendRaw(nil)
}

// finalize 在流结束时补齐异常截断 Chunk 与 [DONE]，并异步输出审计日志。
func (s *streamInterceptor) finalize() {
	if !s.appendedFinal {
		s.appendedFinal = true
		if !s.sawDONE {
			if !s.injectedMeta && s.shouldInjectMeta() {
				s.injectMeta()
			}
			msg := "upstream stream truncated before [DONE]"
			if s.idleHit.Load() {
				msg = "upstream stream idle timeout"
			}
			chunk, _ := json.Marshal(map[string]any{
				"error": map[string]any{"message": msg, "type": "stream_truncated", "code": "gateway_stream_eof"},
			})
			s.appendRaw(append([]byte("data: "), chunk...))
			s.appendRaw(nil)
			s.appendRaw([]byte("data: [DONE]"))
			s.appendRaw(nil)
		}
	}
	s.once.Do(func() {
		if s.idleTimer != nil {
			s.idleTimer.Stop()
		}
		_ = s.src.Close()
		// mid-stream 断流（上游读到错误且未见 [DONE]）计入该节点断路器失败，
		// 否则"每次发 200 然后流中途死"的上游永远不被熔断（fallback-design.md 第 8 节）
		if !s.sawDONE && s.readTriggered && s.node != nil {
			s.node.Runtime.Breaker().RecordFailure()
		}
		go s.emitAudit()
	})
}

func (s *streamInterceptor) Close() error {
	s.finalize()
	s.finalized = true
	return nil
}

func (s *streamInterceptor) billing() (prompt, completion, total, cached int64, precision string) {
	if s.official != nil {
		prompt = s.official.PromptTokens
		completion = s.official.CompletionTokens
		total = s.official.TotalTokens
		if total == 0 {
			total = prompt + completion
		}
		cached = s.official.PromptTokensDetails.CachedTokens
		if cached > 0 {
			precision = "official_cached"
		} else {
			precision = "official"
		}
		return
	}
	prompt = s.ctx.GetInt64(kernel.CtxPromptTokensEst)
	completion = s.estCompletion
	total = prompt + completion
	precision = "estimated"
	return
}

// jsonAuditBody 在转发非流式 JSON 响应的同时缓冲解析 usage 计费。
// maxBytes > 0 时缓冲有上限（内存防线 层 2），超限放弃缓冲、usage 降级 estimated。
type jsonAuditBody struct {
	src       io.ReadCloser
	ctx       *kernel.PluginContext
	storage   *kernel.EdgeStorage
	maxBytes  int64
	buf       bytes.Buffer
	truncated bool
	once      sync.Once
}

func newJSONAuditBody(src io.ReadCloser, ctx *kernel.PluginContext, storage *kernel.EdgeStorage, maxBytes int64) *jsonAuditBody {
	return &jsonAuditBody{src: src, ctx: ctx, storage: storage, maxBytes: maxBytes}
}

func (b *jsonAuditBody) Read(p []byte) (int, error) {
	n, err := b.src.Read(p)
	if n > 0 {
		if !b.truncated {
			if b.maxBytes > 0 && int64(b.buf.Len()+n) > b.maxBytes {
				b.truncated = true // 超限：停止缓冲，但继续向客户端转发
			} else {
				b.buf.Write(p[:n])
			}
		}
	}
	if err == io.EOF {
		b.finalize()
	}
	return n, err
}

func (b *jsonAuditBody) Close() error {
	b.finalize()
	return b.src.Close()
}

func (b *jsonAuditBody) finalize() {
	b.once.Do(func() {
		if b.truncated {
			// 响应过大，放弃解析官方 usage，审计降级为本地预估（precision=estimated）
			go emitAudit(b.ctx, b.storage, nil, 0)
			return
		}
		var resp struct {
			Usage *usageInfo `json:"usage"`
		}
		_ = json.Unmarshal(b.buf.Bytes(), &resp)
		if resp.Usage != nil {
			go emitAudit(b.ctx, b.storage, resp.Usage, 0)
		} else {
			go emitAudit(b.ctx, b.storage, nil, 0)
		}
	})
}

// --- 审计记录生成与落盘 ---

func (s *streamInterceptor) emitAudit() {
	emitAudit(s.ctx, s.storage, s.official, s.estCompletion)
}

// auditStdout 由内核统一持有（kernel.SetAuditStdout / kernel.AuditStdoutEnabled），
// 与拦截审计共用同一开关。此处保留 SetAuditStdout 兼容入口。
func SetAuditStdout(enabled bool) { kernel.SetAuditStdout(enabled) }

func emitAudit(ctx *kernel.PluginContext, storage *kernel.EdgeStorage, official *usageInfo, estCompletion int64) {
	username, department := "", ""
	var authKey *config.AuthKey
	if key, ok := ctx.GetValue(kernel.CtxAuthKey).(*config.AuthKey); ok {
		authKey = key
		username, department = key.Username, key.Department
	}

	var prompt, completion, total, cached int64
	precision := "estimated"
	if official != nil {
		prompt = official.PromptTokens
		completion = official.CompletionTokens
		total = official.TotalTokens
		if total == 0 {
			total = prompt + completion
		}
		cached = official.PromptTokensDetails.CachedTokens
		if cached > 0 {
			precision = "official_cached"
		} else {
			precision = "official"
		}
	} else {
		prompt = ctx.GetInt64(kernel.CtxPromptTokensEst)
		completion = estCompletion
		total = prompt + completion
	}

	start := ctx.StartTime()
	dur := time.Since(start).Milliseconds()
	if dur < 0 {
		dur = 0
	}

	// 配额消耗（roadmap R1）：按实际 total_tokens 累加，跨重启落盘
	if authKey != nil && authKey.QuotaDaily > 0 {
		if qt, ok := ctx.GetValue(kernel.CtxQuota).(*kernel.QuotaTracker); ok {
			qt.Add(authKey.VKey, total)
		}
	}

	rec := map[string]any{
		"trace_id":          ctx.GetString(kernel.CtxTraceID),
		"timestamp":         time.Now().Format(time.RFC3339Nano),
		"username":          username,
		"department":        department,
		"model":             ctx.GetString(kernel.CtxModel),
		"upstream":          ctx.GetString(kernel.CtxUpstreamName),
		"prompt_injected":   ctx.GetBool(kernel.CtxPromptInjected),
		"security_check":    ctx.GetString(kernel.CtxSecurityCheck),
		"prompt_tokens":     prompt,
		"completion_tokens": completion,
		"total_tokens":      total,
		"cached_tokens":     cached,
		"precision":         precision,
		"executed_plugins":  ctx.ExecutedPlugins(),
		"duration_ms":       dur,
		"agent_mode":        authKey != nil && authKey.AgentMode, // agent 宽松模式标记（成本归属）
		// fallback 审计（failover 模式）：实际尝试链/次数/截断/停止原因
		"fallback_chain":  ctx.GetStrings(kernel.CtxFallbackChain),
		"attempts":        ctx.GetInt64(kernel.CtxAttempts),
		"chain_truncated": ctx.GetBool(kernel.CtxChainTruncated),
		"stop_reason":     ctx.GetString(kernel.CtxStopReason),
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return
	}
	if kernel.AuditStdoutEnabled() {
		fmt.Println(string(b))
	}
	if storage != nil {
		storage.Append(b)
	}
}
