package kernel

import (
	"testing"

	"github.com/DezDing/ai-gateway-core/config"
)

func TestDeriveInflight(t *testing.T) {
	if got := deriveInflight(0); got != 1024 {
		t.Fatalf("totalMem=0 应返回默认 1024, got %d", got)
	}
	if got := deriveInflight(10 * 1024 * 1024); got != 16 {
		t.Fatalf("小内存应 clamp 到 16, got %d", got)
	}
	if got := deriveInflight(1 << 30); got != 204 { // 1GB → 204.8 → 204
		t.Fatalf("1GB 应推导 204, got %d", got)
	}
	if got := deriveInflight(1 << 40); got != 4096 { // 1TB → clamp 4096
		t.Fatalf("超大内存应 clamp 到 4096, got %d", got)
	}
}

func TestMemGuardAcquire(t *testing.T) {
	g := &MemGuard{maxInflight: 2, hysteresis: 1}
	for i := 0; i < 2; i++ {
		if _, ok := g.TryAcquire(); !ok {
			t.Fatalf("第 %d 个应放行", i)
		}
	}
	if _, ok := g.TryAcquire(); ok {
		t.Fatal("满应拒绝")
	}
	g.Release()
	if _, ok := g.TryAcquire(); !ok {
		t.Fatal("释放后应可获取")
	}
}

// TestMemGuardHardModeHysteresis 验证硬阈值 + 迟滞：连续 N 次超阈值才熔断，回落恢复。
func TestMemGuardHardModeHysteresis(t *testing.T) {
	mem := uint64(95) // 高于硬阈值 90
	g := &MemGuard{
		maxInflight: 100, softBytes: 80, hardBytes: 90, totalMem: 100,
		hysteresis: 3, readMem: func() uint64 { return mem },
	}
	// 未满 3 次采样：不熔断
	g.sample()
	g.sample()
	if g.hardMode || g.softMode {
		t.Fatal("未达迟滞次数不应熔断")
	}
	// 第 3 次：硬熔断
	g.sample()
	if !g.hardMode {
		t.Fatal("连续 3 次超硬阈值应熔断")
	}
	if reason, ok := g.TryAcquire(); ok || reason != "memory_hard" {
		t.Fatalf("硬熔断应拒绝, reason=%q ok=%v", reason, ok)
	}

	// 回落：3 次低于阈值后恢复
	mem = 10
	g.sample()
	g.sample()
	g.sample()
	if g.hardMode || g.softMode {
		t.Fatal("回落 3 次应恢复")
	}
	if _, ok := g.TryAcquire(); !ok {
		t.Fatal("恢复后应放行")
	}
}

// TestMemGuardSoftModeHalvesLimit 验证软阈值降并发：有效上限减半而非直接拒绝。
func TestMemGuardSoftModeHalvesLimit(t *testing.T) {
	g := &MemGuard{
		maxInflight: 100, softBytes: 80, hardBytes: 90, totalMem: 100,
		hysteresis: 1, readMem: func() uint64 { return 85 },
	}
	g.sample()
	if !g.softMode {
		t.Fatal("超软阈值应进入降并发模式")
	}
	// 有效上限 = 50
	for i := 0; i < 50; i++ {
		if _, ok := g.TryAcquire(); !ok {
			t.Fatalf("第 %d 个应放行（软模式上限 50）", i)
		}
	}
	if _, ok := g.TryAcquire(); ok {
		t.Fatal("软模式第 51 个应拒绝")
	}
}

func TestNewMemGuardAutoDerive(t *testing.T) {
	g := NewMemGuard(config.Capacity{})
	st := g.Stats()
	if v, ok := st["max_inflight"].(int); !ok || v <= 0 {
		t.Fatalf("自动推导应给出正并发上限, got %v", st["max_inflight"])
	}
	g.Stop()
}
