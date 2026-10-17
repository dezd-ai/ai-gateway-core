package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/dezd-ai/ai-gateway-core/plugins" // 触发各插件 init 注册

	"github.com/dezd-ai/ai-gateway-core/bench"
	"github.com/dezd-ai/ai-gateway-core/config"
	"github.com/dezd-ai/ai-gateway-core/doctor"
	"github.com/dezd-ai/ai-gateway-core/kernel"
	"github.com/dezd-ai/ai-gateway-core/mockup"
)

// version 由构建时注入：go build -ldflags "-X main.version=v0.1.0"
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		runServe(nil)
		return
	}
	switch os.Args[1] {
	case "serve":
		runServe(os.Args[2:])
	case "check", "doctor":
		doctor.Run(os.Args[2:])
	case "bench":
		bench.Run(os.Args[2:])
	case "keygen":
		runKeygen(os.Args[2:])
	case "-v", "--version", "version":
		fmt.Printf("ai-gateway-core %s\n", version)
	case "-h", "--help", "help":
		printUsage()
	default:
		runServe(os.Args[1:])
	}
}

func runServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	configPath := fs.String("config", "config.yaml", "配置文件路径")
	mockMode := fs.Bool("mock", false, "将上游重定向到内置 Mock 流式服务，便于无真实密钥闭环验证")
	_ = fs.Parse(args)

	// 容器/首次运行体验：默认 config.yaml 不存在时回退到随仓库发布的 config.example.yaml。
	// 仅对默认路径生效（显式 -config 指定的路径不存在时仍然报错，避免掩盖配置错误）。
	resolvedCfg := *configPath
	if _, statErr := os.Stat(resolvedCfg); statErr != nil && resolvedCfg == "config.yaml" {
		if _, err2 := os.Stat("config.example.yaml"); err2 == nil {
			resolvedCfg = "config.example.yaml"
			fmt.Println("[WARN] 未找到 config.yaml，回退使用 config.example.yaml（示例配置：上游密钥需通过环境变量或挂载真实配置提供）")
		}
	}

	var g *kernel.Gateway
	var err error
	if *mockMode {
		var cfg *config.Config
		cfg, err = config.Load(resolvedCfg)
		if err == nil {
			mock := mockup.NewLLMServer()
			defer mock.Close()
			for _, u := range cfg.Upstreams {
				u.BaseURL = mock.URL
				u.EndpointPath = ""
				u.Protocol = "openai"
				u.AuthMode = "direct"
				u.APIKey = "mock-key"
			}
			g, err = kernel.NewGatewayFromConfig(cfg, resolvedCfg)
		}
	} else {
		g, err = kernel.NewGateway(resolvedCfg)
	}
	if err != nil {
		fmt.Printf("[FATAL] 网关启动失败: %v\n", err)
		os.Exit(1)
	}
	// Mock 模式下上游为内存重定向，不监听 config.yaml，避免热加载覆盖 Mock 快照。
	if !*mockMode {
		g.HotReload(2 * time.Second)
	}

	stopWatchdog := make(chan struct{})
	go kernel.WatchdogLoop(g.Config().Watchdog, stopWatchdog)

	addr := fmt.Sprintf("%s:%d", g.Config().Server.Host, g.Config().Server.Port)
	// 慢客户端/header 轰炸防护（内存防线配套）；WriteTimeout 不设（0），避免打断 SSE 流式长连接
	srv := &http.Server{
		Addr:              addr,
		Handler:           g,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20, // 1 MiB
	}

	fmt.Println("================================================================")
	fmt.Println("  AI-Gateway-Core v0.1 已启动")
	fmt.Printf("  监听地址   : http://%s\n", addr)
	fmt.Printf("  日志路径   : %s\n", kernel.ResolveLogPath(g.Config().Log))
	fmt.Printf("  容量       : %s\n", capacityLine(g.MemStats()))
	fmt.Printf("  热加载     : 已启用（后台监听 %s 变动，无中断刷新）\n", resolvedCfg)
	fmt.Println("================================================================")

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sig
		fmt.Println("\n[INFO] 收到退出信号，优雅停机中...")
		close(stopWatchdog)
		_ = srv.Close()
		g.Shutdown()
		os.Exit(0)
	}()

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fmt.Printf("[FATAL] HTTP 服务异常: %v\n", err)
		os.Exit(1)
	}
}

func capacityLine(st map[string]any) string {
	if len(st) == 0 {
		return "（不可用）"
	}
	return fmt.Sprintf("max_inflight=%v，内存软阈值=%vB / 硬阈值=%vB",
		st["max_inflight"], st["soft_bytes"], st["hard_bytes"])
}

func printUsage() {
	fmt.Print(`AI-Gateway-Core v0.1  用法:
  ai-gateway-core                 启动网关（默认 serve）
  ai-gateway-core serve           启动网关（-config 指定配置）
  ai-gateway-core check           运行 Doctor CLI 诊断（-config 指定配置）
  ai-gateway-core bench           运行高并发基准压测（-config -workers -duration -port）
  ai-gateway-core keygen          生成随机 vKey（-username -department -whitelist -tags -rate_limit -quota_daily -agent）
  ai-gateway-core version         打印版本号
`)
}
