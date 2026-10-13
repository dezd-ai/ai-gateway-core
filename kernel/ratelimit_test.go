package kernel

import (
	"path/filepath"
	"testing"
)

func TestRateLimiterAllow(t *testing.T) {
	rl := NewRateLimiter()
	// rate=2：burst=2，前 2 个放行，第 3 个拒绝
	if !rl.Allow("k", 2) {
		t.Fatal("第 1 个应放行")
	}
	if !rl.Allow("k", 2) {
		t.Fatal("第 2 个应放行")
	}
	if rl.Allow("k", 2) {
		t.Fatal("burst 用尽后应拒绝")
	}
	// 不同 key 独立
	if !rl.Allow("k2", 2) {
		t.Fatal("不同 key 应独立")
	}
	// rate<=0 不限流
	for i := 0; i < 100; i++ {
		if !rl.Allow("k3", 0) {
			t.Fatal("rate<=0 应不限流")
		}
	}
}

func TestQuotaTracker(t *testing.T) {
	p := filepath.Join(t.TempDir(), "quota.json")
	qt := NewQuotaTracker(p)
	if qt.Exceeded("k", 100) {
		t.Fatal("初始应未超配额")
	}
	qt.Add("k", 60)
	if qt.Exceeded("k", 100) {
		t.Fatal("60 < 100 应未超")
	}
	qt.Add("k", 50)
	if !qt.Exceeded("k", 100) {
		t.Fatal("110 >= 100 应超")
	}
	// 持久化：重新加载应保留已用额度
	qt2 := NewQuotaTracker(p)
	if !qt2.Exceeded("k", 100) {
		t.Fatal("重载后应保留已用额度")
	}
	// quota<=0 不限
	if qt.Exceeded("k", 0) {
		t.Fatal("quota<=0 应不限")
	}
}
