package main

import (
	"os"
	"strconv"
	"strings"
)

// loadCostRates 从 ai-gateway-core 的 config.yaml 中提取每个上游的 cost_per_1k。
// 只做最小化行扫描，不依赖 ai-gateway-core 模块，保证两个项目彻底解耦。
func loadCostRates(path string) map[string]float64 {
	rates := map[string]float64{}
	data, err := os.ReadFile(path)
	if err != nil {
		return rates
	}
	inUpstreams := false
	current := ""
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		if indent == 0 {
			inUpstreams = trimmed == "upstreams:"
			current = ""
			continue
		}
		if !inUpstreams {
			continue
		}
		if strings.HasPrefix(trimmed, "- name:") {
			current = strings.Trim(strings.TrimSpace(strings.TrimPrefix(trimmed, "- name:")), `"'`)
			continue
		}
		if strings.HasPrefix(trimmed, "cost_per_1k:") && current != "" {
			val := strings.Trim(strings.TrimSpace(strings.TrimPrefix(trimmed, "cost_per_1k:")), `"'`)
			if f, err := strconv.ParseFloat(val, 64); err == nil {
				rates[current] = f
			}
		}
	}
	return rates
}
