package kernel

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// RateLimiter 是 vKey 级 QPS 限流器（令牌桶），内存态，跨热加载保留（roadmap R1）。
type RateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*rateBucket
}

type rateBucket struct {
	tokens float64
	last   time.Time
}

func NewRateLimiter() *RateLimiter {
	return &RateLimiter{buckets: map[string]*rateBucket{}}
}

// Allow 令牌桶放行判断：rate = 每秒请求数，burst = rate（允许 1 秒瞬时量）。
// rate <= 0 视为不限流，恒放行。
func (rl *RateLimiter) Allow(vkey string, rate int) bool {
	if rate <= 0 {
		return true
	}
	rl.mu.Lock()
	defer rl.mu.Unlock()

	b, ok := rl.buckets[vkey]
	now := time.Now()
	if !ok {
		rl.buckets[vkey] = &rateBucket{tokens: float64(rate) - 1, last: now}
		return true
	}
	// 按流逝时间补充令牌，上限 burst = rate
	elapsed := now.Sub(b.last).Seconds()
	if elapsed > 0 {
		b.tokens += elapsed * float64(rate)
		if b.tokens > float64(rate) {
			b.tokens = float64(rate)
		}
		b.last = now
	}
	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	return false
}

// QuotaTracker 是 vKey 级每日 token 配额跟踪器，内存态 + JSON 落盘（跨重启保留）。
// 每日日期变化时自动重置。
type QuotaTracker struct {
	mu   sync.Mutex
	path string
	day  string // 当前日期 YYYY-MM-DD
	used map[string]int64
}

type quotaState struct {
	Day  string           `json:"day"`
	Used map[string]int64 `json:"used"`
}

// NewQuotaTracker 从 path 加载配额状态（无文件或过期则从零开始）。
func NewQuotaTracker(path string) *QuotaTracker {
	qt := &QuotaTracker{path: path, used: map[string]int64{}}
	qt.load()
	return qt
}

func todayStr() string {
	return time.Now().Format("2006-01-02")
}

func (qt *QuotaTracker) load() {
	qt.day = todayStr()
	data, err := os.ReadFile(qt.path)
	if err != nil {
		return
	}
	var st quotaState
	if json.Unmarshal(data, &st) != nil {
		return
	}
	if st.Day == qt.day {
		qt.used = st.Used
	}
}

func (qt *QuotaTracker) save() {
	if qt.path == "" {
		return
	}
	st := quotaState{Day: qt.day, Used: qt.used}
	b, err := json.Marshal(st)
	if err != nil {
		return
	}
	dir := filepath.Dir(qt.path)
	if dir != "." && dir != "" {
		_ = os.MkdirAll(dir, 0o755)
	}
	_ = os.WriteFile(qt.path, b, 0o644)
}

// Add 累加某 key 今日已用 token，并落盘。
func (qt *QuotaTracker) Add(vkey string, tokens int64) {
	qt.mu.Lock()
	defer qt.mu.Unlock()
	if qt.day != todayStr() {
		qt.day = todayStr()
		qt.used = map[string]int64{}
	}
	if tokens > 0 {
		qt.used[vkey] += tokens
	}
	qt.save()
}

// Exceeded 判断某 key 是否已用尽配额（quota <= 0 视为不限，恒 false）。
func (qt *QuotaTracker) Exceeded(vkey string, quota int64) bool {
	if quota <= 0 {
		return false
	}
	qt.mu.Lock()
	defer qt.mu.Unlock()
	if qt.day != todayStr() {
		qt.day = todayStr()
		qt.used = map[string]int64{}
	}
	return qt.used[vkey] >= quota
}

// Save 显式落盘（优雅停机时调用）。
func (qt *QuotaTracker) Save() {
	qt.mu.Lock()
	defer qt.mu.Unlock()
	qt.save()
}
