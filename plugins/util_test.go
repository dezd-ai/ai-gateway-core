package plugins

import "testing"

func TestExtractBearer(t *testing.T) {
	if extractBearer("Bearer sk-abc") != "sk-abc" {
		t.Fatal("应提取 Bearer 后的 token")
	}
	if extractBearer("") != "" {
		t.Fatal("空头应返回空")
	}
	if extractBearer("Basic abc") != "" {
		t.Fatal("非 Bearer 应返回空")
	}
}

func TestEstimateTokens(t *testing.T) {
	if estimateTokens("") != 0 {
		t.Fatal("空串应为 0")
	}
	if estimateTokens("abcd") != 1 {
		t.Fatalf("4 个拉丁字符应约 1 token，实际 %d", estimateTokens("abcd"))
	}
	if estimateTokens("你好") != 2 {
		t.Fatalf("2 个 CJK 字符应 2 token，实际 %d", estimateTokens("你好"))
	}
}

func TestInjectTemplateHeadTail(t *testing.T) {
	payload := map[string]any{
		"messages": []any{
			map[string]any{"role": "system", "content": "sys"},
			map[string]any{"role": "user", "content": "hello"},
		},
	}
	out := injectTemplate(payload, "HEAD ", " TAIL")
	msgs := out["messages"].([]any)
	user := msgs[1].(map[string]any)
	if user["content"] != "HEAD hello TAIL" {
		t.Fatalf("user content = %q", user["content"])
	}
	// 首个非 user 消息不应被改动
	sys := msgs[0].(map[string]any)
	if sys["content"] != "sys" {
		t.Fatalf("system content 被误改 = %q", sys["content"])
	}
}

func TestInjectTemplateNoMessages(t *testing.T) {
	payload := map[string]any{"model": "m"}
	out := injectTemplate(payload, "HEAD", "")
	msgs, ok := out["messages"].([]any)
	if !ok || len(msgs) != 1 {
		t.Fatalf("无 messages 时应插入 system 消息，实际 %v", out["messages"])
	}
}

// TestExtractPromptTextMultimodal 验证多模态（content 为数组）文本可被提取（backlog #2）。
func TestExtractPromptTextMultimodal(t *testing.T) {
	messages := []any{
		map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "text", "text": "描述这张图"},
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,..."}},
		}},
		map[string]any{"role": "user", "content": "补充说明"},
	}
	got := extractPromptText(messages)
	if got != "描述这张图\n补充说明" {
		t.Fatalf("多模态文本提取 = %q", got)
	}
}

// TestInjectTemplateMultimodal 验证多模态 content 数组注入后图片部件不被覆盖（backlog #1）。
func TestInjectTemplateMultimodal(t *testing.T) {
	payload := map[string]any{
		"messages": []any{
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "text", "text": "看图"},
				map[string]any{"type": "image_url", "image_url": map[string]any{"url": "http://img/1.png"}},
			}},
		},
	}
	out := injectTemplate(payload, "HEAD ", " TAIL")
	msgs := out["messages"].([]any)
	content := msgs[0].(map[string]any)["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("多模态部件数应变 2（不覆盖图片），实际 %d", len(content))
	}
	text := content[0].(map[string]any)["text"]
	if text != "HEAD 看图 TAIL" {
		t.Fatalf("text 部件 = %q", text)
	}
	img, ok := content[1].(map[string]any)["image_url"]
	if !ok || img.(map[string]any)["url"] != "http://img/1.png" {
		t.Fatal("image_url 部件应被保留")
	}
}

// TestInjectTemplateNoUserMessage 验证无 user 消息时追加 user 消息，不误改 system（backlog #1 附带修复）。
func TestInjectTemplateNoUserMessage(t *testing.T) {
	payload := map[string]any{
		"messages": []any{
			map[string]any{"role": "system", "content": "sys"},
		},
	}
	out := injectTemplate(payload, "HEAD ", " TAIL")
	msgs := out["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("应追加一条 user 消息，实际 %d 条", len(msgs))
	}
	if msgs[0].(map[string]any)["content"] != "sys" {
		t.Fatal("system 消息不应被改动")
	}
	u := msgs[1].(map[string]any)
	if u["role"] != "user" || u["content"] != "HEAD  TAIL" {
		t.Fatalf("追加的 user 消息 = %v", u)
	}
}
