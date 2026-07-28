package engine

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// 合約測試：事件經 HTTP 送達橋端，kind/label 與 unity_demo 的投影表（backend/main.py）對齊。
func TestOfficeReporterContract(t *testing.T) {
	var mu sync.Mutex
	var got []officeEvent
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/office/event" {
			t.Errorf("路徑錯誤: %s", r.URL.Path)
		}
		var ev officeEvent
		_ = json.NewDecoder(r.Body).Decode(&ev)
		mu.Lock()
		got = append(got, ev)
		mu.Unlock()
	}))
	defer srv.Close()

	ctx := context.Background()
	r := NewOfficeReporter(srv.URL+"/", "p17") // 尾斜線要被容忍
	r.Begin("盤點 TODO")
	r.OnToolCall(ctx, "bash", `{"command":"grep TODO"}`)
	r.OnToolResult(ctx, "bash", "3 處", false)
	r.OnToolResult(ctx, "bash", "boom", true)
	r.OnMessage(ctx, "完成")
	r.End(errors.New("網路中斷"))
	r.Close() // 排空後 got 即完整

	want := []officeEvent{
		{Agent: "p17", Kind: "start", Label: "盤點 TODO"},
		{Agent: "p17", Kind: "tool", Label: "bash", Detail: `{"command":"grep TODO"}`},
		{Agent: "p17", Kind: "result", Label: "bash", Detail: "3 處"},
		{Agent: "p17", Kind: "error", Label: "bash", Detail: "boom"},
		{Agent: "p17", Kind: "msg", Label: "完成"},
		{Agent: "p17", Kind: "done", Label: "error", Detail: "網路中斷"},
	}
	if len(got) != len(want) {
		t.Fatalf("事件數 %d != %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("事件 %d: got %+v want %+v", i, got[i], want[i])
		}
	}
}

// 橋不在線：事件靜默丟、Close 不卡死。
func TestOfficeReporterBridgeDown(t *testing.T) {
	r := NewOfficeReporter("http://127.0.0.1:1", "p17") // 連線秒拒
	r.Begin("x")
	r.End(nil)
	r.Close() // 卡住即測試逾時失敗
}
