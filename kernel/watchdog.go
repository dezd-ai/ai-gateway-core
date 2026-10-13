package kernel

import (
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/DezDing/ai-gateway-core/config"
)

// ResolveLogPath 依据运行环境自适应日志路径：Mac 降级写入项目内 logs/ 目录。
func ResolveLogPath(cfg config.Log) string {
	if cfg.Path != "" {
		return cfg.Path
	}
	if runtime.GOOS == "darwin" {
		return "./logs/uba.json"
	}
	return "/var/log/ai-gateway/uba.json"
}

// WatchdogLoop 在确定性时间周期槽位向硬件看门狗喂狗。
// Mac 无 /dev/watchdog 时优雅跳过并仅打印 WARN，绝不 panic。
func WatchdogLoop(cfg config.Watchdog, stop <-chan struct{}) {
	if !cfg.Enabled {
		return
	}
	device := cfg.Device
	if device == "" {
		device = "/dev/watchdog"
	}
	if runtime.GOOS == "darwin" {
		if _, err := os.Stat(device); err != nil {
			fmt.Println("[WARN] Mac 环境未检测到物理看门狗", device, "，跳过喂狗逻辑（非致命）")
			return
		}
	}
	f, err := os.OpenFile(device, os.O_WRONLY, 0)
	if err != nil {
		fmt.Printf("[WARN] 打开看门狗设备 %s 失败: %v，跳过喂狗（非致命）\n", device, err)
		return
	}
	defer f.Close()

	interval := time.Duration(cfg.IntervalSec) * time.Second
	if interval <= 0 {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if _, err := f.Write([]byte{0}); err != nil {
				fmt.Printf("[WARN] 喂狗失败: %v\n", err)
			}
		case <-stop:
			return
		}
	}
}
