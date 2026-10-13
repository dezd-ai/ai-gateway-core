package main

import (
	_ "embed"
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"strconv"
	"time"
)

//go:embed web/index.html
var indexHTML []byte

func main() {
	listen := flag.String("listen", ":9090", "监听地址")
	logPath := flag.String("log", "../logs/uba.json", "审计日志路径")
	configPath := flag.String("config", "../config.yaml", "网关配置文件路径（读取 cost_per_1k）")
	flag.Parse()

	store := NewStore(*logPath, *configPath)
	store.Start(2 * time.Second)

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(indexHTML)
	})
	mux.HandleFunc("/api/records", handleRecords(store))
	mux.HandleFunc("/api/stats", handleStats(store))
	mux.HandleFunc("/api/trace", handleTrace(store))

	log.Printf("AI-Gateway 观测台已启动: http://localhost%s", *listen)
	log.Printf("  审计日志: %s", *logPath)
	log.Printf("  网关配置: %s", *configPath)
	if err := http.ListenAndServe(*listen, mux); err != nil {
		log.Fatalf("服务启动失败: %v", err)
	}
}

type recordOut struct {
	Record
	Cost      float64 `json:"cost"`
	RateKnown bool    `json:"rate_known"`
}

func handleRecords(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		limit, _ := strconv.Atoi(q.Get("limit"))
		if limit <= 0 || limit > 1000 {
			limit = 200
		}
		filter := Filter{
			Username: q.Get("username"),
			Model:    q.Get("model"),
			Upstream: q.Get("upstream"),
			Limit:    limit,
		}
		recs := store.Records(filter)
		out := make([]recordOut, 0, len(recs))
		for _, rec := range recs {
			rate, ok := store.Rate(rec.Upstream)
			out = append(out, recordOut{
				Record:    rec,
				Cost:      costOf(rec.TotalTokens, rate),
				RateKnown: ok && rate > 0,
			})
		}
		writeJSON(w, out)
	}
}

func handleStats(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, store.Stats())
	}
}

func handleTrace(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		if id == "" {
			writeJSON(w, map[string]string{"error": "缺少 trace_id"})
			return
		}
		rec, ok := store.Trace(id)
		if !ok {
			writeJSON(w, map[string]string{"error": "未找到该 trace_id"})
			return
		}
		rate, has := store.Rate(rec.Upstream)
		writeJSON(w, recordOut{Record: rec, Cost: costOf(rec.TotalTokens, rate), RateKnown: has && rate > 0})
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}
