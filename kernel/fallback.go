package kernel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/DezDing/ai-gateway-core/config"
)

// fallbackPlanKey 在请求 Context 中携带 failover 候选计划。
type fallbackPlanKeyType struct{}

var fallbackPlanKey = fallbackPlanKeyType{}

// fallbackCandidate 是候选链中的单个元素：(上游, 模型) 二元组。
// model 为空表示保持请求原模型（同模型候选）；非空表示降级候选（model_map，需改写 body）。
type fallbackCandidate struct {
	node  *Node
	model string
}

// fallbackPlan 是 failover 模式的请求级失败转移计划（docs/fallback-design.md）。
// 由 Router.RouteWithFallback 在 Director 阶段构建并挂入请求 Context，
// routerRoundTripper 分发到 RoundTrip 执行循环尝试。
type fallbackPlan struct {
	candidates []fallbackCandidate
	fb         *config.Fallback
	origURL    url.URL // 每个尝试前恢复的原始请求 URL（Rewrite 需要幂等重放）
	origBody   []byte  // 存在 model_map 时缓存原始请求体（每尝试重放 + 降级改写）
}

// retryOn 判断某失败类型是否在 retry_on 声明内；未声明 retry_on 时默认全部可重试（4xx 永不）。
func retryOn(fb *config.Fallback, token string) bool {
	if len(fb.RetryOn) == 0 {
		return true
	}
	for _, t := range fb.RetryOn {
		if t == token {
			return true
		}
	}
	return false
}

// agentTTFT 返回 agent 宽松模式的放大首字超时；非 agent 或未配置返回 0（沿用共享 Transport）。
func agentTTFT(ctx *PluginContext) time.Duration {
	if ctx == nil {
		return 0
	}
	key, _ := ctx.GetValue(CtxAuthKey).(*config.AuthKey)
	if key == nil || !key.AgentMode {
		return 0
	}
	cfg, _ := ctx.GetValue(CtxConfig).(*config.Config)
	if cfg == nil || cfg.Transport.AgentTTFTTimeoutSec <= 0 {
		return 0
	}
	return time.Duration(cfg.Transport.AgentTTFTTimeoutSec) * time.Second
}

// RoundTrip 沿候选链循环尝试：状态码分流、连接级同节点重试、总超时预算、每尝试重放 Rewrite。
// 返回语义：
//   - (res, nil)   成功响应，或 4xx/被 retry_on 排除的 429/5xx（作为最终响应返回，客户端可见真实状态）
//   - (nil, err)   全部连接级失败，返回最后错误（由 ErrorHandler 映射为 502/503）
func (p *fallbackPlan) RoundTrip(req *http.Request, ctx *PluginContext) (*http.Response, error) {
	fb := p.fb
	maxAttempts := fb.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = len(p.candidates)
	}

	var deadline time.Time
	if fb.TotalTimeoutMS > 0 {
		deadline = time.Now().Add(time.Duration(fb.TotalTimeoutMS) * time.Millisecond)
	}

	var (
		attempts   int64
		chain      []string
		lastRes    *http.Response
		lastErr    error
		stopReason string
	)

	for i := 0; i < len(p.candidates) && attempts < int64(maxAttempts); i++ {
		cand := p.candidates[i]
		chain = append(chain, cand.node.Upstream.Name)

		if !deadline.IsZero() && time.Now().After(deadline) {
			stopReason = "timeout"
			break
		}

		res, err := p.tryNode(req, ctx, cand, deadline)
		attempts++

		if err != nil {
			class := classifyFailure(nil, err)
			switch {
			case class == classConnect:
				// 连接级失败：请求大概率未送达，先按 connect_retry 同节点重试（无双计费风险），
				// 再决定是否切换下一个候选（由 retry_on 的 "connect" 控制）
				if fb.ConnectRetry > 0 {
					for r := 0; r < fb.ConnectRetry && attempts < int64(maxAttempts); r++ {
						res, err = p.tryNode(req, ctx, cand, deadline)
						attempts++
						if err == nil {
							break
						}
						lastErr = err
					}
				}
				if err == nil {
					// 重试成功 → 落入下方成功分类
				} else if retryOn(fb, "connect") {
					lastErr = err
					continue
				} else {
					if lastErr == nil {
						lastErr = err
					}
					return p.finish(req, nil, lastErr, ctx, chain, attempts, stopReason)
				}
			case class == classTimeout && retryOn(fb, "timeout"):
				lastErr = err
				continue
			default:
				return p.finish(req, nil, err, ctx, chain, attempts, stopReason)
			}
		}

		if err == nil {
			class := classifyFailure(res, nil)
			switch class {
			case classClientError:
				// 4xx：请求本身问题，永不重试/切换
				return p.finish(req, res, nil, ctx, chain, attempts, stopReason)
			case classRateLimit, classServerError:
				token := "5xx"
				if class == classRateLimit {
					token = "429"
				}
				if retryOn(fb, token) {
					lastRes = res
					lastErr = nil
					continue
				}
				// retry_on 未声明该类型：作为最终响应返回（客户端可见真实状态）
				return p.finish(req, res, nil, ctx, chain, attempts, stopReason)
			default:
				return p.finish(req, res, nil, ctx, chain, attempts, stopReason)
			}
		}
	}

	if stopReason == "" {
		stopReason = "exhausted"
	}
	// 全失败：优先返回最后的上游响应（429/5xx 最有信息量），否则返回最后错误
	if lastRes != nil {
		return p.finish(req, lastRes, nil, ctx, chain, attempts, stopReason)
	}
	return p.finish(req, nil, lastErr, ctx, chain, attempts, stopReason)
}

// tryNode 执行一次单节点尝试：恢复原始 URL → 重放原始 body（如启用降级）→
// 重新 Rewrite（凭证/路径）→ 挂载节点 → 应用总超时预算 deadline → RoundTrip。
func (p *fallbackPlan) tryNode(req *http.Request, ctx *PluginContext, cand fallbackCandidate, deadline time.Time) (*http.Response, error) {
	*req.URL = p.origURL

	// body 重放：降级候选改写 model 字段 + 同步 ContentLength（漏了上游直接报错）
	if p.origBody != nil {
		body := p.origBody
		if cand.model != "" {
			body = rewriteModelField(p.origBody, cand.model)
		}
		req.Body = io.NopCloser(bytes.NewReader(body))
		req.ContentLength = int64(len(body))
	}

	if err := cand.node.Rewrite(ctx, req); err != nil {
		return nil, fmt.Errorf("%w: %v", errRewrite, err)
	}

	var cancel context.CancelFunc
	if !deadline.IsZero() {
		var dctx context.Context
		dctx, cancel = context.WithDeadline(req.Context(), deadline)
		*req = *req.WithContext(dctx)
	}
	// 让 ModifyResponse（auditor）能拿到真正服务的节点
	nctx := context.WithValue(req.Context(), nodeCtxKey, cand.node)
	*req = *req.WithContext(nctx)
	ctx.Set(CtxUpstreamName, cand.node.Upstream.Name)

	res, err := cand.node.RoundTripWithTTFT(req, agentTTFT(ctx))
	if cancel != nil {
		cancel()
	}
	return res, err
}

// rewriteModelField 将请求体 JSON 的 model 字段改写为目标模型。
// 非 JSON 体原样返回（降级仅对 JSON 对话体有意义）。
func rewriteModelField(orig []byte, model string) []byte {
	var payload map[string]any
	if err := json.Unmarshal(orig, &payload); err != nil {
		return orig
	}
	payload["model"] = model
	out, err := json.Marshal(payload)
	if err != nil {
		return orig
	}
	return out
}

// finish 统一收尾：记录尝试链/次数/停止原因/截断标记到 Context（供审计），
// 并在发生切换（attempts > 1）且开启透明头时附加响应头。
func (p *fallbackPlan) finish(req *http.Request, res *http.Response, err error, ctx *PluginContext,
	chain []string, attempts int64, stopReason string) (*http.Response, error) {

	ctx.Set(CtxFallbackChain, chain)
	ctx.Set(CtxAttempts, attempts)
	if stopReason != "" {
		ctx.Set(CtxStopReason, stopReason)
	}
	// 截断：max_attempts 小于候选链长，还有候选未被尝试
	ctx.Set(CtxChainTruncated, len(chain) < len(p.candidates))

	if res != nil && p.fb.TransparentHeader && attempts > 1 {
		res.Header.Set("X-AI-Gateway-Fallback", strings.Join(chain, "→"))
		res.Header.Set("X-AI-Gateway-Attempts", strconv.FormatInt(attempts, 10))
	}
	return res, err
}
