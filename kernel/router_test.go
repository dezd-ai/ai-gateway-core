package kernel

import (
	"testing"
	"time"

	"github.com/DezDing/ai-gateway-core/config"
)

func TestSupportsModel(t *testing.T) {
	u := &config.Upstream{Models: []string{"deepseek-v4-flash", "qwen-turbo"}}
	if !supportsModel(u, "deepseek-v4-flash") {
		t.Fatal("应支持已声明模型")
	}
	if supportsModel(u, "glm-4") {
		t.Fatal("不应支持未声明模型")
	}
	if !supportsModel(u, "") {
		t.Fatal("空模型应放行（不做模型约束）")
	}
	// 未声明 models 视为不限
	if !supportsModel(&config.Upstream{}, "any-model") {
		t.Fatal("未声明 models 应视为支持所有")
	}
}

func TestModelAllowedFilters(t *testing.T) {
	nodes := []*Node{
		{Upstream: &config.Upstream{Models: []string{"a"}}},
		{Upstream: &config.Upstream{Models: []string{"b"}}},
		{Upstream: &config.Upstream{}}, // 不限
	}
	got := modelAllowed(nodes, "a")
	if len(got) != 2 {
		t.Fatalf("应命中 2 个节点，实际 %d", len(got))
	}
	got = modelAllowed(nodes, "c")
	if len(got) != 1 {
		t.Fatalf("仅未限模型的节点应命中，实际 %d", len(got))
	}
}

func TestNoModelSupportError(t *testing.T) {
	err := &NoModelSupportError{Model: "glm-4"}
	if err.Error() != `no upstream supports model "glm-4"` {
		t.Fatalf("错误消息 = %q", err.Error())
	}
}

func TestRoundRobinOrder(t *testing.T) {
	rule := &RouteRule{}
	nodes := []*Node{{}, {}, {}}
	s := roundRobinStrategy{}
	for i := 0; i < 6; i++ {
		n, err := s.Select(nil, rule, nodes)
		if err != nil {
			t.Fatal(err)
		}
		if n != nodes[i%3] {
			t.Fatalf("轮询顺序错误: 第 %d 次应命中 nodes[%d]", i, i%3)
		}
	}
}

// TestLatencyColdStart 验证未测量节点不会因 latency=0 被误当「最快」。
func TestLatencyColdStart(t *testing.T) {
	rule := &RouteRule{}
	measured := &Node{Runtime: &NodeRuntime{}}
	measured.Runtime.RecordLatency(200 * time.Millisecond)
	unmeasured := &Node{Runtime: &NodeRuntime{}}
	nodes := []*Node{measured, unmeasured}

	s := latencyStrategy{}
	for i := 0; i < 3; i++ {
		n, err := s.Select(nil, rule, nodes)
		if err != nil {
			t.Fatal(err)
		}
		if n != measured {
			t.Fatal("有实测延迟时不应选未测量节点")
		}
	}
}

func TestLeastConnection(t *testing.T) {
	rule := &RouteRule{}
	busy := &Node{Runtime: &NodeRuntime{}}
	busy.Runtime.IncConn()
	busy.Runtime.IncConn()
	idle := &Node{Runtime: &NodeRuntime{}}
	nodes := []*Node{busy, idle}

	n, err := leastConnStrategy{}.Select(nil, rule, nodes)
	if err != nil {
		t.Fatal(err)
	}
	if n != idle {
		t.Fatal("应选连接数最少的节点")
	}
}
