package plugins

import (
	"fmt"
	"net/http"
	"os"
	"runtime"

	"github.com/dezd-ai/ai-gateway-core/config"
	"github.com/dezd-ai/ai-gateway-core/kernel"
)

// AuthVKeyPlugin 提取 Bearer vKey，在内存 O(1) Map 中鉴权，
// 并将身份、部门、模型白名单与多维画像标签注入 PluginContext。
type AuthVKeyPlugin struct {
	kernel.BasePlugin
}

func init() {
	kernel.RegisterPlugin("auth_vkey", func() kernel.Plugin { return &AuthVKeyPlugin{} })
}

func (p *AuthVKeyPlugin) Name() string { return "auth_vkey" }

func (p *AuthVKeyPlugin) PreRequest(ctx *kernel.PluginContext, req *http.Request) error {
	cfg, _ := ctx.GetValue(kernel.CtxConfig).(*config.Config)

	vkey := extractBearer(req.Header.Get("Authorization"))
	if vkey == "" {
		ctx.Abort(http.StatusUnauthorized, gateErrorJSON("invalid_request_error", "缺少 Bearer vKey", "missing_api_key"))
		return nil
	}
	key, ok := cfg.Auth.Keys[vkey]
	if !ok {
		ctx.Abort(http.StatusUnauthorized, gateErrorJSON("invalid_request_error", "vKey 校验失败", "invalid_api_key"))
		return nil
	}

	// 硬件底层 HAL 指纹校验（TPM2.0 / TEE），不一致触发自我熔断。
	if cfg.Auth.HALEnabled {
		if err := verifyHALFingerprint(key.HardwareFingerprint); err != nil {
			ctx.Abort(http.StatusUnauthorized, gateErrorJSON("self_lockdown", "设备硬件指纹校验失败: "+err.Error(), "hal_mismatch"))
			return nil
		}
	}

	// 吊销：disabled 的 key 立即失效（roadmap R1）
	if key.Disabled {
		ctx.Abort(http.StatusForbidden, gateErrorJSON("permission_error", "vKey 已禁用", "key_disabled"))
		return nil
	}

	// 请求体上限（agent 感知）：在鉴权后判定，无需读 body
	//（原网关入口早检改为此处，因 agent 放大需知道 key 身份，见 docs/agent-mode.md）
	if maxBody := effectiveBodyLimit(cfg, ctx); maxBody > 0 && req.ContentLength > maxBody {
		ctx.Abort(http.StatusRequestEntityTooLarge, gateErrorJSON("invalid_request_error", "request body too large", "request_too_large"))
		return nil
	}

	// QPS 限流（令牌桶，跨热加载保留）
	if key.RateLimit > 0 {
		if rl, ok := ctx.GetValue(kernel.CtxRateLimiter).(*kernel.RateLimiter); ok {
			if !rl.Allow(key.VKey, key.RateLimit) {
				ctx.Abort(http.StatusTooManyRequests, gateErrorJSON("rate_limit_error", "请求过于频繁，请稍后重试", "rate_limited"))
				return nil
			}
		}
	}

	// 每日 token 配额
	if key.QuotaDaily > 0 {
		if qt, ok := ctx.GetValue(kernel.CtxQuota).(*kernel.QuotaTracker); ok {
			if qt.Exceeded(key.VKey, key.QuotaDaily) {
				ctx.Abort(http.StatusTooManyRequests, gateErrorJSON("quota_exceeded", "今日配额已用尽", "quota_exceeded"))
				return nil
			}
		}
	}

	ctx.Set(kernel.CtxAuthKey, key)
	return nil
}

// verifyHALFingerprint 预留对接硬件 HAL 接口。Mac 无 TPM/TEE 设备时优雅跳过。
func verifyHALFingerprint(expected string) error {
	if expected == "" {
		return nil
	}
	if runtime.GOOS == "darwin" {
		fmt.Println("[WARN] Mac 环境无 TPM2.0/TEE 硬件，跳过 HAL 指纹校验（非致命）")
		return nil
	}
	actual, err := readDeviceFingerprint("/dev/tpm0")
	if err != nil {
		fmt.Printf("[WARN] 读取 HAL 硬件指纹失败: %v，跳过校验\n", err)
		return nil
	}
	if actual != expected {
		return fmt.Errorf("硬件指纹不一致，触发自我熔断")
	}
	return nil
}

func readDeviceFingerprint(device string) (string, error) {
	b, err := os.ReadFile(device)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
