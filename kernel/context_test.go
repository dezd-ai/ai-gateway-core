package kernel

import (
	"net/http"
	"testing"
)

func TestPluginContextSetGet(t *testing.T) {
	ctx := NewPluginContext()
	ctx.Set("k", "v")
	if ctx.GetString("k") != "v" {
		t.Fatal("GetString 应返回已设置值")
	}
	if _, ok := ctx.Get("missing"); ok {
		t.Fatal("缺失键不应返回 ok")
	}
}

func TestRecordPluginDedup(t *testing.T) {
	ctx := NewPluginContext()
	ctx.RecordPlugin("auth_vkey")
	ctx.RecordPlugin("auth_vkey")
	ctx.RecordPlugin("auditor")
	list := ctx.ExecutedPlugins()
	if len(list) != 2 || list[0] != "auth_vkey" || list[1] != "auditor" {
		t.Fatalf("应去重且保序，实际 %v", list)
	}
}

func TestAbortRoundTrip(t *testing.T) {
	ctx := NewPluginContext()
	ctx.Abort(http.StatusForbidden, []byte("blocked"))
	aborted, status, body := ctx.IsAborted()
	if !aborted || status != http.StatusForbidden || string(body) != "blocked" {
		t.Fatalf("abort 往返不一致: %v %d %q", aborted, status, body)
	}
}

func TestNewTraceIDUnique(t *testing.T) {
	a := NewTraceID()
	b := NewTraceID()
	if a == b {
		t.Fatal("TraceID 应全局唯一")
	}
	if len(a) != 32 {
		t.Fatalf("TraceID 长度应为 32，实际 %d", len(a))
	}
}
