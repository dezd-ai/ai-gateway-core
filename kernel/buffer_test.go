package kernel

import (
	"errors"
	"strings"
	"testing"
)

// TestReadBodyLimit 验证请求体限额读取（内存防线 层 1）：限额内正常、超限报错、0=不限。
func TestReadBodyLimit(t *testing.T) {
	out, err := ReadBody(strings.NewReader("hello"), 10)
	if err != nil || string(out) != "hello" {
		t.Fatalf("限额内读取失败: %v %q", err, out)
	}

	if _, err := ReadBody(strings.NewReader("hello world"), 5); !errors.Is(err, ErrBodyTooLarge) {
		t.Fatalf("超限应返回 ErrBodyTooLarge, got %v", err)
	}

	out, err = ReadBody(strings.NewReader("hello world"), 0)
	if err != nil || string(out) != "hello world" {
		t.Fatalf("0 应表示不限, got %v %q", err, out)
	}
}
