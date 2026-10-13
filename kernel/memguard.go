package kernel

import (
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/DezDing/ai-gateway-core/config"
)

// MemGuard 是内存防线层 3（并发准入）+ 层 4（水位熔断）的实现（docs/backlog.md「1.5」）。
// 阈值默认按系统内存自动推导（cgroup 感知），config 显式值优先；水位触发带迟滞防抖动。
type MemGuard struct {
	mu          sync.Mutex
	maxInflight int
	inFlight    int
	softMode    bool
	hardMode    bool
	softHit     int
	hardHit     int
	softBytes   uint64
	hardBytes   uint64
	totalMem    uint64

	hysteresis int
	interval   time.Duration
	readMem    func() uint64 // 可注入（测试）；nil 时用平台实现
	stop       chan struct{}
	stopped    bool
}

// NewMemGuard 依据配置构建容量守卫：0 值自动推导，显式值优先。
func NewMemGuard(cfg config.Capacity) *MemGuard {
	total := readTotalMemory()

	inflight := cfg.MaxInflight
	if inflight <= 0 {
		inflight = deriveInflight(total) // 每 5MB 1 并发，clamp 16~4096
	}

	softPct := cfg.MemSoftPercent
	if softPct <= 0 {
		softPct = 80
	}
	hardPct := cfg.MemHardPercent
	if hardPct <= 0 {
		hardPct = 90
	}
	hy := cfg.HysteresisSamples
	if hy <= 0 {
		hy = 3
	}

	return &MemGuard{
		maxInflight: inflight,
		softBytes:   total * uint64(softPct) / 100,
		hardBytes:   total * uint64(hardPct) / 100,
		totalMem:    total,
		hysteresis:  hy,
		interval:    time.Second,
		readMem:     nil, // 使用平台实现
		stop:        make(chan struct{}),
	}
}

// Start 启动水位采样协程（层 4）。totalMem 为 0（平台无法读取）时水位熔断不生效，仅准入生效。
func (g *MemGuard) Start() {
	if g.totalMem == 0 {
		return // 无法采样内存：水位熔断禁用，层 3 准入仍生效
	}
	go func() {
		ticker := time.NewTicker(g.interval)
		defer ticker.Stop()
		for {
			select {
			case <-g.stop:
				return
			case <-ticker.C:
				g.sample()
			}
		}
	}()
}

func (g *MemGuard) Stop() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.stopped {
		g.stopped = true
		close(g.stop)
	}
}

func (g *MemGuard) sample() {
	mem := g.currentMem()
	g.mu.Lock()
	defer g.mu.Unlock()

	switch {
	case g.hardBytes > 0 && mem >= g.hardBytes:
		g.hardHit++
		g.softHit = 0
		if g.hardHit >= g.hysteresis {
			g.hardMode = true
		}
	case g.softBytes > 0 && mem >= g.softBytes:
		g.softHit++
		g.hardHit = 0
		if g.softHit >= g.hysteresis {
			g.softMode = true
			g.hardMode = false
		}
	default:
		// 回落
		if g.softHit > 0 {
			g.softHit--
		}
		if g.hardHit > 0 {
			g.hardHit--
		}
		if g.softHit == 0 && g.hardHit == 0 {
			g.softMode = false
			g.hardMode = false
		}
	}
}

// TryAcquire 层 3+4 联合准入：硬阈值拒绝 / 软阈值降并发（上限减半）/ 信号量满拒绝。
// 返回 (拒绝原因, 是否放行)；放行后调用方必须 Release。
func (g *MemGuard) TryAcquire() (string, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.hardMode {
		return "memory_hard", false
	}
	limit := g.maxInflight
	if g.softMode {
		limit = g.maxInflight / 2
		if limit < 1 {
			limit = 1
		}
	}
	if g.inFlight >= limit {
		return "busy", false
	}
	g.inFlight++
	return "", true
}

func (g *MemGuard) Release() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.inFlight > 0 {
		g.inFlight--
	}
}

func (g *MemGuard) currentMem() uint64 {
	if g.readMem != nil {
		return g.readMem()
	}
	return readCurrentMemory()
}

// Stats 暴露容量快照（层 5 可观测）。
func (g *MemGuard) Stats() map[string]any {
	g.mu.Lock()
	defer g.mu.Unlock()
	limit := g.maxInflight
	if g.softMode {
		limit = g.maxInflight / 2
	}
	return map[string]any{
		"max_inflight":       g.maxInflight,
		"effective_inflight": limit,
		"in_flight":          g.inFlight,
		"soft_mode":          g.softMode,
		"hard_mode":          g.hardMode,
		"soft_bytes":         g.softBytes,
		"hard_bytes":         g.hardBytes,
		"total_mem":          g.totalMem,
	}
}

// --- 内存读取（cgroup 感知） ---

// deriveInflight 每 5MB 内存允许 1 并发，clamp 到 [16, 4096]。
func deriveInflight(totalMem uint64) int {
	if totalMem == 0 {
		return 1024
	}
	n := int(totalMem / (5 * 1024 * 1024))
	if n < 16 {
		n = 16
	}
	if n > 4096 {
		n = 4096
	}
	return n
}

// readTotalMemory 读取可用内存上限：容器内 cgroup v2/v1 限额，裸机 /proc/meminfo，其它平台 0。
func readTotalMemory() uint64 {
	if v := readFileUint("/sys/fs/cgroup/memory.max"); v > 0 { // cgroup v2
		return v
	}
	if v := readFileUint("/sys/fs/cgroup/memory/memory.limit_in_bytes"); v > 0 { // cgroup v1
		return v
	}
	if v := readMeminfoTotal(); v > 0 { // 裸机 Linux
		return v
	}
	return 0 // darwin/windows 等：不推导，走默认
}

// readCurrentMemory 读取当前内存占用：cgroup memory.current 或 /proc/meminfo 近似。
func readCurrentMemory() uint64 {
	if v := readFileUint("/sys/fs/cgroup/memory.current"); v > 0 { // cgroup v2
		return v
	}
	// 近似：MemTotal - MemAvailable
	fields := readMeminfo()
	total := fields["MemTotal"]
	avail := fields["MemAvailable"]
	if total > avail {
		return total - avail
	}
	return 0
}

func readFileUint(path string) uint64 {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	s := strings.TrimSpace(string(b))
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0
	}
	return v
}

func readMeminfoTotal() uint64 {
	return readMeminfo()["MemTotal"]
}

func readMeminfo() map[string]uint64 {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return nil
	}
	out := map[string]uint64{}
	for _, line := range strings.Split(string(b), "\n") {
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		val := strings.TrimSpace(parts[1])
		val = strings.TrimSuffix(val, " kB")
		if v, err := strconv.ParseUint(strings.TrimSpace(val), 10, 64); err == nil {
			out[parts[0]] = v * 1024 // kB → bytes
		}
	}
	return out
}
