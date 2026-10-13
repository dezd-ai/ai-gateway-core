package plugins

import (
	"encoding/json"
	"strings"

	"github.com/DezDing/ai-gateway-core/config"
	"github.com/DezDing/ai-gateway-core/kernel"
)

// effectiveBodyLimit 计算请求体上限：agent key 且配置了 agent 放大时用 agent 值，否则全局。
// agent_max_request_body_bytes: 0 = 沿用全局（非"不限"，见 docs/agent-mode.md）。
func effectiveBodyLimit(cfg *config.Config, ctx *kernel.PluginContext) int64 {
	if cfg != nil && cfg.Transport.AgentMaxRequestBodyBytes > 0 {
		if key, ok := ctx.GetValue(kernel.CtxAuthKey).(*config.AuthKey); ok && key.AgentMode {
			return cfg.Transport.AgentMaxRequestBodyBytes
		}
	}
	if cfg == nil {
		return 0
	}
	return cfg.Transport.MaxRequestBodyBytes
}

// extractBearer 从 Authorization 头提取 Bearer vKey。
func extractBearer(h string) string {
	const prefix = "Bearer "
	if strings.HasPrefix(h, prefix) {
		return strings.TrimSpace(h[len(prefix):])
	}
	return ""
}

// gateErrorJSON 构造伪装成标准大模型的错误 JSON 体。
func gateErrorJSON(typ, msg, code string) []byte {
	b, _ := json.Marshal(map[string]any{
		"error": map[string]any{"message": msg, "type": typ, "code": code},
	})
	return b
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// estimateTokens 提供无外部依赖的粗略 Token 预估：CJK 每字符 1 token，
// 拉丁连续串按每 4 字符折算 1 token。该值在捕获到官方 usage 后会被强行覆盖。
func estimateTokens(s string) int64 {
	if s == "" {
		return 0
	}
	var total int64
	var latin strings.Builder
	flush := func() {
		if latin.Len() > 0 {
			n := int64(latin.Len())
			total += (n + 3) / 4
			latin.Reset()
		}
	}
	for _, r := range s {
		switch {
		case (r >= 0x4E00 && r <= 0x9FFF) || (r >= 0x3000 && r <= 0x303F) || (r >= 0xFF00 && r <= 0xFFEF):
			flush()
			total++
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			flush()
		default:
			latin.WriteRune(r)
		}
	}
	flush()
	if total == 0 {
		total = 1
	}
	return total
}

// extractPromptText 从 OpenAI messages 数组提取纯文本提示词。
// 兼容两种 content 形态：字符串（纯文本）与数组（多模态：text/image_url/... 部件），
// 数组形态会提取所有 text 部件的文本（backlog #2）。
func extractPromptText(messages any) string {
	list, ok := messages.([]any)
	if !ok {
		return ""
	}
	var sb strings.Builder
	for _, m := range list {
		mm, ok := m.(map[string]any)
		if !ok {
			continue
		}
		appendContentText(&sb, mm["content"])
	}
	return strings.TrimSpace(sb.String())
}

// appendContentText 将 content（string 或 []any 多模态部件）中的文本追加到 sb。
func appendContentText(sb *strings.Builder, content any) {
	switch c := content.(type) {
	case string:
		if c != "" {
			sb.WriteString(c)
			sb.WriteByte('\n')
		}
	case []any:
		for _, part := range c {
			pm, ok := part.(map[string]any)
			if !ok {
				continue
			}
			if t, ok := pm["text"].(string); ok && t != "" {
				sb.WriteString(t)
				sb.WriteByte('\n')
			}
		}
	}
}

// injectTemplate 将企业准则模板无感注入到首个 user 消息（头尾）。
// 兼容 content 为字符串与数组（多模态）两种形态：
//   - 字符串：直接 head + content + tail；
//   - 数组：改写首个 text 部件（head+text+tail），保留 image_url/audio 等部件，绝不覆盖（backlog #1）；
//   - 无 user 消息：追加一条 user 消息（head+tail），不再误注入到 system/assistant。
func injectTemplate(payload map[string]any, head, tail string) map[string]any {
	if head == "" && tail == "" {
		return payload
	}
	msgs, ok := payload["messages"].([]any)
	if !ok || len(msgs) == 0 {
		payload["messages"] = []any{map[string]any{"role": "system", "content": head + tail}}
		return payload
	}
	idx := -1
	for i, m := range msgs {
		if mm, ok := m.(map[string]any); ok && mm["role"] == "user" {
			idx = i
			break
		}
	}
	if idx < 0 {
		// 无 user 消息：追加一条 user 消息（head+tail），不误改 system/assistant
		payload["messages"] = append(msgs, map[string]any{"role": "user", "content": head + tail})
		return payload
	}
	target, ok := msgs[idx].(map[string]any)
	if !ok {
		return payload
	}
	switch content := target["content"].(type) {
	case string:
		target["content"] = head + content + tail
	case []any:
		target["content"] = injectIntoParts(content, head, tail)
	default:
		// 无法识别的 content 形态：以文本部件形式注入，不破坏原结构
		target["content"] = []any{map[string]any{"type": "text", "text": head + tail}}
	}
	return payload
}

// injectIntoParts 在多模态 content 数组的首个 text 部件注入头尾模板，保留其它部件。
// 若无 text 部件，则追加一个 text 部件。
func injectIntoParts(parts []any, head, tail string) []any {
	for _, p := range parts {
		pm, ok := p.(map[string]any)
		if !ok || pm["type"] != "text" {
			continue
		}
		text, _ := pm["text"].(string)
		pm["text"] = head + text + tail
		return parts
	}
	return append(parts, map[string]any{"type": "text", "text": head + tail})
}
