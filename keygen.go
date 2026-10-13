package main

import (
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"strings"
)

// runKeygen 生成随机 vKey 并打印可直接粘贴到 config.yaml auth.keys 的片段（roadmap R1）。
// 设计见 docs/backlog.md 1.6：key 可查、可作废、有审计兜底，不做"一次性显示"。
func runKeygen(args []string) {
	fs := flag.NewFlagSet("keygen", flag.ExitOnError)
	username := fs.String("username", "", "用户名")
	department := fs.String("department", "", "部门")
	whitelist := fs.String("whitelist", "", "模型白名单，逗号分隔")
	tags := fs.String("tags", "", "标签，逗号分隔")
	rateLimit := fs.Int("rate_limit", 0, "QPS 限流（每秒请求数），0=不限")
	quotaDaily := fs.Int64("quota_daily", 0, "每日 token 配额，0=不限")
	agent := fs.Bool("agent", false, "标记为 agent key（宽松模式，docs/agent-mode.md）")
	_ = fs.Parse(args)

	vkey := "sk-" + randomHex(32)

	fmt.Println("已生成 vKey（可随时查看，泄露/丢失可作废重新生成）：")
	fmt.Println("--------------------------------------------------------------")
	fmt.Printf("    - vkey: %q\n", vkey)
	if *username != "" {
		fmt.Printf("      username: %q\n", *username)
	}
	if *department != "" {
		fmt.Printf("      department: %q\n", *department)
	}
	if *whitelist != "" {
		fmt.Printf("      model_whitelist: [%s]\n", quoteList(splitCSV(*whitelist)))
	}
	if *tags != "" {
		fmt.Printf("      tags: [%s]\n", quoteList(splitCSV(*tags)))
	}
	if *rateLimit > 0 {
		fmt.Printf("      rate_limit: %d\n", *rateLimit)
	}
	if *quotaDaily > 0 {
		fmt.Printf("      quota_daily: %d\n", *quotaDaily)
	}
	if *agent {
		fmt.Println("      agent_mode: true   # agent 宽松模式（治理保留、干预为零）")
	}
	fmt.Println("--------------------------------------------------------------")
	fmt.Println("请把上面片段粘贴到 config.yaml 的 auth.keys 下（热加载自动生效）。")
	fmt.Println("吊销：给该条目加 disabled: true；或删除该条目。")
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand 失败几乎不可能，退化返回固定串（避免空 key）
		return strings.Repeat("0", n*2)
	}
	return hex.EncodeToString(b)
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func quoteList(ss []string) string {
	quoted := make([]string, len(ss))
	for i, s := range ss {
		quoted[i] = fmt.Sprintf("%q", s)
	}
	return strings.Join(quoted, ", ")
}
