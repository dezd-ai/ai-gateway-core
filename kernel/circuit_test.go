package kernel

import (
	"testing"
	"time"
)

func TestCircuitBreakerOpensAfterThreshold(t *testing.T) {
	cb := NewCircuitBreaker(3, 30, 2)
	for i := 0; i < 3; i++ {
		if !cb.Allow() {
			t.Fatalf("第 %d 次应放行", i)
		}
		cb.RecordFailure()
	}
	if cb.Allow() {
		t.Fatal("连续失败达到阈值后应熔断")
	}
	if cb.State() != "open" {
		t.Fatalf("state = %s，应为 open", cb.State())
	}
	// 熔断期间成功记录不改变状态
	cb.RecordSuccess()
	if cb.State() != "open" {
		t.Fatal("open 状态下 RecordSuccess 不应关闭")
	}
}

func TestCircuitBreakerHalfOpenCloses(t *testing.T) {
	cb := NewCircuitBreaker(1, 30, 2)
	cb.openTimeout = 0 // 直接进入 half_open，便于测试
	cb.RecordFailure()
	if !cb.Allow() {
		t.Fatal("half_open 应放行探测请求")
	}
	if cb.State() != "half_open" {
		t.Fatalf("state = %s，应为 half_open", cb.State())
	}
	cb.RecordSuccess()
	if cb.State() != "closed" {
		t.Fatalf("探测成功后应恢复 closed，实际 %s", cb.State())
	}
}

func TestJitterBackoffGrows(t *testing.T) {
	d0 := JitterBackoff(time.Second, 0)
	d2 := JitterBackoff(time.Second, 2)
	// attempt=0 的基数 1s，attempt=2 的基数 4s，加抖动后仍必然更大
	if d2 <= d0 {
		t.Fatalf("退避应随次数增长: d0=%v d2=%v", d0, d2)
	}
}
