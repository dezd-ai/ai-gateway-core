package main

import (
	"encoding/json"
	"os"
	"sort"
	"sync"
	"time"
)

// Record 对应 ai-gateway-core 审计日志 uba.json 中的一行。
type Record struct {
	TraceID          string   `json:"trace_id"`
	Timestamp        string   `json:"timestamp"`
	Username         string   `json:"username"`
	Department       string   `json:"department"`
	Model            string   `json:"model"`
	Upstream         string   `json:"upstream"`
	ExecutedPlugins  []string `json:"executed_plugins"`
	PromptInjected   bool     `json:"prompt_injected"`
	SecurityCheck    string   `json:"security_check"`
	PromptTokens     int64    `json:"prompt_tokens"`
	CompletionTokens int64    `json:"completion_tokens"`
	TotalTokens      int64    `json:"total_tokens"`
	CachedTokens     int64    `json:"cached_tokens"`
	Precision        string   `json:"precision"`
	DurationMs       int64    `json:"duration_ms"`
	AgentMode        bool     `json:"agent_mode"` // agent 宽松模式 key（成本归属）
	// Fallback 审计（failover 模式，见 docs/fallback-design.md）
	FallbackChain  []string `json:"fallback_chain"`
	Attempts       int64    `json:"attempts"`
	ChainTruncated bool     `json:"chain_truncated"`
	StopReason     string   `json:"stop_reason"`
	// 拦截审计（PreRequest 拦截，见 docs/backlog.md #4；正常记录缺省这些字段）
	HTTPStatus  int    `json:"http_status"`
	Success     *bool  `json:"success"`
	AbortReason string `json:"abort_reason"`
}

// GroupStat 是按用户/模型/上游聚合后的分组统计。
type GroupStat struct {
	Key              string  `json:"key"`
	Requests         int64   `json:"requests"`
	PromptTokens     int64   `json:"prompt_tokens"`
	CompletionTokens int64   `json:"completion_tokens"`
	TotalTokens      int64   `json:"total_tokens"`
	Cost             float64 `json:"cost"`
}

// Stats 是整体汇总。
type Stats struct {
	TotalRequests    int64       `json:"total_requests"`
	PromptTokens     int64       `json:"prompt_tokens"`
	CompletionTokens int64       `json:"completion_tokens"`
	TotalTokens      int64       `json:"total_tokens"`
	CachedTokens     int64       `json:"cached_tokens"`
	Cost             float64     `json:"cost"`
	CacheHitRequests int64       `json:"cache_hit_requests"`
	AvgDurationMs    float64     `json:"avg_duration_ms"`
	InjectedRequests int64       `json:"injected_requests"`
	ByUser           []GroupStat `json:"by_user"`
	ByModel          []GroupStat `json:"by_model"`
	ByUpstream       []GroupStat `json:"by_upstream"`
}

// Filter 是记录查询过滤条件。
type Filter struct {
	Username string
	Model    string
	Upstream string
	Limit    int
}

// Store 以 RWMutex 保护内存快照，周期重读日志与成本费率。
type Store struct {
	mu         sync.RWMutex
	records    []Record
	logPath    string
	configPath string
	rates      map[string]float64
}

func NewStore(logPath, configPath string) *Store {
	s := &Store{logPath: logPath, configPath: configPath, rates: map[string]float64{}}
	s.reload()
	return s
}

// Start 启动后台周期刷新。
func (s *Store) Start(interval time.Duration) {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for range ticker.C {
			s.reload()
		}
	}()
}

// reload 重读日志与成本费率。
func (s *Store) reload() {
	records := readRecords(s.logPath)
	rates := loadCostRates(s.configPath)

	s.mu.Lock()
	s.records = records
	s.rates = rates
	s.mu.Unlock()
}

func readRecords(path string) []Record {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []Record
	for _, line := range splitLines(data) {
		if len(line) == 0 {
			continue
		}
		var r Record
		if err := json.Unmarshal(line, &r); err == nil && r.TraceID != "" {
			out = append(out, r)
		}
	}
	return out
}

func splitLines(data []byte) [][]byte {
	var out [][]byte
	start := 0
	for i := 0; i < len(data); i++ {
		if data[i] == '\n' {
			out = append(out, data[start:i])
			start = i + 1
		}
	}
	if start < len(data) {
		out = append(out, data[start:])
	}
	return out
}

// Rate 返回指定上游的成本费率。
func (s *Store) Rate(upstream string) (float64, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.rates[upstream]
	return r, ok
}

// Records 返回过滤后的记录（最新在前）。
func (s *Store) Records(f Filter) []Record {
	s.mu.RLock()
	recs := s.records
	s.mu.RUnlock()

	out := make([]Record, 0, len(recs))
	for i := len(recs) - 1; i >= 0; i-- {
		r := recs[i]
		if f.Username != "" && r.Username != f.Username {
			continue
		}
		if f.Model != "" && r.Model != f.Model {
			continue
		}
		if f.Upstream != "" && r.Upstream != f.Upstream {
			continue
		}
		out = append(out, r)
		if f.Limit > 0 && len(out) >= f.Limit {
			break
		}
	}
	return out
}

// Trace 按 trace_id 查找单条记录。
func (s *Store) Trace(id string) (Record, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, r := range s.records {
		if r.TraceID == id {
			return r, true
		}
	}
	return Record{}, false
}

// Stats 计算整体汇总与分组聚合。
func (s *Store) Stats() Stats {
	s.mu.RLock()
	recs := s.records
	rates := s.rates
	s.mu.RUnlock()

	var st Stats
	byUser := map[string]*GroupStat{}
	byModel := map[string]*GroupStat{}
	byUpstream := map[string]*GroupStat{}

	for _, r := range recs {
		st.TotalRequests++
		st.PromptTokens += r.PromptTokens
		st.CompletionTokens += r.CompletionTokens
		st.TotalTokens += r.TotalTokens
		st.CachedTokens += r.CachedTokens
		st.AvgDurationMs += float64(r.DurationMs)
		if r.CachedTokens > 0 {
			st.CacheHitRequests++
		}
		if r.PromptInjected {
			st.InjectedRequests++
		}
		cost := costOf(r.TotalTokens, rates[r.Upstream])
		st.Cost += cost

		acc(&byUser, r.Username, r, cost)
		acc(&byModel, r.Model, r, cost)
		acc(&byUpstream, r.Upstream, r, cost)
	}

	if st.TotalRequests > 0 {
		st.AvgDurationMs /= float64(st.TotalRequests)
	}
	st.ByUser = sortGroups(byUser)
	st.ByModel = sortGroups(byModel)
	st.ByUpstream = sortGroups(byUpstream)
	return st
}

func costOf(totalTokens int64, rate float64) float64 {
	if rate <= 0 {
		return 0
	}
	return float64(totalTokens) / 1000.0 * rate
}

func acc(m *map[string]*GroupStat, key string, r Record, cost float64) {
	if key == "" {
		key = "(未知)"
	}
	g := (*m)[key]
	if g == nil {
		g = &GroupStat{Key: key}
		(*m)[key] = g
	}
	g.Requests++
	g.PromptTokens += r.PromptTokens
	g.CompletionTokens += r.CompletionTokens
	g.TotalTokens += r.TotalTokens
	g.Cost += cost
}

func sortGroups(m map[string]*GroupStat) []GroupStat {
	out := make([]GroupStat, 0, len(m))
	for _, g := range m {
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TotalTokens > out[j].TotalTokens })
	return out
}
