package kernel

import (
	"context"
	"errors"
	"net"
	"net/http"
)

// failClass 是失败分类，决定 fallback 对一次尝试采取的动作（docs/fallback-design.md 第 3 节）。
type failClass int

const (
	classSuccess       failClass = iota // 成功
	classClientError                    // 4xx：请求本身问题，永不重试/切换
	classRateLimit                      // 429：直接切，不重试同节点
	classServerError                    // 5xx：直接切，不重试同节点
	classTimeout                        // TTFT/读超时：直接切
	classConnect                        // 连接级失败：可同节点重试 connect_retry 次
	classStreamStarted                  // mid-stream：永不切换，走补齐（由流式拦截器判定）
)

// errRewrite 标记每次尝试前的凭证/URL 重写失败（配置问题，永不重试/切换）。
var errRewrite = errors.New("upstream rewrite failed")

// classifyFailure 将一次 RoundTrip 的结果归类为 fallback 动作依据。
// res == nil 表示传输层失败（此时 err 必非空）；res != nil 时按状态码归类。
func classifyFailure(res *http.Response, err error) failClass {
	if err != nil {
		if errors.Is(err, errRewrite) {
			return classClientError
		}
		// 总超时预算触发的 context deadline 与网络超时同样按 timeout 处理
		if errors.Is(err, context.DeadlineExceeded) {
			return classTimeout
		}
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			return classTimeout
		}
		return classConnect
	}
	switch {
	case res.StatusCode == http.StatusTooManyRequests:
		return classRateLimit
	case res.StatusCode >= 500:
		return classServerError
	case res.StatusCode >= 400:
		return classClientError
	default:
		return classSuccess
	}
}
