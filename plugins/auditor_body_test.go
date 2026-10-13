package plugins

import (
	"io"
	"strings"
	"testing"

	"github.com/DezDing/ai-gateway-core/kernel"
)

// TestJSONAuditBodyTruncation 验证非流式响应审计缓冲上限（内存防线 层 2）：
// 超限停止缓冲但仍转发，usage 解析降级。
func TestJSONAuditBodyTruncation(t *testing.T) {
	kernel.SetAuditStdout(false)
	defer kernel.SetAuditStdout(true)

	big := strings.Repeat("x", 1000)
	ctx := kernel.NewPluginContext()
	b := newJSONAuditBody(io.NopCloser(strings.NewReader(big)), ctx, nil, 100)

	got, err := io.ReadAll(b)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != big {
		t.Fatal("即使超限也应完整转发给客户端")
	}
	if !b.truncated {
		t.Fatal("超限应置 truncated")
	}
	if b.buf.Len() > 100 {
		t.Fatalf("缓冲不应超过 maxBytes, got %d", b.buf.Len())
	}
}
